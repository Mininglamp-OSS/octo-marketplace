package model

import (
	"encoding/json"
	"time"
)

// Skill visibility values (Public/Private/Space) share the Visibility type
// defined in mcp.go; see mcp.go for the full set of constants.

// SkillVersion represents a version record in the skill's release history.
type SkillVersion struct {
	ID        string    `json:"id"`
	SkillID   string    `json:"skill_id"`
	Version   string    `json:"version"`
	Changelog string    `json:"changelog"`
	Storage   string    `json:"storage"` // JSON: {"type":"s3","object_key":"...","readme_key":"..."}
	ChangedBy string    `json:"changed_by"`
	CreatedAt time.Time `json:"created_at"`
}

// Skill represents a published marketplace skill.
type Skill struct {
	ID            string          `json:"id"`
	Name          string          `json:"name"`
	DisplayName   string          `json:"display_name"`
	IconURL       string          `json:"icon_url"`
	Description   string          `json:"description"`
	CategoryID    string          `json:"category_id"`
	Tags          json.RawMessage `json:"tags"`
	OwnerID       string          `json:"owner_id"`
	OwnerName     string          `json:"owner_name"`
	SpaceID       string          `json:"space_id"`
	Visibility    Visibility      `json:"visibility"`
	Version       string          `json:"version"`
	ReadmeContent string          `json:"readme_content"`
	FileName      string          `json:"file_name"`
	FileURL       string          `json:"file_url"`
	FileSize      int64           `json:"file_size"`
	FileSHA256    string          `json:"file_sha256"`
	CreatedAt     time.Time       `json:"created_at"`
	UpdatedAt     time.Time       `json:"updated_at"`
}
