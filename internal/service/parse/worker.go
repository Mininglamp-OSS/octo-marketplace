package parse

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	mdsanitize "github.com/Mininglamp-OSS/octo-marketplace/internal/markdown"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/storage"
)

const defaultWorkerPoolSize = 10
const defaultWorkerQueueMultiplier = 10

var (
	statusUpdateTimeout = 5 * time.Second
	ErrParseQueueFull   = errors.New("parse queue is full")
)

// WorkerConfig holds configuration for the parse worker pool.
type WorkerConfig struct {
	PoolSize     int
	QueueSize    int
	ParseTimeout time.Duration
}

type parseJob struct {
	taskID      string
	objectKey   string
	maxZipBytes int64
	ctx         context.Context
	done        chan struct{}
}

// Worker manages the async parsing goroutine pool.
type Worker struct {
	store        storage.Storage
	repo         *Repo
	db           *sql.DB
	jobs         chan parseJob
	jobWG        sync.WaitGroup
	parseTimeout time.Duration
}

// NewWorker creates a parse worker with a bounded goroutine pool.
func NewWorker(store storage.Storage, repo *Repo, db *sql.DB, cfg WorkerConfig) *Worker {
	poolSize := cfg.PoolSize
	if poolSize <= 0 {
		poolSize = defaultWorkerPoolSize
	}
	queueSize := cfg.QueueSize
	if queueSize <= 0 {
		queueSize = poolSize * defaultWorkerQueueMultiplier
	}
	parseTimeout := cfg.ParseTimeout
	if parseTimeout <= 0 {
		parseTimeout = time.Minute
	}
	w := &Worker{
		store:        store,
		repo:         repo,
		db:           db,
		jobs:         make(chan parseJob, queueSize),
		parseTimeout: parseTimeout,
	}
	for i := 0; i < poolSize; i++ {
		go w.run()
	}
	return w
}

func (w *Worker) run() {
	for job := range w.jobs {
		w.runJob(job)
	}
}

func (w *Worker) runJob(job parseJob) {
	defer w.jobWG.Done()
	if job.done != nil {
		defer close(job.done)
	}
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[parse-worker] panic recovered for task %s: %v", job.taskID, r)
			w.updateFailed(job.taskID, "INTERNAL_ERROR", fmt.Sprintf("panic: %v", r))
		}
	}()
	w.process(job.ctx, job.taskID, job.objectKey, job.maxZipBytes)
}

// Submit enqueues a parse job without blocking. It returns ErrParseQueueFull
// when both running and queued work are already at the configured bound.
func (w *Worker) Submit(taskID, objectKey string, maxZipBytes int64) error {
	w.jobWG.Add(1)
	job := parseJob{
		taskID:      taskID,
		objectKey:   objectKey,
		maxZipBytes: maxZipBytes,
		ctx:         context.Background(),
	}
	select {
	case w.jobs <- job:
		return nil
	default:
		w.jobWG.Done()
		return ErrParseQueueFull
	}
}

