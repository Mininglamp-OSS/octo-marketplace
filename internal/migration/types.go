// Package migration plans snapshot conversions without external side effects.
package migration

import (
	"encoding/json"
	"errors"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

var ErrIssues = errors.New("migration plan has blocking issues")

type Input struct {
	SnapshotID string
	Resources  []Resource
	Versions   []model.PluginVersion
	Releases   []model.PluginVersionRelease
}
type Resource struct {
	Plugin             model.Plugin
	Relations          []model.PluginRelation
	Files              []File
	PublishedVersionID *string
	PendingReview      bool
}
type File struct {
	Path, MIME string
	Data       []byte `json:"-"`
	ObjectKey  string
}
type Policy struct{ FrontendChineseSourceIDs []string }
type Plan struct {
	ID, SnapshotID string
	Nodes          []Node
	Mappings       []Mapping
	Issues         []Issue
}
type Node struct {
	SourceID                string
	Source                  model.Plugin           `json:"-"`
	Plugin                  model.Plugin           `json:"-"`
	Relations               []model.PluginRelation `json:"-"`
	VersionID, VersionLabel string
	Artifacts               json.RawMessage `json:"-"`
	Release, Clone          bool
	Files                   []File `json:"-"`
}
type Mapping struct{ SourceID, TargetID, VersionID, Disposition string }
type Issue struct{ ResourceID, Code string }
