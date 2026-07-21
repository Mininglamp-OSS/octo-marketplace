package skill

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	mdsanitize "github.com/Mininglamp-OSS/octo-marketplace/internal/markdown"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	categoryrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/category"
	skillrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/skill"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/storage"
)

// Service handles business logic for skills.
type Service struct {
	repo    *skillrepo.Repo
	catRepo *categoryrepo.Repo
	store   storage.Storage
	idGen   func() string
}

// New creates a skill service.
func New(repo *skillrepo.Repo, catRepo *categoryrepo.Repo, store storage.Storage, idGen func() string) *Service {
	return &Service{repo: repo, catRepo: catRepo, store: store, idGen: idGen}
}

// ErrNotFound indicates the skill was not found or access denied.
var ErrNotFound = errors.New("skill not found")

// ErrForbidden indicates the user does not own the skill.
var ErrForbidden = errors.New("forbidden")

// ErrInvalidParseTask indicates the parse task is invalid for creating a skill.
var ErrInvalidParseTask = errors.New("invalid parse task")

// ErrParseTaskConsumed indicates the parse task has already been used to create a skill.
var ErrParseTaskConsumed = errors.New("parse task already consumed")

// ErrCategoryNotFound indicates the specified category does not exist.
var ErrCategoryNotFound = errors.New("category not found")

// ErrNameTaken indicates that the requested name is already used by another
// skill owned by the caller in the current Space.
var ErrNameTaken = errors.New("skill name taken")

// ErrInvalidTags indicates the tags field is not a JSON string array.
var ErrInvalidTags = errors.New("invalid tags")

// ErrNoFile indicates that the skill version has no downloadable file.
var ErrNoFile = errors.New("no file available")

// ErrIDMismatch indicates the zip's embedded id does not match the target skill.
var ErrIDMismatch = errors.New("zip id mismatch")

// SkillItem is the API-facing representation of a skill.
type SkillItem struct {
	ID            string          `json:"skill_id"`
	Name          string          `json:"name"`
	DisplayName   string          `json:"display_name"`
	IconURL       string          `json:"icon_url"`
	Description   string          `json:"description"`
	CategoryID    string          `json:"category_id"`
	Tags          json.RawMessage `json:"tags"`
	OwnerName     string          `json:"owner_name"`
	CreatorID     string          `json:"creator_id"`
	CreatorName   string          `json:"creator_name"`
	Visibility    string          `json:"visibility"`
	Version       string          `json:"version"`
	ReadmeContent string          `json:"readme_content,omitempty"`
	FileName      string          `json:"file_name"`
	FileSize      int64           `json:"file_size"`
	ViewCount     int64           `json:"view_count"`
	DownloadCount int64           `json:"download_count"`
	CreatedAt     string          `json:"created_at"`
	UpdatedAt     string          `json:"updated_at"`

	// Internal authorization and storage metadata. These fields are required
	// by download handlers but must never be serialized in catalog responses.
	OwnerID    string `json:"-"`
	SpaceID    string `json:"-"`
	FileURL    string `json:"-"`
	FileSHA256 string `json:"-"`
}

// ListResult holds paginated skill items.
type ListResult struct {
	Items      []SkillItem `json:"items"`
	NextCursor *string     `json:"next_cursor"`
	Total      int         `json:"total,omitempty"` // set for offset-based pagination
}

