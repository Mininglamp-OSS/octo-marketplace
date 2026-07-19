package parse

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	mdsanitize "github.com/Mininglamp-OSS/octo-marketplace/internal/markdown"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/storage"
)

// Service handles the upload/parse business logic.
type Service struct {
	store        storage.Storage
	repo         *Repo
	worker       *Worker
	idGen        func() string
	maxMB        int
	staleTimeout time.Duration
	maxAttempts  int
}

// ServiceConfig holds configuration for the parse service.
type ServiceConfig struct {
	StaleTimeout time.Duration
	MaxAttempts  int
}

// NewService creates a parse service.
func NewService(store storage.Storage, repo *Repo, worker *Worker, idGen func() string, maxMB int, cfg ServiceConfig) *Service {
	staleTimeout := cfg.StaleTimeout
	if staleTimeout <= 0 {
		staleTimeout = 5 * time.Minute
	}
	maxAttempts := cfg.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 2
	}
	return &Service{
		store:        store,
		repo:         repo,
		worker:       worker,
		idGen:        idGen,
		maxMB:        maxMB,
		staleTimeout: staleTimeout,
		maxAttempts:  maxAttempts,
	}
}

// ErrInvalidFileName indicates the file_name is not a safe .zip basename.
var ErrInvalidFileName = errors.New("file_name must be a safe .zip basename")

// ErrFileTooLarge indicates the file exceeds the upload limit.
var ErrFileTooLarge = errors.New("file too large")

// ErrTaskNotFound indicates the parse task was not found.
var ErrTaskNotFound = errors.New("task not found")

// ErrTaskNotPending indicates the task is not in pending status.
var ErrTaskNotPending = errors.New("task not in pending status")

// ErrForbidden indicates the user does not own the resource.
var ErrForbidden = errors.New("forbidden")

// InitResult is returned from InitUpload.
type InitResult struct {
	UploadID     string            `json:"skill_upload_id"`
	PresignedURL string            `json:"presigned_url"`
	ExpiresIn    int               `json:"expires_in"`
	Method       string            `json:"method"`
	Headers      map[string]string `json:"headers"`
}

// InitUpload generates a presigned URL and creates a pending parse task.
func (s *Service) InitUpload(ctx context.Context, fileName string, fileSize int64, ownerID, spaceID string) (*InitResult, error) {
	fileName, err := normalizeUploadFileName(fileName)
	if err != nil {
		return nil, ErrInvalidFileName
	}
	maxBytes := int64(s.maxMB) * 1024 * 1024
	if fileSize > maxBytes {
		return nil, ErrFileTooLarge
	}

	uploadID := s.idGen()
	objectKey := fmt.Sprintf("skill-uploads/%s/%s", uploadID, fileName)

	url, headers, err := s.store.PresignPut(ctx, objectKey, "application/zip", time.Hour)
	if err != nil {
		return nil, fmt.Errorf("presign put: %w", err)
	}

	// Create pending task
	taskID := uploadID // Use same ID for simplicity
	task := &TaskRow{
		ID:       taskID,
		UploadID: uploadID,
		FileName: fileName,
		FileSize: fileSize,
		FileURL:  objectKey,
		Status:   "pending",
		OwnerID:  ownerID,
		SpaceID:  spaceID,
	}
	if err := s.repo.Create(ctx, task); err != nil {
		return nil, fmt.Errorf("create task: %w", err)
	}

	headerMap := make(map[string]string)
	for k, v := range headers {
		if len(v) > 0 {
			headerMap[k] = v[0]
		}
	}

	return &InitResult{
		UploadID:     uploadID,
		PresignedURL: url,
		ExpiresIn:    3600,
		Method:       "PUT",
		Headers:      headerMap,
	}, nil
}