// ProcessSync runs a parse job in the bounded worker pool and waits for it to finish.
func (w *Worker) ProcessSync(ctx context.Context, taskID, objectKey string, maxZipBytes int64) error {
	if ctx == nil {
		ctx = context.Background()
	}
	done := make(chan struct{})
	w.jobWG.Add(1)
	job := parseJob{
		taskID:      taskID,
		objectKey:   objectKey,
		maxZipBytes: maxZipBytes,
		ctx:         ctx,
		done:        done,
	}
	select {
	case w.jobs <- job:
	case <-ctx.Done():
		w.jobWG.Done()
		return ctx.Err()
	}

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Wait blocks until all running parse jobs complete. Used for graceful shutdown.
func (w *Worker) Wait() {
	w.jobWG.Wait()
}

func (w *Worker) process(parent context.Context, taskID, objectKey string, maxZipBytes int64) {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, w.parseTimeout)
	defer cancel()

	// 1. Download zip from storage to a temp file
	tmpDir, err := os.MkdirTemp("", "parse-*")
	if err != nil {
		w.updateFailed(taskID, "INTERNAL_ERROR", "cannot create temp dir")
		return
	}
	defer os.RemoveAll(tmpDir)

	zipPath := filepath.Join(tmpDir, "skill.zip")
	if err := w.downloadToFile(ctx, objectKey, zipPath, maxZipBytes); err != nil {
		if errors.Is(err, ErrFileTooLarge) {
			w.deleteOversizedObject(objectKey)
			w.updateFailed(taskID, "FILE_TOO_LARGE", "uploaded object exceeds size limit")
			return
		}
		w.updateFailed(taskID, "INTERNAL_ERROR", "download failed: "+err.Error())
		return
	}

	// 2. Compute SHA256
	sha, err := fileSHA256(zipPath)
	if err != nil {
		w.updateFailed(taskID, "INTERNAL_ERROR", "sha256 failed")
		return
	}

	// 3. Extract zip and find SKILL.md
	result, errCode, errMsg := ExtractZip(zipPath, maxZipBytes)
	if errCode != "" {
		w.updateFailed(taskID, errCode, errMsg)
		return
	}

	// 4. Parse frontmatter
	fm, body := ParseFrontmatter(result.SkillMDContent)

	// 4.1 Validate name (required, hyphen-case, max 64)
	if errMsg := validateSkillName(fm.Name); errMsg != "" {
		w.updateFailed(taskID, "INVALID_SKILL_MD", errMsg)
		return
	}
	// 4.2 Validate description (required, max 1024, no < >)
	if errMsg := validateSkillDescription(fm.Description); errMsg != "" {
		w.updateFailed(taskID, "INVALID_SKILL_MD", errMsg)
		return
	}

	// 4.3 Check reupload target name before global duplicate checks, so a wrong package
	// reports mismatch instead of colliding with an unrelated Skill.
	task, err := w.repo.GetByID(ctx, taskID)
	if err != nil {
		w.updateFailed(taskID, "INTERNAL_ERROR", "cannot fetch parse task")
		return
	}
	if mismatchErr := w.checkReuploadNameMatch(ctx, fm.Name, task.SpaceID, task.OwnerID, task.SkillID); mismatchErr != "" {
		w.updateFailed(taskID, "SKILL_NAME_MISMATCH", mismatchErr)
		return
	}
	if dupErr := w.checkNameDuplicate(ctx, fm.Name, task.SpaceID, task.OwnerID, task.SkillID); dupErr != "" {
		w.updateFailed(taskID, "DUPLICATE_NAME", dupErr)
		return
	}

	// 5. Sanitize results
	name := sanitizeString(fm.Name, 64)
	desc := sanitizeString(fm.Description, 1024)
	version := sanitizeString(fm.Version, 32)
	if version == "" {
		version = "1.0.0"
	}

	var descPtr *string
	if desc != "" {
		descPtr = &desc
	}

	tags, _ := json.Marshal(fm.Tags)
	if fm.Tags == nil {
		tags = []byte("[]")
	}

	// Limit readme content
	readme := truncateUTF8Bytes(body, 1024*1024)
	readme = mdsanitize.Sanitize(readme)
	var readmePtr *string
	if readme != "" {
		readmePtr = &readme
	}

	// Marshal metadata from frontmatter
	resultID := sanitizeString(fm.ID, 36)
	forkedFrom := sanitizeString(fm.ForkedFrom, 36)
	var metadataJSON json.RawMessage
	if fm.Metadata != nil {
		metadataJSON, _ = json.Marshal(fm.Metadata)
	}

	// 6. Update task as success
	w.updateSuccess(taskID, name, descPtr, version, tags, readmePtr, sha, resultID, forkedFrom, metadataJSON)
}

func (w *Worker) downloadToFile(ctx context.Context, key, dst string, maxBytes int64) error {
	info, err := w.store.StatObject(ctx, key)
	if err != nil {
		return err
	}
	if info.Size <= 0 || info.Size > maxBytes {
		return ErrFileTooLarge
	}

	rc, err := w.store.GetObject(ctx, key)
	if err != nil {
		return err
	}
	defer rc.Close()

	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer f.Close()

	limited := io.LimitReader(rc, maxBytes+1)
	n, err := io.Copy(f, limited)
	if err != nil {
		return err
	}
	if n > maxBytes {
		return ErrFileTooLarge
	}
	return nil
}

