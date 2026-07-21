package skill

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	skillrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/skill"
)

// AdminListParams holds parameters for admin skill listing.
type AdminListParams struct {
	Query      string
	CategoryID string
	Tags       []string
	Limit      int
	Offset     int
	Sort       string
}

// AdminCreateParams holds parameters for admin skill creation.
type AdminCreateParams struct {
	ParseTaskID string
	Name        string
	DisplayName string
	IconURL     string
	Description string
	CategoryID  string
	Tags        json.RawMessage
	Version     string
	AdminUID    string
	AdminName   string
}

// AdminUpdateParams holds parameters for admin skill update.
type AdminUpdateParams struct {
	Name        *string
	DisplayName *string
	IconURL     *string
	Description *string
	CategoryID  *string
	Tags        json.RawMessage
}

// AdminReuploadParams holds parameters for admin skill reupload.
type AdminReuploadParams struct {
	ParseTaskID string
	Version     string
	Changelog   string
	Tags        json.RawMessage
	AdminUID    string
}

// AdminList returns public skills without Space restriction.
func (s *Service) AdminList(ctx context.Context, p AdminListParams) (*ListResult, error) {
	tags := normalizeTags(p.Tags)
	tagIDGroups, err := s.resolveListTagIDGroups(ctx, skillrepo.GlobalTagSpaceID, tags)
	if err != nil {
		return nil, err
	}
	if len(tags) > 0 && len(tagIDGroups) == 0 {
		return &ListResult{Items: []SkillItem{}, Total: 0}, nil
	}
	repoResult, err := s.repo.AdminList(ctx, skillrepo.AdminListFilter{
		Query:       p.Query,
		CategoryID:  p.CategoryID,
		Tags:        tags,
		TagIDGroups: tagIDGroups,
		Limit:       p.Limit,
		Offset:      p.Offset,
		Sort:        p.Sort,
	})
	if err != nil {
		return nil, err
	}
	return s.toListResult(ctx, repoResult), nil
}

// AdminGet returns a single public skill by ID without Space restriction.
func (s *Service) AdminGet(ctx context.Context, id string) (*SkillItem, error) {
	row, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if row == nil || row.Visibility != "public" {
		return nil, ErrNotFound
	}
	item := s.rowToItem(ctx, row)
	return &item, nil
}

