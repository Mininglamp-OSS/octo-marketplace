// Package migration plans snapshot conversions without external side effects.
package migration

import (
	"encoding/json"
	"errors"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

var ErrIssues = errors.New("migration plan has blocking issues")

type Input struct {
	CategorySnapshot         map[string]string
	CategorySnapshotCaptured bool
	SnapshotID               string
	Resources                []Resource
	Versions                 []model.PluginVersion
	Releases                 []model.PluginVersionRelease
}

// SourceEvidence binds the original SQL inventory and object bytes for one resource.
type SourceObject struct {
	Key, Hash string
	Size      int64
}
type SourceEvidence struct {
	RowHash string
	Tables  map[string]string
	Objects []SourceObject
}
type Resource struct {
	IconFile           *File `json:"-"`
	Evidence           SourceEvidence
	SourceRowHash      string
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
	CategorySnapshot         map[string]string `json:"-"`
	CategorySnapshotCaptured bool              `json:"-"`
	ID, SnapshotID           string
	Nodes                    []Node
	Mappings                 []Mapping
	Issues                   []Issue
}
type Node struct {
	IconFile      *File                     `json:"-"`
	Evidence      map[string]SourceEvidence `json:"-"`
	SourceRowHash string                    `json:"-"`
	// Source publication evidence is private plan state, checked under lock before writes.
	SourcePublishedVersionID *string                     `json:"-"`
	SourceRelease            *model.PluginVersionRelease `json:"-"`
	SourceID                 string
	Source                   model.Plugin           `json:"-"`
	Plugin                   model.Plugin           `json:"-"`
	Relations                []model.PluginRelation `json:"-"`
	VersionID, VersionLabel  string
	Artifacts                json.RawMessage `json:"-"`
	Release, Clone           bool
	Files                    []File `json:"-"`
}
type Mapping struct{ SourceID, TargetID, VersionID, Disposition string }
type Issue struct{ ResourceID, Code string }

// DatabaseIdentity binds a run to the existing database and its MySQL server.
type DatabaseIdentity struct {
	Name       string `json:"name"`
	ServerUUID string `json:"server_uuid"`
}
