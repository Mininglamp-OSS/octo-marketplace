package model

type DependencyUpdate struct {
	PluginID        string     `json:"plugin_id"`
	Name            string     `json:"name"`
	Type            PluginType `json:"plugin_type"`
	FromVersionID   string     `json:"from_version_id"`
	FromVersion     string     `json:"from_version"`
	TargetVersionID string     `json:"target_version_id"`
	TargetVersion   string     `json:"target_version"`
	IsSkipped       bool       `json:"is_skipped"`
}
type DependencyUpdates struct {
	PluginID           string             `json:"plugin_id"`
	PublishedVersionID string             `json:"published_version_id"`
	DraftRevision      string             `json:"draft_revision"`
	Updates            []DependencyUpdate `json:"updates"`
}
type DependencyUpdateTarget struct {
	PluginID        string `json:"plugin_id" binding:"required"`
	TargetVersionID string `json:"target_version_id" binding:"required"`
}
