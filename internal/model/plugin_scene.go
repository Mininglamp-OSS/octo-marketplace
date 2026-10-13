package model

import "time"

// PluginScene is a named Marketplace placement namespace. SceneCode is the
// stable machine identifier sent by catalog clients as scene_code.
type PluginScene struct {
	ID            string
	Code          string
	Name          string
	Description   string
	SortOrder     int
	PluginCount   int
	CategoryCount int
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// AdminPluginPlacement is the admin projection of one Plugin-to-scene
// relationship, enriched with names needed by the management table.
type AdminPluginPlacement struct {
	ID           string
	SceneID      string
	SceneCode    string
	SceneName    string
	PluginID     string
	PluginName   string
	PluginType   PluginType
	CategoryID   *string
	CategoryName *string
	IsVisible    bool
	SortOrder    int
	CreatedAt    time.Time
	UpdatedAt    time.Time
}