func (w *Worker) deleteOversizedObject(key string) {
	ctx, cancel := context.WithTimeout(context.Background(), statusUpdateTimeout)
	defer cancel()
	if err := w.store.DeleteObject(ctx, key); err != nil {
		log.Printf("[parse-worker] failed to delete oversized object %s: %v", key, err)
	}
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (w *Worker) updateFailed(taskID, errorCode, errorMessage string) {
	ctx, cancel := context.WithTimeout(context.Background(), statusUpdateTimeout)
	defer cancel()
	if errorMessage != "" {
		log.Printf("[parse-worker] task %s failed code=%s detail=%s", taskID, errorCode, errorMessage)
	}
	_ = w.repo.UpdateFailed(ctx, taskID, errorCode, publicParseErrorMessageWithDetail(errorCode, errorMessage))
}

func (w *Worker) updateSuccess(taskID string, name string, description *string, version string, tags json.RawMessage, readme *string, sha256 string, resultID string, forkedFrom string, metadata json.RawMessage) {
	ctx, cancel := context.WithTimeout(context.Background(), statusUpdateTimeout)
	defer cancel()
	if err := w.repo.UpdateSuccess(ctx, taskID, name, description, version, tags, readme, sha256, resultID, forkedFrom, metadata); err != nil {
		log.Printf("[parse-worker] update success failed for task %s: %v", taskID, err)
	}
}

func sanitizeString(s string, maxLen int) string {
	s = strings.ToValidUTF8(replaceNullBytes(s), "")
	runes := []rune(s)
	if len(runes) > maxLen {
		s = string(runes[:maxLen])
	}
	return s
}

func truncateUTF8Bytes(s string, maxBytes int) string {
	if maxBytes < 0 {
		return ""
	}
	s = strings.ToValidUTF8(s, "")
	if len(s) <= maxBytes {
		return s
	}
	end := maxBytes
	for end > 0 && !utf8.ValidString(s[:end]) {
		end--
	}
	return s[:end]
}

func replaceNullBytes(s string) string {
	result := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] != 0 {
			result = append(result, s[i])
		}
	}
	return string(result)
}

// validateSkillName checks name against hyphen-case rules.
// Returns empty string if valid, error message if not.
func validateSkillName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "SKILL.md 缺少必填字段 name"
	}
	if len(name) > 64 {
		return fmt.Sprintf("name 超过 64 字符限制（当前 %d）", len(name))
	}
	if strings.HasPrefix(name, "-") || strings.HasSuffix(name, "-") {
		return "name 不能以连字符 - 开头或结尾"
	}
	if strings.Contains(name, "--") {
		return "name 不能包含连续连字符 --"
	}
	for _, c := range name {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-') {
			return fmt.Sprintf("name 只能包含字母、数字和连字符（发现非法字符 '%c'）", c)
		}
	}
	return ""
}

// validateSkillDescription checks description rules.
func validateSkillDescription(desc string) string {
	desc = strings.TrimSpace(desc)
	if desc == "" {
		return "SKILL.md 缺少必填字段 description"
	}
	if utf8.RuneCountInString(desc) > 1024 {
		return fmt.Sprintf("description 超过 1024 字符限制（当前 %d）", utf8.RuneCountInString(desc))
	}
	if strings.ContainsAny(desc, "<>") {
		return "description 不能包含尖括号 < 或 >"
	}
	return ""
}

func (w *Worker) checkReuploadNameMatch(ctx context.Context, name, spaceID, ownerID, skillID string) string {
	if skillID == "" {
		return ""
	}

	var currentName string
	err := w.db.QueryRowContext(ctx,
		"SELECT name FROM skills WHERE id = ? AND space_id = ? AND owner_id = ?",
		skillID, spaceID, ownerID,
	).Scan(&currentName)
	if err == sql.ErrNoRows {
		return "目标 Skill 不存在或无权限"
	}
	if err != nil {
		log.Printf("[parse-worker] checkReuploadNameMatch query error: %v", err)
		return "内部错误：无法验证重新上传的 Skill 名称"
	}
	if name != currentName {
		return fmt.Sprintf("重新上传的 Skill 与当前 Skill 不一致：上传 Skill name 为 %q，当前 Skill name 为 %q", name, currentName)
	}
	return ""
}

// checkNameDuplicate checks if a skill name already exists for the same owner in the same space.
// excludeSkillID is used for re-upload (update) to skip self.
func (w *Worker) checkNameDuplicate(ctx context.Context, name, spaceID, ownerID, excludeSkillID string) string {
	query := `
		SELECT id FROM skills
		WHERE name = ? AND space_id = ? AND owner_id = ?
	`
	args := []interface{}{name, spaceID, ownerID}

	if excludeSkillID != "" {
		query += " AND id != ?"
		args = append(args, excludeSkillID)
	}

	query += " LIMIT 1"

	var existingID string
	err := w.db.QueryRowContext(ctx, query, args...).Scan(&existingID)
	if err == sql.ErrNoRows {
		return ""
	}
	if err != nil {
		log.Printf("[parse-worker] checkNameDuplicate query error: %v", err)
		return "内部错误：无法验证名称唯一性"
	}
	return fmt.Sprintf("skill name \"%s\" 已存在（ID: %s），请使用其他名称", name, existingID)
}
