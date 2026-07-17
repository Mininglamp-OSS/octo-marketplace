package skill

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
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
	Visibility    string          `json:"visibility"`
	Version       string          `json:"version"`
	ReadmeContent string          `json:"readme_content,omitempty"`
	FileName      string          `json:"file_name"`
	FileSize      int64           `json:"file_size"`
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
		MineOnly:   false,
	})
	if err != nil {
		return nil, err
	}
	return s.toListResult(ctx, repoResult), nil
}

// ListMine returns skills owned by the user.
func (s *Service) ListMine(ctx context.Context, p ListParams) (*ListResult, error) {
	repoResult, err := s.repo.List(ctx, skillrepo.ListFilter{
		SpaceID:  p.SpaceID,
		UserID:   p.UserID,
		Query:    p.Query,
		Tags:     normalizeTags(p.Tags),
		Cursor:   p.Cursor,
		Limit:    p.Limit,
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
	ParseTaskID string
	Name        string
	DisplayName string
	IconURL     string
	Description string
	CategoryID  string
	Tags        json.RawMessage
	Visibility  string
	Version     string
	UserID      string
	UserName    string
	SpaceID     string
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
	readmeContent := ""
	if pt.ResultReadme != nil {
		readmeContent = mdsanitize.Sanitize(*pt.ResultReadme)
	}

	visibility := p.Visibility
	if visibility == "" {
		visibility = "space"
	}

	id := s.idGen()

	// Compute final object key: skills/{skill_id}/v{version}/{file_name}
	finalKey := fmt.Sprintf("skills/%s/v%s/%s", id, version, pt.FileName)

	// Relocate the file BEFORE committing the DB transaction.
	// If copy fails, we don't consume the parse task — user can retry.
	if pt.FileURL != finalKey {
		if err := s.store.CopyObject(ctx, pt.FileURL, finalKey); err != nil {
			return nil, fmt.Errorf("relocate uploaded file: %w", err)
		}
	}

	row, err := s.repo.CreateSkillAndConsumeTask(ctx, p.ParseTaskID, skillrepo.CreateParams{
		ID:            id,
		Name:          name,
		DisplayName:   p.DisplayName,
		IconURL:       p.IconURL,
		Description:   description,
		CategoryID:    p.CategoryID,
		Tags:          tags,
		OwnerID:       p.UserID,
		OwnerName:     p.UserName,
		SpaceID:       p.SpaceID,
		Visibility:    toVisibility(visibility),
		Version:       version,
		ReadmeContent: readmeContent,
		FileName:      pt.FileName,
		FileURL:       finalKey,
		FileSize:      pt.FileSize,
		FileSHA256:    pt.FileSHA256,
		TagNames:      tagNames,
	})
	if err != nil {
		if errors.Is(err, skillrepo.ErrParseTaskAlreadyConsumed) {
			return nil, ErrParseTaskConsumed
		}
		if errors.Is(err, skillrepo.ErrNameTaken) {
			return nil, ErrNameTaken
		}
		return nil, err
	}

	// Record initial version in history
	if verErr := s.repo.InsertVersion(ctx, model.SkillVersion{
		ID:        s.idGen(),
		SkillID:   id,
		Version:   version,
		Changelog: "初始发布",
		Storage:   fmt.Sprintf(`{"type":"s3","object_key":%q}`, finalKey),
		ChangedBy: p.UserID,
	}); verErr != nil {
		log.Printf("[skill] InsertVersion failed for skill %s: %v", id, verErr)
	}

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

		// Determine version for final key
		version := row.Version
		if p.Version != nil {
			version = *p.Version
		} else if pt.ResultVersion != "" {
			version = pt.ResultVersion
		}

		// Compute final object key
		finalKey := fmt.Sprintf("skills/%s/v%s/%s", id, version, pt.FileName)

		// Apply file metadata from parse task
		repoParams.FileName = &pt.FileName
		repoParams.FileSize = &pt.FileSize
		repoParams.FileSHA256 = &pt.FileSHA256
		repoParams.FileURL = &finalKey

		// Apply parsed content if not overridden in the request
		if pt.ResultReadme != nil {
			readme := mdsanitize.Sanitize(*pt.ResultReadme)
			repoParams.ReadmeContent = &readme
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

		// Relocate file BEFORE committing the DB transaction.
		// If copy fails, we don't consume the parse task — user can retry.
		if pt.FileURL != finalKey {
			if err := s.store.CopyObject(ctx, pt.FileURL, finalKey); err != nil {
				return nil, fmt.Errorf("relocate uploaded file: %w", err)
			}
		}

		// Transactionally update skill and consume parse task
		taskSkillID := pt.SkillID
		if taskSkillID == "" {
			taskSkillID = id // for tasks not explicitly linked
		}
		err = s.repo.UpdateSkillAndConsumeTask(ctx, id, repoParams, p.ParseTaskID, userID, spaceID, taskSkillID)
		if err != nil {
			if errors.Is(err, skillrepo.ErrParseTaskAlreadyConsumed) {
				return nil, ErrParseTaskConsumed
			}
			if errors.Is(err, skillrepo.ErrNameTaken) {
				return nil, ErrNameTaken
			}
			return nil, err
		}

		// Record new version in history
		if verErr := s.repo.InsertVersion(ctx, model.SkillVersion{
			ID:        s.idGen(),
			SkillID:   id,
			Version:   version,
			Changelog: p.Changelog,
			Storage:   fmt.Sprintf(`{"type":"s3","object_key":%q}`, finalKey),
			ChangedBy: userID,
		}); verErr != nil {
			log.Printf("[skill] InsertVersion failed for skill %s v%s: %v", id, version, verErr)
		}

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

// Delete hard-deletes a skill. Only the owner within the same space can delete.
func (s *Service) Delete(ctx context.Context, id, userID, spaceID string) error {
	row, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if row == nil || row.OwnerID != userID || row.SpaceID != spaceID {
		return ErrNotFound
	}
	_, err = s.repo.Delete(ctx, id)
	return err
}

func toVisibility(v string) model.Visibility {
	return model.Visibility(v)
}

func toVisibilityPtr(v string) *model.Visibility {
	vis := model.Visibility(v)
	return &vis
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
		SpaceID:       row.SpaceID,
		Visibility:    row.Visibility,
		Version:       row.Version,
		ReadmeContent: mdsanitize.Sanitize(row.ReadmeContent),
		FileName:      row.FileName,
		FileURL:       row.FileURL,
		FileSize:      row.FileSize,
		FileSHA256:    row.FileSHA256,
		CreatedAt:     row.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
		UpdatedAt:     row.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z"),
	}
}

func (s *Service) toListResult(ctx context.Context, r *skillrepo.ListResult) *ListResult {
	items := make([]SkillItem, 0, len(r.Items))
	for i := range r.Items {
		items = append(items, s.rowToItem(ctx, &r.Items[i]))
	}
	return &ListResult{Items: items, NextCursor: r.NextCursor}
}

// isURL returns true if the string looks like a full URL (not an object key).
func isURL(s string) bool {
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
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
