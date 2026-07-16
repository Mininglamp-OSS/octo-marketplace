package parse

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/storage"
)

// Service handles the upload/parse business logic.
type Service struct {
	store  storage.Storage
	repo   *Repo
	worker *Worker
	idGen  func() string
	maxMB  int
}

// NewService creates a parse service.
func NewService(store storage.Storage, repo *Repo, worker *Worker, idGen func() string, maxMB int) *Service {
	return &Service{
		store:  store,
		repo:   repo,
		worker: worker,
		idGen:  idGen,
		maxMB:  maxMB,
	}
}

// ErrInvalidFileName indicates the file_name is not a .zip file.
var ErrInvalidFileName = errors.New("file_name must end with .zip")

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
	UploadID     string            `json:"upload_id"`
	PresignedURL string            `json:"presigned_url"`
	ExpiresIn    int               `json:"expires_in"`
	Method       string            `json:"method"`
	Headers      map[string]string `json:"headers"`
}

// InitUpload generates a presigned URL and creates a pending parse task.
func (s *Service) InitUpload(ctx context.Context, fileName string, fileSize int64, ownerID, spaceID string) (*InitResult, error) {
	if !strings.HasSuffix(strings.ToLower(fileName), ".zip") {
		return nil, ErrInvalidFileName
	}
	maxBytes := int64(s.maxMB) * 1024 * 1024
	if fileSize > maxBytes {
		return nil, ErrFileTooLarge
	}

	uploadID := s.idGen()
	objectKey := fmt.Sprintf("skills/%s/%s", uploadID, fileName)

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
	if !strings.HasSuffix(strings.ToLower(fileName), ".zip") {
		return nil, ErrInvalidFileName
	}
	maxBytes := int64(s.maxMB) * 1024 * 1024
	if fileSize > maxBytes {
		return nil, ErrFileTooLarge
	}

	uploadID := s.idGen()
	objectKey := fmt.Sprintf("skills/%s/%s", uploadID, fileName)

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

	// Update status to parsing
	if err := s.repo.UpdateStatus(ctx, task.ID, "parsing"); err != nil {
		return "", err
	}

	// Submit to worker pool
	maxBytes := int64(s.maxMB) * 1024 * 1024
	s.worker.Submit(task.ID, task.FileURL, maxBytes)

	return task.ID, nil
}

// PollResult is returned from GetParseStatus.
type PollResult struct {
	Status string      `json:"status"`
	TaskID string      `json:"task_id"`
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
			readme = *task.ResultReadme
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
			Message: task.ErrorMessage,
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
}

// InitIconUpload generates a presigned URL for uploading a skill icon image.
func (s *Service) InitIconUpload(ctx context.Context, fileName string, fileSize int64, ownerID string) (*IconUploadResult, error) {
	// Validate image extension
	lower := strings.ToLower(fileName)
	if !strings.HasSuffix(lower, ".png") && !strings.HasSuffix(lower, ".jpg") && !strings.HasSuffix(lower, ".jpeg") && !strings.HasSuffix(lower, ".svg") {
		return nil, errors.New("file must be an image (png/jpg/jpeg/svg)")
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