// TagItem is the API-facing representation of a Space-scoped skill tag.
type TagItem struct {
	Name      string `json:"name"`
	CreatedBy string `json:"created_by"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// ListParams are the parameters for listing skills.
type ListParams struct {
	SpaceID    string
	UserID     string
	Query      string
	CategoryID string
	Tags       []string
	Cursor     string
	Limit      int
	Offset     int
	Sort       string // comprehensive, latest, downloads, views
}

// List returns skills visible to the user.
func (s *Service) List(ctx context.Context, p ListParams) (*ListResult, error) {
	repoResult, err := s.repo.List(ctx, skillrepo.ListFilter{
		SpaceID:    p.SpaceID,
		UserID:     p.UserID,
		Query:      p.Query,
		CategoryID: p.CategoryID,
		Tags:       normalizeTags(p.Tags),
		Cursor:     p.Cursor,
		Limit:      p.Limit,
		Offset:     p.Offset,
		Sort:       p.Sort,
		MineOnly:   false,
	})
	if err != nil {
		return nil, err
	}
	return s.toListResult(ctx, repoResult), nil
}

// ListMine returns skills owned by the user. Always uses latest (cursor) sort
// to preserve backward-compatible cursor pagination on the /skills/mine endpoint.
func (s *Service) ListMine(ctx context.Context, p ListParams) (*ListResult, error) {
	repoResult, err := s.repo.List(ctx, skillrepo.ListFilter{
		SpaceID:  p.SpaceID,
		UserID:   p.UserID,
		Query:    p.Query,
		Tags:     normalizeTags(p.Tags),
		Cursor:   p.Cursor,
		Limit:    p.Limit,
		Sort:     skillrepo.SortLatest,
		MineOnly: true,
	})
	if err != nil {
		return nil, err
	}
	return s.toListResult(ctx, repoResult), nil
}

// Get returns a single skill by ID, checking visibility.
func (s *Service) Get(ctx context.Context, id, spaceID, userID string) (*SkillItem, error) {
	row, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, ErrNotFound
	}
	if !canView(row, spaceID, userID) {
		return nil, ErrNotFound
	}
	item := s.rowToItem(ctx, row)
	return &item, nil
}

// CreateParams holds the request data for creating a skill.
type CreateParams struct {
	ParseTaskID   string
	Name          string
	DisplayName   string
	IconURL       string
	Description   string
	CategoryID    string
	Tags          json.RawMessage
	Visibility    string
	Version       string
	Changelog     string
	SourceSkillID string // optional: fork source
	UserID        string
	UserName      string
	SpaceID       string
	CreatorID     string
	CreatorName   string
}

// Create creates a new skill from a completed parse task.
func (s *Service) Create(ctx context.Context, p CreateParams) (*SkillItem, error) {
	// Validate parse task
	pt, err := s.repo.GetParseTask(ctx, p.ParseTaskID)
	if err != nil {
		return nil, err
	}
	if pt == nil || pt.OwnerID != p.UserID || pt.Status != "success" {
		return nil, ErrInvalidParseTask
	}
	// Enforce space isolation: parse task must belong to the same space
	if pt.SpaceID != p.SpaceID {
		return nil, ErrInvalidParseTask
	}
	// Reject reupload tasks — they must be used with PUT update, not POST create
	if pt.SkillID != "" {
		return nil, ErrInvalidParseTask
	}

	// Validate category
	if p.CategoryID != "" {
		exists, err := s.catRepo.Exists(ctx, p.CategoryID)
		if err != nil {
			return nil, err
		}
		if !exists {
			return nil, ErrCategoryNotFound
		}
	}

	// Use parse task results as defaults, allow override
	name := p.Name
	if name == "" {
		name = pt.ResultName
	}
	description := p.Description
	if description == "" && pt.ResultDescription != nil {
		description = *pt.ResultDescription
	}
	version := p.Version
	if version == "" {
		version = pt.ResultVersion
	}
	if version == "" {
		version = "1.0.0"
	}
	tags := p.Tags
	if tags == nil {
		tags = pt.ResultTags
	}
	if tags == nil {
		tags = json.RawMessage(`[]`)
	}
	tags, tagNames, err := normalizeRawTags(tags)
	if err != nil {
		return nil, ErrInvalidTags
	}

	// Determine source_skill_id: API param first, else result_id from parse, else empty
	sourceSkillID := p.SourceSkillID
	if sourceSkillID == "" && pt.ResultID != "" {
		sourceSkillID = pt.ResultID
	}

	visibility := p.Visibility
	if visibility == "" {
		visibility = "space"
	}

	id := s.idGen()
	versionID := s.idGen()

	// Download the temporary zip from object storage
	zipReader, err := s.store.GetObject(ctx, pt.FileURL)
	if err != nil {
		return nil, fmt.Errorf("download temp zip: %w", err)
	}
	zipData, err := io.ReadAll(zipReader)
	zipReader.Close()
	if err != nil {
		return nil, fmt.Errorf("read temp zip: %w", err)
	}

	// Build raw metadata from parse task for vendor field preservation
	var rawMeta map[string]interface{}
	if pt.ResultMetadata != nil {
		_ = json.Unmarshal(pt.ResultMetadata, &rawMeta)
	}

	// Parse tag names for rewrite
	var rewriteTags []string
	_ = json.Unmarshal(tags, &rewriteTags)

	// RewriteZipPackage: inject id, forkedFrom, and updated metadata
	rewriteResult, err := RewriteZipPackage(
		bytes.NewReader(zipData), int64(len(zipData)),
		RewriteParams{
			Name:        name,
			Desc:        description,
			Version:     version,
			Tags:        rewriteTags,
			ID:          id,
			ForkedFrom:  sourceSkillID,
			RawMetadata: rawMeta,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("rewrite zip: %w", err)
	}

	// Compute object keys
	zipObjectKey := fmt.Sprintf("skills/%s/v%s/skill.zip", id, version)
	skillMdObjectKey := fmt.Sprintf("skills/%s/v%s/SKILL.md", id, version)

	// PutObject: upload rewritten zip
	if err := s.store.PutObject(ctx, zipObjectKey, bytes.NewReader(rewriteResult.ZipBytes), rewriteResult.ZipSize, "application/zip"); err != nil {
		return nil, fmt.Errorf("upload zip: %w", err)
	}

	// PutObject: upload SKILL.md
	if err := s.store.PutObject(ctx, skillMdObjectKey, bytes.NewReader(rewriteResult.SkillMD), int64(len(rewriteResult.SkillMD)), "text/markdown; charset=utf-8"); err != nil {
		// Best-effort cleanup of the zip
		_ = s.store.DeleteObject(ctx, zipObjectKey)
		return nil, fmt.Errorf("upload skill md: %w", err)
	}

	// Build VersionStorage JSON
	vs := model.VersionStorage{
		Type:             "s3",
		ZipObjectKey:     zipObjectKey,
		SkillMdObjectKey: skillMdObjectKey,
		ZipFileName:      "skill.zip",
		ZipSize:          rewriteResult.ZipSize,
		ZipSHA256:        rewriteResult.ZipSHA256,
	}
	storageJSON, _ := json.Marshal(vs)

	// ReadmeContent: extract from SKILL.md body (sanitized, truncated at 1MB)
	readmeContent := ""
	if rewriteResult.SkillMD != nil {
		readmeContent = extractReadmeBody(rewriteResult.SkillMD)
	}

	row, err := s.repo.CreateSkillAndConsumeTask(ctx, p.ParseTaskID, skillrepo.CreateParams{
		ID:               id,
		Name:             name,
		DisplayName:      p.DisplayName,
		IconURL:          p.IconURL,
		SourceSkillID:    sourceSkillID,
		CurrentVersionID: versionID,
		Description:      description,
		CategoryID:       p.CategoryID,
		Tags:             tags,
		OwnerID:          p.UserID,
		OwnerName:        p.UserName,
		CreatorID:        firstNonEmpty(p.CreatorID, p.UserID),
		CreatorName:      firstNonEmpty(p.CreatorName, p.UserName),
		SpaceID:          p.SpaceID,
		Visibility:       toVisibility(visibility),
		Version:          version,
		ReadmeContent:    readmeContent,
		FileName:         "skill.zip",
		FileURL:          zipObjectKey,
		FileSize:         rewriteResult.ZipSize,
		FileSHA256:       rewriteResult.ZipSHA256,
		TagNames:         tagNames,
	}, &model.SkillVersion{
		ID:        versionID,
		SkillID:   id,
		Version:   version,
		Changelog: firstNonEmpty(p.Changelog, "初始发布"),
		Storage:   string(storageJSON),
		ChangedBy: p.UserID,
	})
	if err != nil {
		// Best-effort cleanup of uploaded objects on DB failure
		go func() {
			_ = s.store.DeleteObject(context.Background(), zipObjectKey)
			_ = s.store.DeleteObject(context.Background(), skillMdObjectKey)
		}()
		if errors.Is(err, skillrepo.ErrParseTaskAlreadyConsumed) {
			return nil, ErrParseTaskConsumed
		}
		if errors.Is(err, skillrepo.ErrNameTaken) {
			return nil, ErrNameTaken
		}
		return nil, err
	}

	// Best-effort async cleanup of the temporary zip
	go func() {
		if pt.FileURL != zipObjectKey {
			_ = s.store.DeleteObject(context.Background(), pt.FileURL)
		}
	}()

	item := s.rowToItem(ctx, row)
	return &item, nil
}

// UpdateParams holds fields to update.
type UpdateParams struct {
	Name        *string
	DisplayName *string
	IconURL     *string
	Description *string
	CategoryID  *string
	Tags        json.RawMessage
	Visibility  *string
	Version     *string
	ParseTaskID string // optional: if set, applies reupload parse results
	Changelog   string // version changelog, used when ParseTaskID is set
}

// Update updates a skill. Only the owner within the same space can update.
func (s *Service) Update(ctx context.Context, id, userID, spaceID string, p UpdateParams) (*SkillItem, error) {
	row, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if row == nil || row.OwnerID != userID || row.SpaceID != spaceID {
		return nil, ErrNotFound
	}

	// Validate category if changing
	if p.CategoryID != nil && *p.CategoryID != "" {
		exists, err := s.catRepo.Exists(ctx, *p.CategoryID)
		if err != nil {
			return nil, err
		}
		if !exists {
			return nil, ErrCategoryNotFound
		}
	}

	var vis *model.Visibility
	if p.Visibility != nil {
		vis = toVisibilityPtr(*p.Visibility)
	}

	repoParams := skillrepo.UpdateParams{
		Name:        p.Name,
		DisplayName: p.DisplayName,
		IconURL:     p.IconURL,
		Description: p.Description,
		CategoryID:  p.CategoryID,
		Tags:        p.Tags,
		Visibility:  vis,
		Version:     p.Version,
	}
	if p.Tags != nil {
		normalized, tagNames, err := normalizeRawTags(p.Tags)
		if err != nil {
			return nil, ErrInvalidTags
		}
		repoParams.Tags = normalized
		repoParams.TagNames = tagNames
	}

	// If a parse_task_id is provided, apply reupload results
	if p.ParseTaskID != "" {
		pt, err := s.repo.GetParseTask(ctx, p.ParseTaskID)
		if err != nil {
			return nil, err
		}
		if pt == nil || pt.OwnerID != userID || pt.Status != "success" {
			return nil, ErrInvalidParseTask
		}
		if pt.SpaceID != spaceID {
			return nil, ErrInvalidParseTask
		}
		// Validate the task is associated with this skill (reupload)
		if pt.SkillID != "" && pt.SkillID != id {
			return nil, ErrInvalidParseTask
		}
		// Validate zip embedded id matches current skill id
		if pt.ResultID != "" && pt.ResultID != id {
			return nil, ErrIDMismatch
		}

		// Determine version for final key
		version := row.Version
		if p.Version != nil {
			version = *p.Version
		} else if pt.ResultVersion != "" {
			version = pt.ResultVersion
		}

		// If name/description/version/tags not set in request, use parse results
		if p.Name == nil && pt.ResultName != "" {
			repoParams.Name = &pt.ResultName
		}
		if p.Description == nil && pt.ResultDescription != nil {
			repoParams.Description = pt.ResultDescription
		}
		if p.Version == nil && pt.ResultVersion != "" {
			repoParams.Version = &pt.ResultVersion
		}
		if p.Tags == nil && pt.ResultTags != nil {
			normalized, tagNames, err := normalizeRawTags(pt.ResultTags)
			if err != nil {
				return nil, ErrInvalidTags
			}
			repoParams.Tags = normalized
			repoParams.TagNames = tagNames
		}

		// Download the temporary zip from object storage
		zipReader, err := s.store.GetObject(ctx, pt.FileURL)
		if err != nil {
			return nil, fmt.Errorf("download temp zip: %w", err)
		}
		zipData, err := io.ReadAll(zipReader)
		zipReader.Close()
		if err != nil {
			return nil, fmt.Errorf("read temp zip: %w", err)
		}

		// Build raw metadata from parse task
		var rawMeta map[string]interface{}
		if pt.ResultMetadata != nil {
			_ = json.Unmarshal(pt.ResultMetadata, &rawMeta)
		}

		// Parse tag names for rewrite
		var rewriteTags []string
		resolvedName := row.Name
		if repoParams.Name != nil {
			resolvedName = *repoParams.Name
		}
		resolvedDesc := row.Description
		if repoParams.Description != nil {
			resolvedDesc = *repoParams.Description
		}
		if repoParams.Tags != nil {
			_ = json.Unmarshal(repoParams.Tags, &rewriteTags)
		}

		// RewriteZipPackage: inject skill id (must equal current skill's id)
		rewriteResult, err := RewriteZipPackage(
			bytes.NewReader(zipData), int64(len(zipData)),
			RewriteParams{
				Name:        resolvedName,
				Desc:        resolvedDesc,
				Version:     version,
				Tags:        rewriteTags,
				ID:          id,
				ForkedFrom:  row.SourceSkillID,
				RawMetadata: rawMeta,
			},
		)
		if err != nil {
			return nil, fmt.Errorf("rewrite zip: %w", err)
		}

		// Compute object keys
		zipObjectKey := fmt.Sprintf("skills/%s/v%s/skill.zip", id, version)
		skillMdObjectKey := fmt.Sprintf("skills/%s/v%s/SKILL.md", id, version)

		// PutObject: upload rewritten zip
		if err := s.store.PutObject(ctx, zipObjectKey, bytes.NewReader(rewriteResult.ZipBytes), rewriteResult.ZipSize, "application/zip"); err != nil {
			return nil, fmt.Errorf("upload zip: %w", err)
		}

		// PutObject: upload SKILL.md
		if err := s.store.PutObject(ctx, skillMdObjectKey, bytes.NewReader(rewriteResult.SkillMD), int64(len(rewriteResult.SkillMD)), "text/markdown; charset=utf-8"); err != nil {
			_ = s.store.DeleteObject(ctx, zipObjectKey)
			return nil, fmt.Errorf("upload skill md: %w", err)
		}

		// Build VersionStorage JSON
		vs := model.VersionStorage{
			Type:             "s3",
			ZipObjectKey:     zipObjectKey,
			SkillMdObjectKey: skillMdObjectKey,
			ZipFileName:      "skill.zip",
			ZipSize:          rewriteResult.ZipSize,
			ZipSHA256:        rewriteResult.ZipSHA256,
		}
		storageJSON, _ := json.Marshal(vs)

		// ReadmeContent from SKILL.md body
		readmeContent := extractReadmeBody(rewriteResult.SkillMD)
		repoParams.ReadmeContent = &readmeContent

		// Backfill old columns
		fileName := "skill.zip"
		repoParams.FileName = &fileName
		repoParams.FileSize = &rewriteResult.ZipSize
		repoParams.FileSHA256 = &rewriteResult.ZipSHA256
		repoParams.FileURL = &zipObjectKey

		// Pre-generate version ID and set current_version_id on the skill update
		versionID := s.idGen()
		repoParams.CurrentVersionID = &versionID

		// Transactionally update skill, insert version, and consume parse task
		taskSkillID := pt.SkillID
		if taskSkillID == "" {
			taskSkillID = id // for tasks not explicitly linked
		}
		err = s.repo.UpdateSkillAndConsumeTask(ctx, id, repoParams, p.ParseTaskID, userID, spaceID, taskSkillID, &model.SkillVersion{
			ID:        versionID,
			SkillID:   id,
			Version:   version,
			Changelog: p.Changelog,
			Storage:   string(storageJSON),
			ChangedBy: userID,
		})
		if err != nil {
			// Best-effort cleanup of uploaded objects on DB failure
			go func() {
				_ = s.store.DeleteObject(context.Background(), zipObjectKey)
				_ = s.store.DeleteObject(context.Background(), skillMdObjectKey)
			}()
			if errors.Is(err, skillrepo.ErrParseTaskAlreadyConsumed) {
				return nil, ErrParseTaskConsumed
			}
			if errors.Is(err, skillrepo.ErrSkillNotFound) {
				return nil, ErrNotFound
			}
			if errors.Is(err, skillrepo.ErrNameTaken) {
				return nil, ErrNameTaken
			}
			return nil, err
		}

		// Best-effort cleanup of the temporary zip
		go func() {
			if pt.FileURL != zipObjectKey {
				_ = s.store.DeleteObject(context.Background(), pt.FileURL)
			}
		}()

		// Re-fetch to return updated data
		updated, err := s.repo.GetByID(ctx, id)
		if err != nil {
			return nil, err
		}
		item := s.rowToItem(ctx, updated)
		return &item, nil
	}

	_, err = s.repo.UpdateWithTags(ctx, id, spaceID, userID, repoParams)
	if err != nil {
		if errors.Is(err, skillrepo.ErrSkillNotFound) {
			return nil, ErrNotFound
		}
		if errors.Is(err, skillrepo.ErrNameTaken) {
			return nil, ErrNameTaken
		}
		return nil, err
	}

	// Re-fetch to return updated data
	updated, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	item := s.rowToItem(ctx, updated)
	return &item, nil
}

// ListTags returns Space-scoped tags created by skill create/update flows.
func (s *Service) ListTags(ctx context.Context, spaceID, query string, limit int) ([]TagItem, error) {
	rows, err := s.repo.ListTags(ctx, spaceID, query, limit)
	if err != nil {
		return nil, err
	}
	items := make([]TagItem, 0, len(rows))
	for _, row := range rows {
		items = append(items, TagItem{
			Name:      row.Name,
			CreatedBy: row.CreatedBy,
			CreatedAt: row.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
			UpdatedAt: row.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z"),
		})
	}
	return items, nil
}

// Delete soft-deletes a skill. Only the owner within the same space can delete.
func (s *Service) Delete(ctx context.Context, id, userID, spaceID string) error {
	row, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if row == nil || row.OwnerID != userID || row.SpaceID != spaceID {
		return ErrNotFound
	}
	affected, err := s.repo.Delete(ctx, id)
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

func toVisibility(v string) model.Visibility {
	return model.Visibility(v)
}

func toVisibilityPtr(v string) *model.Visibility {
	vis := model.Visibility(v)
	return &vis
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func canView(row *skillrepo.SkillRow, spaceID, userID string) bool {
	switch row.Visibility {
	case "public":
		// Public skills are visible to all members of the same Space.
		return row.SpaceID == spaceID
	case "space":
		return row.SpaceID == spaceID
	case "private":
		return row.OwnerID == userID && row.SpaceID == spaceID
	default:
		return false
	}
}

func (s *Service) rowToItem(ctx context.Context, row *skillrepo.SkillRow) SkillItem {
	iconURL := row.IconURL
	if iconURL != "" && !isURL(iconURL) {
		// icon_url is an object key — resolve to a presigned download URL
		if resolved, err := s.store.PresignGet(ctx, iconURL, 1*time.Hour); err == nil {
			iconURL = resolved
		}
	}

	// Resolve version/file fields from VersionStorage when available
	version := row.ResolvedVersion
	if version == "" {
		version = row.Version
	}
	fileName := row.FileName
	fileURL := row.FileURL
	fileSize := row.FileSize
	fileSHA256 := row.FileSHA256

	if row.VersionStorage != "" {
		var vs model.VersionStorage
		if err := json.Unmarshal([]byte(row.VersionStorage), &vs); err == nil {
			if vs.ZipObjectKey != "" {
				fileURL = vs.ZipObjectKey
			} else {
				// Legacy storage format: fallback to "object_key"
				var legacy struct {
					ObjectKey string `json:"object_key"`
				}
				if json.Unmarshal([]byte(row.VersionStorage), &legacy) == nil && legacy.ObjectKey != "" {
					fileURL = legacy.ObjectKey
				}
			}
			if vs.ZipFileName != "" {
				fileName = vs.ZipFileName
			}
			if vs.ZipSize > 0 {
				fileSize = vs.ZipSize
			}
			if vs.ZipSHA256 != "" {
				fileSHA256 = vs.ZipSHA256
			}
		}
	}
	if fileName == "" {
		fileName = "skill.zip"
	}

	return SkillItem{
		ID:            row.ID,
		Name:          row.Name,
		DisplayName:   row.DisplayName,
		IconURL:       iconURL,
		Description:   row.Description,
		CategoryID:    row.CategoryID,
		Tags:          row.Tags,
		OwnerID:       row.OwnerID,
		OwnerName:     row.OwnerName,
		CreatorID:     firstNonEmpty(row.CreatorID, row.OwnerID),
		CreatorName:   firstNonEmpty(row.CreatorName, row.OwnerName),
		SpaceID:       row.SpaceID,
		Visibility:    row.Visibility,
		Version:       version,
		ReadmeContent: mdsanitize.Sanitize(row.ReadmeContent),
		FileName:      fileName,
		FileURL:       fileURL,
		FileSize:      fileSize,
		FileSHA256:    fileSHA256,
		ViewCount:     row.ViewCount,
		DownloadCount: row.DownloadCount,
		CreatedAt:     row.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
		UpdatedAt:     row.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z"),
	}
}

func (s *Service) toListResult(ctx context.Context, r *skillrepo.ListResult) *ListResult {
	items := make([]SkillItem, 0, len(r.Items))
	for i := range r.Items {
		items = append(items, s.rowToItem(ctx, &r.Items[i]))
	}
	return &ListResult{Items: items, NextCursor: r.NextCursor, Total: r.Total}
}

// isURL returns true if the string looks like a full URL (not an object key).
func isURL(s string) bool {
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}

// DownloadInfo holds the download URL and integrity digest for a skill.
type DownloadInfo struct {
	DownloadURL string
	FileSHA256  string
}

// GetDownloadInfo resolves the artifact download URL for a visible skill.
// Uses current_version_id → VersionStorage.zip_object_key for the presigned URL.
func (s *Service) GetDownloadInfo(ctx context.Context, id, spaceID, userID string) (*DownloadInfo, error) {
	item, err := s.Get(ctx, id, spaceID, userID)
	if err != nil {
		return nil, err
	}
	if item.FileURL == "" {
		return nil, ErrNoFile
	}
	url, err := s.store.PresignGet(ctx, item.FileURL, 1*time.Hour)
	if err != nil {
		return nil, fmt.Errorf("presign download: %w", err)
	}
	return &DownloadInfo{
		DownloadURL: url,
		FileSHA256:  item.FileSHA256,
	}, nil
}

// GetSkillMD retrieves the SKILL.md content for a visible skill's current version.
// Returns the markdown bytes. Returns ErrNoFile if the version has no skill_md_object_key.
func (s *Service) GetSkillMD(ctx context.Context, id, spaceID, userID string) ([]byte, error) {
	row, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, ErrNotFound
	}
	if !canView(row, spaceID, userID) {
		return nil, ErrNotFound
	}

	// Parse VersionStorage to get skill_md_object_key
	if row.VersionStorage == "" {
		return nil, ErrNoFile
	}
	var vs model.VersionStorage
	if err := json.Unmarshal([]byte(row.VersionStorage), &vs); err != nil {
		return nil, ErrNoFile
	}
	if vs.SkillMdObjectKey == "" {
		// Old version storage without skill_md_object_key — fallback to legacy object_key
		// Check for legacy "object_key" field
		var legacy struct {
			ObjectKey string `json:"object_key"`
		}
		_ = json.Unmarshal([]byte(row.VersionStorage), &legacy)
		if legacy.ObjectKey == "" {
			return nil, ErrNoFile
		}
		// Legacy: no separate SKILL.md file
		return nil, ErrNoFile
	}

	reader, err := s.store.GetObject(ctx, vs.SkillMdObjectKey)
	if err != nil {
		return nil, fmt.Errorf("get skill md: %w", err)
	}
	defer reader.Close()

	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("read skill md: %w", err)
	}
	return data, nil
}

// extractReadmeBody extracts the body (after frontmatter) from SKILL.md content,
// sanitizes it, and truncates to 1MB.
func extractReadmeBody(md []byte) string {
	_, body := splitFrontmatterAndBody(md)
	body = strings.TrimSpace(body)
	if body == "" {
		return ""
	}
	sanitized := mdsanitize.Sanitize(body)
	const maxReadme = 1 << 20 // 1MB
	if len(sanitized) > maxReadme {
		sanitized = sanitized[:maxReadme]
	}
	return sanitized
}

// VersionItem is the API-facing representation of a skill version.
type VersionItem struct {
	ID        string          `json:"skill_version_id"`
	SkillID   string          `json:"skill_id"`
	Version   string          `json:"version"`
	Changelog string          `json:"changelog"`
	Storage   json.RawMessage `json:"storage"`
	ChangedBy string          `json:"changed_by"`
	CreatedAt string          `json:"created_at"`
}

// ListVersions returns version history for a skill. Viewer must have access.
func (s *Service) ListVersions(ctx context.Context, skillID, spaceID, userID string) ([]VersionItem, error) {
	row, err := s.repo.GetByID(ctx, skillID)
	if err != nil {
		return nil, err
	}
	if row == nil || !canView(row, spaceID, userID) {
		return nil, ErrNotFound
	}

	rows, err := s.repo.ListVersions(ctx, skillID)
	if err != nil {
		return nil, err
	}

	items := make([]VersionItem, 0, len(rows))
	for _, r := range rows {
		var storage json.RawMessage
		if r.Storage != "" {
			storage = json.RawMessage(r.Storage)
		} else {
			storage = json.RawMessage(`{}`)
		}
		items = append(items, VersionItem{
			ID:        r.ID,
			SkillID:   r.SkillID,
			Version:   r.Version,
			Changelog: r.Changelog,
			Storage:   storage,
			ChangedBy: r.ChangedBy,
			CreatedAt: r.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
		})
	}
	return items, nil
}