// InitReupload generates a presigned URL for re-uploading to an existing skill.
func (s *Service) InitReupload(ctx context.Context, skillID, fileName string, fileSize int64, ownerID, spaceID string) (*InitResult, error) {
	fileName, err := normalizeUploadFileName(fileName)
	if err != nil {
		return nil, ErrInvalidFileName
	}
	maxBytes := int64(s.maxMB) * 1024 * 1024
	if fileSize > maxBytes {
		return nil, ErrFileTooLarge
	}

	uploadID := s.idGen()
	objectKey := fmt.Sprintf("skill-uploads/%s/%s", uploadID, fileName)

	url, headers, err := s.store.PresignPut(ctx, objectKey, "application/zip", time.Hour)
	if err != nil {
		return nil, fmt.Errorf("presign put: %w", err)
	}

	// Create pending task linked to existing skill
	taskID := uploadID
	task := &TaskRow{
		ID:       taskID,
		UploadID: uploadID,
		FileName: fileName,
		FileSize: fileSize,
		FileURL:  objectKey,
		Status:   "pending",
		OwnerID:  ownerID,
		SpaceID:  spaceID,
		SkillID:  skillID,
	}
	if err := s.repo.Create(ctx, task); err != nil {
		return nil, fmt.Errorf("create task: %w", err)
	}

	headerMap := make(map[string]string)
	for k, v := range headers {
		if len(v) > 0 {
			headerMap[k] = v[0]
		}
	}

	return &InitResult{
		UploadID:     uploadID,
		PresignedURL: url,
		ExpiresIn:    3600,
		Method:       "PUT",
		Headers:      headerMap,
	}, nil
}

// TriggerParse starts the async parsing for a given upload_id.
func (s *Service) TriggerParse(ctx context.Context, uploadID, ownerID string) (string, error) {
	task, err := s.repo.GetByUploadID(ctx, uploadID)
	if err != nil {
		return "", err
	}
	if task == nil {
		return "", ErrTaskNotFound
	}
	if task.OwnerID != ownerID {
		return "", ErrForbidden
	}
	if task.Status != "pending" {
		return "", ErrTaskNotPending
	}

	ok, err := s.repo.TransitionPendingToParsing(ctx, task.ID)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", ErrTaskNotPending
	}

	// Submit to worker pool
	maxBytes := int64(s.maxMB) * 1024 * 1024
	s.worker.Submit(task.ID, task.FileURL, maxBytes)

	return task.ID, nil
}

// ParseUploadSync starts parsing a previously initialized upload and waits for
// the parse worker to finish. It is used by bot publish flows after the bot has
// uploaded the archive to the presigned URL.
func (s *Service) ParseUploadSync(ctx context.Context, uploadID, ownerID string) (*PollResult, error) {
	task, err := s.repo.GetByUploadID(ctx, uploadID)
	if err != nil {
		return nil, err
	}
	if task == nil {
		return nil, ErrTaskNotFound
	}
	if task.OwnerID != ownerID {
		return nil, ErrForbidden
	}
	if task.Status == "success" {
		return s.GetParseStatus(ctx, task.ID, ownerID)
	}
	if task.Status != "pending" {
		return nil, ErrTaskNotPending
	}

	ok, err := s.repo.TransitionPendingToParsing(ctx, task.ID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrTaskNotPending
	}

	maxBytes := int64(s.maxMB) * 1024 * 1024
	s.worker.process(task.ID, task.FileURL, maxBytes)
	return s.GetParseStatus(ctx, task.ID, ownerID)
}

func normalizeUploadFileName(fileName string) (string, error) {
	fileName = strings.TrimSpace(fileName)
	if !isSupportedSkillPackageName(fileName) {
		return "", ErrInvalidFileName
	}
	return normalizeObjectFileName(fileName)
}

func isSupportedSkillPackageName(fileName string) bool {
	lower := strings.ToLower(fileName)
	return strings.HasSuffix(lower, ".zip") || strings.HasSuffix(lower, ".skill")
}

