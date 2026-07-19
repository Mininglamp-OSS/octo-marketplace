package model

import "time"

// Category represents a skill classification group.
type Category struct {
	ID        string     `json:"skill_category_id"`
	Name      string     `json:"name"`
	IconKey   string     `json:"icon_key"`
	SortOrder int        `json:"sort_order"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	DeletedAt *time.Time `json:"deleted_at,omitempty"`
}
