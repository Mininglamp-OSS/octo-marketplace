// Both Marketplace installation URLs use the same Fleet capability pipeline.
package plugin

import (
	"context"
	"encoding/json"
	"sort"
	"time"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/logging"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	"go.uber.org/zap"
)

type InstallTracker interface {
	TrackInstall(context.Context, string, string) error
	TrackDownload(context.Context, string, string) error
}

func (s *Service) WithMetrics(m InstallTracker) *Service { s.metrics = m; return s }

type InstallParams struct {
	WorkspaceID string
	RuntimeID   string
	CustomEnv   map[string]string
	// Optional for existing clients. Without a key, each HTTP call is a new install.
	IdempotencyKey string
	Token          string
}
type InstallOutcome struct {
	AgentID string
	SquadID string
}

// Install adapts the original body and response without imposing the newer
// endpoint's UUID or required-header contract on clients that have not upgraded.
func (s *Service) Install(ctx context.Context, caller Caller, pluginID string, p InstallParams) (*InstallOutcome, error) {
	key := p.IdempotencyKey
	if key == "" {
		key = s.id()
	}
	in := InstallationParams{WorkspaceID: p.WorkspaceID, RuntimeID: p.RuntimeID, CustomEnv: p.CustomEnv, IdempotencyKey: key, Token: p.Token}
	normalized, err := normalizeInstallationParams(in, false)
	if err != nil {
		return nil, err
	}
	out, err := s.createInstallation(ctx, caller, pluginID, normalized)
	if err != nil {
		return nil, err
	}
	return &InstallOutcome{AgentID: out.AgentID, SquadID: out.SquadID}, nil
}

// resolveInstallDetail loads a plugin's detail (with relations) for install and
// refuses LOUDLY when the caller cannot see every declared relation target. The
// tenant read filters relation targets by caller visibility (GetWithRelations),
// so a shared parent that legitimately depends on a private child would resolve
// to a short relation list — and the install fan-out would then provision an
// expert with missing skills or a squad missing members/leader while returning
// 200 (P1-1). Comparing the declared count (CountDeclaredRelations, unfiltered)
// against the visible list detects the drop; only the count is used, so nothing
// beyond the number of edges the publisher already declared is revealed. This is
// the visibility-driven sibling of the size-driven loud truncation elsewhere in
// this file. Resolving under the owner's visibility instead is deliberately NOT
// done: that would copy a private dependency's content into any caller's agent,
// a confidentiality decision the owner must take explicitly.
func (s *Service) resolveInstallDetail(ctx context.Context, caller Caller, pluginID string) (*Detail, error) {
	// Install must distinguish a hidden dependency (ErrDependencyHidden) from a
	// resource the caller cannot read at all. The public Detail gate returns
	// ErrNotFound before the historical-relation check, so resolve the released
	// detail without that catalog gate and let checkHistoricalRelations decide.
	detail, err := s.currentDetail(ctx, caller, pluginID, true)
	if err != nil {
		return nil, err
	}
	detail, err = s.publishedDetail(ctx, scope(caller), detail.Plugin, detail.Relations, false)
	if err != nil {
		return nil, err
	}
	declared, err := s.declaredRelationCount(ctx, caller, detail)
	if err != nil {
		return nil, err
	}
	if declared > len(detail.Relations) {
		return nil, ErrDependencyHidden
	}
	return detail, nil
}

// declaredRelationCount returns the number of relations install must be able to
// see. In managed mode the public graph is the released snapshot, so the count
// must come from that same snapshot: a later unpublished draft must never make
// an unchanged release uninstallable, while a hidden released dependency is still
// detected. Legacy resources without a release keep the live current-state count.
func (s *Service) declaredRelationCount(ctx context.Context, caller Caller, d *Detail) (int, error) {
	if s.versionManagement && d.Plugin.Type != model.PluginTypeConnector {
		store, err := s.versionStore()
		if err != nil {
			return 0, err
		}
		sc := scope(caller)
		state, err := store.GetVersionState(ctx, sc, d.Plugin.ID)
		if err != nil {
			return 0, mapStoreError(err)
		}
		if state.PublishedVersionID != nil {
			v, err := store.GetManagedVersion(ctx, sc, d.Plugin.ID, *state.PublishedVersionID)
			if err != nil {
				return 0, mapStoreError(err)
			}
			rels, err := model.DecodeSnapshotRelations(v.Relations)
			if err != nil {
				return 0, ErrIntegrity
			}
			return len(rels), nil
		}
	}
	return s.repo.CountDeclaredRelations(ctx, d.Plugin.ID)
}

func relationsOfType(relations []model.PluginRelation, relationType string) []model.PluginRelation {
	out := make([]model.PluginRelation, 0, len(relations))
	for _, rel := range relations {
		if rel.Type == relationType && rel.Status == 1 && rel.DeletedAt == nil {
			out = append(out, rel)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].SortOrder < out[j].SortOrder })
	return out
}

func manifestDescription(manifest json.RawMessage) string {
	var doc struct {
		Description string `json:"description"`
	}
	_ = json.Unmarshal(manifest, &doc)
	return doc.Description
}

// manifestName reads the display name (`name`) out of a stored manifest. Used
// to preserve an existing skill's curated display name on a package-only
// reupload rather than resetting it to the freshly-parsed package's machine
// name.
func manifestName(manifest json.RawMessage) string {
	var doc struct {
		Name string `json:"name"`
	}
	_ = json.Unmarshal(manifest, &doc)
	return doc.Name
}

// trackTimeout bounds the best-effort install counter bump; see the expert
// service for the rationale (a stalled Redis must not delay the response).
const trackTimeout = 2 * time.Second

// trackInstall bumps the plugin install counter after a successful provision.
// Best-effort and detached from the request context; failures are logged.
func (s *Service) trackInstall(ctx context.Context, pluginID string) {
	if s.metrics == nil {
		return
	}
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), trackTimeout)
	defer cancel()
	if err := s.metrics.TrackInstall(cctx, "plugin", pluginID); err != nil {
		logging.Warn("install_metric_track_failed",
			zap.String("operation", "plugin.install.track"),
			zap.String("resource_id", pluginID),
			logging.ErrorField(err),
		)
	}
}