func normalizeObjectFileName(fileName string) (string, error) {
	if fileName == "" || fileName != filepath.Base(fileName) {
		return "", ErrInvalidFileName
	}
	if strings.ContainsAny(fileName, `/\`) || strings.Contains(fileName, "..") || strings.ContainsRune(fileName, 0) {
		return "", ErrInvalidFileName
	}
	return fileName, nil
}

// PollResult is returned from GetParseStatus.
type PollResult struct {
	Status string      `json:"status"`
	TaskID string      `json:"skill_parse_task_id"`
	Result *ParseData  `json:"result,omitempty"`
	Error  *ParseError `json:"error,omitempty"`
}

// ParseData holds the successful parse result.
type ParseData struct {
	Name          string   `json:"name"`
	Description   string   `json:"description,omitempty"`
	Version       string   `json:"version"`
	Tags          []string `json:"tags"`
	ReadmeContent string   `json:"readme_content,omitempty"`
	FileName      string   `json:"file_name"`
	FileSize      int64    `json:"file_size"`
	FileSHA256    string   `json:"file_sha256"`
}

// ParseError holds the failure details.
type ParseError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// GetParseStatus polls the parse task status.
// When the task is stuck in 'parsing' beyond staleTimeout, it attempts atomic
// recovery: claiming the task via TryRecoverStaleParsing and re-submitting to
// the worker pool. If max attempts are exceeded, it marks the task as failed.
func (s *Service) GetParseStatus(ctx context.Context, taskID, ownerID string) (*PollResult, error) {
	task, err := s.repo.GetByID(ctx, taskID)
	if err != nil {
		return nil, err
	}
	if task == nil {
		return nil, ErrTaskNotFound
	}
	if task.OwnerID != ownerID {
		return nil, ErrForbidden
	}

	result := &PollResult{
		Status: task.Status,
		TaskID: task.ID,
	}

	switch task.Status {
	case "success":
		var tags []string
		if task.ResultTags != nil {
			_ = json.Unmarshal(task.ResultTags, &tags)
		}
		if tags == nil {
			tags = []string{}
		}
		desc := ""
		if task.ResultDescription != nil {
			desc = *task.ResultDescription
		}
		readme := ""
		if task.ResultReadme != nil {
			readme = mdsanitize.Sanitize(*task.ResultReadme)
		}
		result.Result = &ParseData{
			Name:          task.ResultName,
			Description:   desc,
			Version:       task.ResultVersion,
			Tags:          tags,
			ReadmeContent: readme,
			FileName:      task.FileName,
			FileSize:      task.FileSize,
			FileSHA256:    task.FileSHA256,
		}
	case "failed":
		result.Error = &ParseError{
			Code:    task.ErrorCode,
			Message: publicParseErrorMessage(task.ErrorCode),
		}
	case "parsing":
		// Check if the task is stale and attempt recovery.
		staleCutoff := time.Now().Add(-s.staleTimeout)
		if task.UpdatedAt.Before(staleCutoff) {
			// Task appears stale — attempt atomic recovery.
			if task.Attempts >= s.maxAttempts {
				// Exhausted retries; mark as failed.
				_ = s.repo.MarkRetryExhausted(ctx, task.ID)
				result.Status = "failed"
				result.Error = &ParseError{
					Code:    "PARSE_RETRY_EXHAUSTED",
					Message: publicParseErrorMessage("PARSE_RETRY_EXHAUSTED"),
				}
				return result, nil
			}

			staleSeconds := int(s.staleTimeout.Seconds())
			won, err := s.repo.TryRecoverStaleParsing(ctx, task.ID, staleSeconds, s.maxAttempts)
			if err != nil {
				// Recovery SQL failed — return current parsing status.
				return result, nil
			}
			if won {
				// This pod won the race — re-submit to the worker pool.
				maxBytes := int64(s.maxMB) * 1024 * 1024
				s.worker.Submit(task.ID, task.FileURL, maxBytes)
			}
			// Either way, status is still parsing (recovery just kicked off).
		}
	}

	return result, nil
}

// IconUploadResult is returned from InitIconUpload.
type IconUploadResult struct {
	ObjectKey    string            `json:"object_key"`
	PresignedURL string            `json:"presigned_url"`
	ExpiresIn    int               `json:"expires_in"`
	Method       string            `json:"method"`
	Headers      map[string]string `json:"headers"`
	// DownloadURL is the persistent (non-signed) URL where the icon will be
	// reachable once the PUT completes. Callers store this on the record so
	// that later reads never touch a signed URL. Filled by InitMcpIconUpload;
	// legacy skill InitIconUpload leaves it empty for backwards compatibility.
	DownloadURL string `json:"download_url,omitempty"`
}

// InitIconUpload generates a presigned URL for uploading a skill icon image.
func (s *Service) InitIconUpload(ctx context.Context, fileName string, fileSize int64, ownerID string) (*IconUploadResult, error) {
	fileName = strings.TrimSpace(fileName)
	// Validate image extension
	lower := strings.ToLower(fileName)
	if !strings.HasSuffix(lower, ".png") && !strings.HasSuffix(lower, ".jpg") && !strings.HasSuffix(lower, ".jpeg") && !strings.HasSuffix(lower, ".svg") {
		return nil, errors.New("file must be an image (png/jpg/jpeg/svg)")
	}
	if _, err := normalizeObjectFileName(fileName); err != nil {
		return nil, ErrInvalidFileName
	}
	// Limit icon to 2MB
	if fileSize > 2*1024*1024 {
		return nil, ErrFileTooLarge
	}

	id := s.idGen()
	objectKey := fmt.Sprintf("icons/%s/%s", id, fileName)

	contentType := "image/png"
	if strings.HasSuffix(lower, ".jpg") || strings.HasSuffix(lower, ".jpeg") {
		contentType = "image/jpeg"
	} else if strings.HasSuffix(lower, ".svg") {
		contentType = "image/svg+xml"
	}

	iconTTL := time.Hour
	url, headers, err := s.store.PresignPut(ctx, objectKey, contentType, iconTTL)
	if err != nil {
		return nil, fmt.Errorf("presign put icon: %w", err)
	}

	headerMap := make(map[string]string)
	for k, v := range headers {
		if len(v) > 0 {
			headerMap[k] = v[0]
		}
	}

	return &IconUploadResult{
		ObjectKey:    objectKey,
		PresignedURL: url,
		ExpiresIn:    int(iconTTL.Seconds()),
		Method:       "PUT",
		Headers:      headerMap,
	}, nil
}

// GetDownloadURL generates a presigned download URL for a skill's zip file.
func (s *Service) GetDownloadURL(ctx context.Context, objectKey string) (string, error) {
	url, err := s.store.PresignGet(ctx, objectKey, 5*time.Minute)
	if err != nil {
		return "", err
	}
	return url, nil
}

// InitMcpIconUpload is the MCP-icon-specific twin of InitIconUpload. Two
// changes over the skill variant:
//  1. Key prefix is `mcp-icons/{uuid}/{filename}` so MCP and Skill assets stay
//     in separate bucket sub-trees.
//  2. Result includes DownloadURL — the persistent URL where the icon will be
//     reachable once the client has PUT the bytes to PresignedURL. Callers
//     store DownloadURL on the MCP record; they never need to re-fetch a
//     presigned GET for the same key.
//
// Accepts the same image formats as octo-admin's client-side validator
// (png / jpg / jpeg / webp / gif). ownerID may be empty when the caller is
// an admin (no user identity) — we don't tie the object key to a subject.
func (s *Service) InitMcpIconUpload(ctx context.Context, fileName string, fileSize int64) (*IconUploadResult, error) {
	fileName = strings.TrimSpace(fileName)
	lower := strings.ToLower(fileName)
	if !strings.HasSuffix(lower, ".png") &&
		!strings.HasSuffix(lower, ".jpg") &&
		!strings.HasSuffix(lower, ".jpeg") &&
		!strings.HasSuffix(lower, ".webp") &&
		!strings.HasSuffix(lower, ".gif") {
		return nil, errors.New("file must be an image (png/jpg/jpeg/webp/gif)")
	}
	if _, err := normalizeObjectFileName(fileName); err != nil {
		return nil, ErrInvalidFileName
	}
	if fileSize > 2*1024*1024 {
		return nil, ErrFileTooLarge
	}

	id := s.idGen()
	objectKey := fmt.Sprintf("mcp-icons/%s/%s", id, fileName)

	contentType := "image/png"
	switch {
	case strings.HasSuffix(lower, ".jpg"), strings.HasSuffix(lower, ".jpeg"):
		contentType = "image/jpeg"
	case strings.HasSuffix(lower, ".webp"):
		contentType = "image/webp"
	case strings.HasSuffix(lower, ".gif"):
		contentType = "image/gif"
	}

	iconTTL := time.Hour
	putURL, headers, err := s.store.PresignPut(ctx, objectKey, contentType, iconTTL)
	if err != nil {
		return nil, fmt.Errorf("presign put mcp icon: %w", err)
	}
	// Compute the persistent URL BEFORE the upload — the client will store
	// this URL on the MCP record after successfully PUTting to putURL.
	downloadURL, err := s.store.PublicURL(ctx, objectKey)
	if err != nil {
		return nil, fmt.Errorf("public url for mcp icon: %w", err)
	}

	headerMap := make(map[string]string)
	for k, v := range headers {
		if len(v) > 0 {
			headerMap[k] = v[0]
		}
	}

	return &IconUploadResult{
		ObjectKey:    objectKey,
		PresignedURL: putURL,
		ExpiresIn:    int(iconTTL.Seconds()),
		Method:       "PUT",
		Headers:      headerMap,
		DownloadURL:  downloadURL,
	}, nil
}
