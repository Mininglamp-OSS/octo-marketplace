package model

import (
	"encoding/json"
	"time"
)

// ParseStatus represents the current state of a parse task.
type ParseStatus string

const (
	ParseStatusPending ParseStatus = "pending"
	ParseStatusParsing ParseStatus = "parsing"
	ParseStatusSuccess ParseStatus = "success"
	ParseStatusFailed  ParseStatus = "failed"
)

// ParseTask represents an asynchronous skill file parsing job.
type ParseTask struct {
	ID                string          `json:"id"`
	UploadID          string          `json:"upload_id"`
	FileURL           string          `json:"file_url"`
	Status            ParseStatus     `json:"status"`
	ErrorCode         string          `json:"error_code"`
	ErrorMessage      string          `json:"error_message"`
	ResultName        string          `json:"result_name"`
	ResultDescription *string         `json:"result_description"`
	ResultVersion     string          `json:"result_version"`
	ResultTags        json.RawMessage `json:"result_tags"`
	ResultReadme      *string         `json:"result_readme"`
	OwnerID           string          `json:"owner_id"`
	SpaceID           string          `json:"space_id"`
	CreatedAt         time.Time       `json:"created_at"`
	UpdatedAt         time.Time       `json:"updated_at"`
}