// AdminCreate creates a new public skill from a completed admin parse task.
func (s *Service) AdminCreate(ctx context.Context, p AdminCreateParams) (*SkillItem, error) {
	pt, err := s.repo.GetParseTask(ctx, p.ParseTaskID)
	if err != nil {
		return nil, err
	}
	if pt == nil || pt.Status != "success" {
		return nil, ErrInvalidParseTask
	}
	if pt.OwnerID != p.AdminUID || pt.SpaceID != skillrepo.GlobalTagSpaceID {
		return nil, ErrInvalidParseTask
	}
	// Reject reupload tasks
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

	id := s.idGen()
	versionID := s.idGen()

	zipData, err := s.readVerifiedTempZip(ctx, pt)
	if err != nil {
		return nil, err
	}

	// Build raw metadata from parse task for vendor field preservation
	var rawMeta map[string]interface{}
	if pt.ResultMetadata != nil {
		_ = json.Unmarshal(pt.ResultMetadata, &rawMeta)
	}

	// Parse tag names for rewrite
	var rewriteTags []string
	_ = json.Unmarshal(tags, &rewriteTags)

	// RewriteZipPackage: inject id and updated metadata
	rewriteResult, err := RewriteZipPackage(
		bytes.NewReader(zipData), int64(len(zipData)),
		RewriteParams{
			Name:        name,
			Desc:        description,
			Version:     version,
			Tags:        rewriteTags,
			ID:          id,
			RawMetadata: rawMeta,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("rewrite zip: %w", err)
	}

	zipObjectKey, skillMdObjectKey := versionObjectKeys(id, versionID)

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
	readmeContent := ""
	if rewriteResult.SkillMD != nil {
		readmeContent = extractReadmeBody(rewriteResult.SkillMD)
	}

	// Transactionally: consume parse task, create skill, and insert version
	row, err := s.repo.CreateSkillAndConsumeTask(ctx, p.ParseTaskID, skillrepo.CreateParams{
		ID:               id,
		Name:             name,
		DisplayName:      p.DisplayName,
		IconURL:          p.IconURL,
		CurrentVersionID: versionID,
		Description:      description,
		CategoryID:       p.CategoryID,
		Tags:             tags,
		OwnerID:          p.AdminUID,
		OwnerName:        p.AdminName,
		SpaceID:          skillrepo.GlobalTagSpaceID,
		Visibility:       "public",
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
		Changelog: "初始发布",
		Storage:   string(storageJSON),
		ChangedBy: p.AdminUID,
	})
	if err != nil {
		_ = s.store.DeleteObject(ctx, zipObjectKey)
		_ = s.store.DeleteObject(ctx, skillMdObjectKey)
		if errors.Is(err, skillrepo.ErrParseTaskAlreadyConsumed) {
			return nil, ErrParseTaskConsumed
		}
		if errors.Is(err, skillrepo.ErrNameTaken) {
			return nil, ErrNameTaken
		}
		return nil, err
	}

	// Best-effort cleanup of the temporary zip
	if pt.FileURL != zipObjectKey {
		_ = s.store.DeleteObject(ctx, pt.FileURL)
	}

	item := s.rowToItem(ctx, row)
	return &item, nil
}

// AdminUpdate updates basic info of a public skill.
func (s *Service) AdminUpdate(ctx context.Context, id string, p AdminUpdateParams) (*SkillItem, error) {
	row, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if row == nil || row.Visibility != "public" {
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

	repoParams := skillrepo.UpdateParams{
		Name:        p.Name,
		DisplayName: p.DisplayName,
		IconURL:     p.IconURL,
		Description: p.Description,
		CategoryID:  p.CategoryID,
		Tags:        p.Tags,
	}
	if p.Tags != nil {
		normalized, tagNames, err := normalizeRawTags(p.Tags)
		if err != nil {
			return nil, ErrInvalidTags
		}
		repoParams.Tags = normalized
		repoParams.TagNames = tagNames
	}

	_, err = s.repo.UpdateWithTagsScoped(ctx, id, row.SpaceID, row.OwnerID, skillrepo.GlobalTagSpaceID, row.OwnerID, repoParams)
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

// AdminDelete soft-deletes a public skill.
func (s *Service) AdminDelete(ctx context.Context, id string) error {
	row, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if row == nil || row.Visibility != "public" {
		return ErrNotFound
	}

	versions, err := s.repo.ListVersions(ctx, id)
	if err != nil {
		return err
	}
	objectKeys := collectDeleteObjectKeys(row.FileURL, append([]string{row.VersionStorage}, versionStorageStrings(versions)...)...)

	affected, err := s.repo.Delete(ctx, id)
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrNotFound
	}
	for _, key := range objectKeys {
		_ = s.store.DeleteObject(ctx, key)
	}

	return nil
}

func versionStorageStrings(versions []skillrepo.VersionRow) []string {
	storages := make([]string, 0, len(versions))
	for _, version := range versions {
		storages = append(storages, version.Storage)
	}
	return storages
}

func collectDeleteObjectKeys(fileURL string, storages ...string) []string {
	seen := make(map[string]struct{})
	var keys []string
	add := func(key string) {
		if key == "" {
			return
		}
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		keys = append(keys, key)
	}

	add(fileURL)
	for _, raw := range storages {
		if raw == "" {
			continue
		}
		var vs model.VersionStorage
		if json.Unmarshal([]byte(raw), &vs) == nil {
			add(vs.ZipObjectKey)
			add(vs.SkillMdObjectKey)
		}
		var legacy struct {
			ObjectKey string `json:"object_key"`
		}
		if json.Unmarshal([]byte(raw), &legacy) == nil {
			add(legacy.ObjectKey)
		}
	}
	return keys
}

// AdminGetSkillMD retrieves the SKILL.md content for a public skill.
func (s *Service) AdminGetSkillMD(ctx context.Context, id string) ([]byte, error) {
	row, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if row == nil || row.Visibility != "public" {
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
		return nil, ErrNoFile
	}

	reader, err := s.store.GetObject(ctx, vs.SkillMdObjectKey)
	if err != nil {
		return nil, fmt.Errorf("get skill md: %w", err)
	}
	defer reader.Close()

	data, err := readLimited(reader, maxSkillMDReadBytes)
	if err != nil {
		return nil, fmt.Errorf("read skill md: %w", err)
	}
	return data, nil
}

// AdminReupload uploads a new version for a public skill.
func (s *Service) AdminReupload(ctx context.Context, id string, p AdminReuploadParams) (*SkillItem, error) {
	row, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if row == nil || row.Visibility != "public" {
		return nil, ErrNotFound
	}

	pt, err := s.repo.GetParseTask(ctx, p.ParseTaskID)
	if err != nil {
		return nil, err
	}
	if pt == nil || pt.Status != "success" {
		return nil, ErrInvalidParseTask
	}
	if pt.OwnerID != p.AdminUID || pt.SpaceID != skillrepo.GlobalTagSpaceID {
		return nil, ErrInvalidParseTask
	}
	if pt.SkillID != "" && pt.SkillID != id {
		return nil, ErrInvalidParseTask
	}
	// Validate zip embedded id matches current skill id
	if pt.ResultID != "" && pt.ResultID != id {
		return nil, ErrIDMismatch
	}
	// Validate SKILL.md name matches current skill before applying a reupload.
	if pt.ResultName != "" && pt.ResultName != row.Name {
		_ = s.store.DeleteObject(ctx, pt.FileURL)
		return nil, ErrNameMismatch
	}

	// Determine version
	version := p.Version
	if version == "" {
		version = pt.ResultVersion
	}
	if version == "" {
		version = row.Version
	}

	// Build update params from parse results
	repoParams := skillrepo.UpdateParams{}
	if pt.ResultName != "" {
		repoParams.Name = &pt.ResultName
	}
	if pt.ResultDescription != nil {
		repoParams.Description = pt.ResultDescription
	}
	if version != "" {
		repoParams.Version = &version
	}

	// Handle tags
	var tags json.RawMessage
	if p.Tags != nil {
		tags = p.Tags
	} else if pt.ResultTags != nil {
		tags = pt.ResultTags
	}
	if tags != nil {
		normalized, tagNames, err := normalizeRawTags(tags)
		if err != nil {
			return nil, ErrInvalidTags
		}
		repoParams.Tags = normalized
		repoParams.TagNames = tagNames
	}

	zipData, err := s.readVerifiedTempZip(ctx, pt)
	if err != nil {
		return nil, err
	}

	// Build raw metadata from parse task
	var rawMeta map[string]interface{}
	if pt.ResultMetadata != nil {
		_ = json.Unmarshal(pt.ResultMetadata, &rawMeta)
	}

	// Resolve fields for rewrite
	resolvedName := row.Name
	if repoParams.Name != nil {
		resolvedName = *repoParams.Name
	}
	resolvedDesc := row.Description
	if repoParams.Description != nil {
		resolvedDesc = *repoParams.Description
	}
	var rewriteTags []string
	if repoParams.Tags != nil {
		_ = json.Unmarshal(repoParams.Tags, &rewriteTags)
	}

	// RewriteZipPackage
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

	versionID := s.idGen()
	zipObjectKey, skillMdObjectKey := versionObjectKeys(id, versionID)

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

	// Backfill file columns
	fileName := "skill.zip"
	repoParams.FileName = &fileName
	repoParams.FileSize = &rewriteResult.ZipSize
	repoParams.FileSHA256 = &rewriteResult.ZipSHA256
	repoParams.FileURL = &zipObjectKey

	repoParams.CurrentVersionID = &versionID

	// Transactionally: consume parse task, update skill, and insert version
	err = s.repo.AdminUpdateSkillAndConsumeTask(ctx, id, row.OwnerID, p.AdminUID, skillrepo.GlobalTagSpaceID, repoParams, p.ParseTaskID, id, &model.SkillVersion{
		ID:        versionID,
		SkillID:   id,
		Version:   version,
		Changelog: p.Changelog,
		Storage:   string(storageJSON),
		ChangedBy: p.AdminUID,
	})
	if err != nil {
		_ = s.store.DeleteObject(ctx, zipObjectKey)
		_ = s.store.DeleteObject(ctx, skillMdObjectKey)
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
	if pt.FileURL != zipObjectKey {
		_ = s.store.DeleteObject(ctx, pt.FileURL)
	}

	// Re-fetch to return updated data
	updated, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	item := s.rowToItem(ctx, updated)
	return &item, nil
}

// AdminGetDownloadInfo resolves the artifact download URL for a public skill (no space/user check).
func (s *Service) AdminGetDownloadInfo(ctx context.Context, id string) (*DownloadInfo, error) {
	item, err := s.AdminGet(ctx, id)
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
