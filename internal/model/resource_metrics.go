package model

import "time"

// ResourceMetrics represents a row in the resource_metrics table.
type ResourceMetrics struct {
	ResourceType  string    `json:"resource_type"`
	ResourceID    string    `json:"resource_id"`
	ViewCount     int64     `json:"view_count"`
	DownloadCount int64     `json:"download_count"`
	InstallCount  int64     `json:"install_count"`
	UpdatedAt     time.Time `json:"updated_at"`
}
