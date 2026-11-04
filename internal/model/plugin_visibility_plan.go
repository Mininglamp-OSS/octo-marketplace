package model

// VisibilityPlan contains only the identities needed to confirm an audience
// change. It deliberately carries no packages, configuration or credentials.
type VisibilityPlanItem struct {
	PluginID           string           `json:"plugin_id"`
	Name               string           `json:"name,omitempty"`
	Type               PluginType       `json:"plugin_type"`
	From               PluginVisibility `json:"from"`
	To                 PluginVisibility `json:"to"`
	PublishedVersionID string           `json:"published_version_id,omitempty"`
	Baseline           string           `json:"baseline"`
}
type VisibilityPlanBlocker struct {
	PluginID string `json:"plugin_id"`
	Name     string `json:"name,omitempty"`
	Reason   string `json:"reason"`
}
type VisibilityPlan struct {
	PluginID string                  `json:"plugin_id"`
	Revision string                  `json:"plan_revision"`
	Changes  []VisibilityPlanItem    `json:"changes"`
	Affected []VisibilityPlanItem    `json:"affected"`
	Blockers []VisibilityPlanBlocker `json:"blockers"`
}
