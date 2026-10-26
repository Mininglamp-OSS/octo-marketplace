package plugin

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"strings"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

// ErrDependencyChanged reports that a pending review's frozen dependency
// observations no longer match the current latest formal releases (or that the
// graph can no longer be resolved at all). The request is NOT auto-settled: the
// applicant/reviewer must cancel it and submit a new request with fresh
// observations.
var ErrDependencyChanged = errors.New("review dependency observations changed")

// reviewObservations is the set A15 records for one frozen review graph.
type reviewObservations struct {
	Dependencies []model.ReviewDependencyObservation
	Connectors   []model.ReviewConnectorObservation
}

// resolveReviewObservations walks a FROZEN relation graph and records the
// current latest formal release of every followable dependency:
//
//   - Skill/Expert targets resolve to plugins.published_version_id (deduped by
//     plugin_id), and a Team additionally expands each member Expert's published
//     release to record that Expert's own Skill/Connector targets.
//   - Connector targets resolve to their current plugin_connector_effective row.
//   - A Connector reviewing itself has no relation targets; its observation is
//     its own effective baseline, which is null/null only for a first submission
//     that has not produced an effective row yet.
//
// This deliberately reads the same canonical pointers the latest-graph resolver
// and the reference validator read (published_version_id + connector effective);
// it does not introduce a competing "latest" definition. Availability/audience
// are NOT checked here — the existing publish/reference validation owns those
// errors — so an unavailable target cannot be mistaken for a version change.
func resolveReviewObservations(ctx context.Context, tx *sql.Tx, root model.Plugin, rels []model.PluginRelation) (reviewObservations, error) {
	out := reviewObservations{
		Dependencies: []model.ReviewDependencyObservation{},
		Connectors:   []model.ReviewConnectorObservation{},
	}
	if root.Type == model.PluginTypeConnector {
		revision, digest, err := readConnectorEffectiveIdentity(ctx, tx, root.ID)
		if errors.Is(err, sql.ErrNoRows) {
			out.Connectors = append(out.Connectors, model.ReviewConnectorObservation{PluginID: root.ID})
			return out, nil
		}
		if err != nil {
			return reviewObservations{}, err
		}
		out.Connectors = append(out.Connectors, model.ReviewConnectorObservation{
			PluginID:          root.ID,
			EffectiveRevision: &revision,
			EffectiveDigest:   &digest,
		})
		return out, nil
	}

	deps := map[string]model.ReviewDependencyObservation{}
	connectors := map[string]model.ReviewConnectorObservation{}
	nodes := map[string]struct{}{}
	expanded := map[string]struct{}{}
	edges := 0

	var walk func([]model.PluginRelation, int) error
	walk = func(edgesIn []model.PluginRelation, depth int) error {
		if depth > 2 {
			return ErrInvalidRelation
		}
		for _, edge := range edgesIn {
			edges++
			if edges > maxGraphEdges {
				return ErrGraphTooLarge
			}
			id := strings.TrimSpace(edge.TargetPluginID)
			if id == "" || id == root.ID {
				return ErrInvalidRelation
			}
			if _, ok := nodes[id]; !ok {
				if len(nodes) >= maxGraphNodes {
					return ErrGraphTooLarge
				}
				nodes[id] = struct{}{}
			}
			if _, done := deps[id]; done {
				continue
			}
			if _, done := connectors[id]; done {
				continue
			}
			target, err := loadObservationTarget(ctx, tx, id)
			if err != nil {
				return err
			}
			if target.Type == model.PluginTypeConnector {
				revision, digest, err := readConnectorEffectiveIdentity(ctx, tx, target.ID)
				if errors.Is(err, sql.ErrNoRows) {
					return ErrDependencyUnavailable
				}
				if err != nil {
					return err
				}
				connectors[id] = model.ReviewConnectorObservation{
					PluginID:          id,
					EffectiveRevision: &revision,
					EffectiveDigest:   &digest,
				}
				continue
			}
			if !target.PublishedVersionID.Valid || target.PublishedVersionID.String == "" {
				return ErrDependencyUnavailable
			}
			deps[id] = model.ReviewDependencyObservation{
				PluginID:   id,
				PluginType: target.Type,
				VersionID:  target.PublishedVersionID.String,
			}
			if target.Type != model.PluginTypeExpert || depth >= 2 {
				continue
			}
			key := id + "\x00" + target.PublishedVersionID.String
			if _, ok := expanded[key]; ok {
				continue
			}
			expanded[key] = struct{}{}
			children, err := loadPublishedReleaseRelations(ctx, tx, id, target.PublishedVersionID.String)
			if err != nil {
				return err
			}
			if err := walk(children, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(rels, 0); err != nil {
		return reviewObservations{}, err
	}
	for _, obs := range deps {
		out.Dependencies = append(out.Dependencies, obs)
	}
	for _, obs := range connectors {
		out.Connectors = append(out.Connectors, obs)
	}
	sort.Slice(out.Dependencies, func(i, j int) bool {
		return out.Dependencies[i].PluginID < out.Dependencies[j].PluginID
	})
	sort.Slice(out.Connectors, func(i, j int) bool {
		return out.Connectors[i].PluginID < out.Connectors[j].PluginID
	})
	return out, nil
}

type observationTarget struct {
	ID                 string
	Type               model.PluginType
	PublishedVersionID sql.NullString
}

// loadObservationTarget reads the canonical published pointer of a frozen
// graph node. The row lock keeps the observation consistent with the submit or
// approve transaction that is about to persist/compare it.
func loadObservationTarget(ctx context.Context, tx *sql.Tx, id string) (observationTarget, error) {
	var target observationTarget
	var typ string
	err := tx.QueryRowContext(ctx,
		`SELECT plugin_id,plugin_type,published_version_id FROM plugins
		  WHERE plugin_id=? AND status=1 AND deleted_at IS NULL FOR UPDATE`, id).
		Scan(&target.ID, &typ, &target.PublishedVersionID)
	if errors.Is(err, sql.ErrNoRows) {
		return observationTarget{}, ErrDependencyUnavailable
	}
	if err != nil {
		return observationTarget{}, err
	}
	target.Type = model.PluginType(typ)
	return target, nil
}

// loadPublishedReleaseRelations reads the release a member Expert's published
// pointer selects, using the same versions+releases join as ResolveLatestGraph.
func loadPublishedReleaseRelations(ctx context.Context, tx *sql.Tx, pluginID, versionID string) ([]model.PluginRelation, error) {
	var raw []byte
	err := tx.QueryRowContext(ctx,
		`SELECT v.relations_json FROM plugin_versions v
		   JOIN plugin_version_releases rel
		     ON rel.plugin_id=v.plugin_id AND rel.version_id=v.version_id
		  WHERE v.plugin_id=? AND v.version_id=? FOR UPDATE`, pluginID, versionID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrDependencyUnavailable
	}
	if err != nil {
		return nil, err
	}
	rels, err := model.DecodeSnapshotRelations(raw)
	if err != nil {
		return nil, ErrInvalidRelation
	}
	return rels, nil
}

func readConnectorEffectiveIdentity(ctx context.Context, tx *sql.Tx, pluginID string) (uint64, string, error) {
	var revision uint64
	var digest string
	err := tx.QueryRowContext(ctx,
		`SELECT effective_revision,effective_digest FROM plugin_connector_effective
		  WHERE plugin_id=? FOR UPDATE`, pluginID).Scan(&revision, &digest)
	return revision, digest, err
}

// observationResolutionError classifies a failed resolution during approval or
// the auto-review refresh. A dependency that is gone/unpublished or a graph that
// can no longer be decoded is the A15 conflict; a transient database error is
// NOT a conflict and must keep flowing as an internal/retryable error instead of
// telling the applicant to resubmit.
func observationResolutionError(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, ErrDependencyUnavailable),
		errors.Is(err, ErrInvalidRelation),
		errors.Is(err, ErrGraphTooLarge):
		return ErrDependencyChanged
	default:
		return err
	}
}

// encodeReviewObservations serializes both observation arrays. An empty slice
// marshals as [] — "resolved to no dependencies" must stay distinguishable from
// the NULL columns of a pre-A15 request.
func encodeReviewObservations(obs reviewObservations) (json.RawMessage, json.RawMessage, error) {
	deps, err := json.Marshal(obs.Dependencies)
	if err != nil {
		return nil, nil, err
	}
	conns, err := json.Marshal(obs.Connectors)
	if err != nil {
		return nil, nil, err
	}
	return deps, conns, nil
}

// decodeStoredReviewObservations returns the persisted set and whether the
// request carries observations at all. A corrupt column is an integrity error,
// never "no dependencies": silently treating it as empty would approve a graph
// whose frozen evidence cannot be verified.
func decodeStoredReviewObservations(dependencyRaw, connectorRaw []byte) (reviewObservations, bool, error) {
	if dependencyRaw == nil && connectorRaw == nil {
		return reviewObservations{}, false, nil
	}
	out := reviewObservations{
		Dependencies: []model.ReviewDependencyObservation{},
		Connectors:   []model.ReviewConnectorObservation{},
	}
	if dependencyRaw != nil {
		if err := json.Unmarshal(dependencyRaw, &out.Dependencies); err != nil {
			return reviewObservations{}, false, wrapped("decode resolved dependency observations", err)
		}
		if out.Dependencies == nil {
			out.Dependencies = []model.ReviewDependencyObservation{}
		}
	}
	if connectorRaw != nil {
		if err := json.Unmarshal(connectorRaw, &out.Connectors); err != nil {
			return reviewObservations{}, false, wrapped("decode connector observations", err)
		}
		if out.Connectors == nil {
			out.Connectors = []model.ReviewConnectorObservation{}
		}
	}
	return out, true, nil
}

func reviewObservationsEqual(a, b reviewObservations) bool {
	if len(a.Dependencies) != len(b.Dependencies) || len(a.Connectors) != len(b.Connectors) {
		return false
	}
	for i := range a.Dependencies {
		if a.Dependencies[i].PluginID != b.Dependencies[i].PluginID ||
			a.Dependencies[i].PluginType != b.Dependencies[i].PluginType ||
			a.Dependencies[i].VersionID != b.Dependencies[i].VersionID {
			return false
		}
	}
	for i := range a.Connectors {
		if a.Connectors[i].PluginID != b.Connectors[i].PluginID ||
			!equalOptionalString(a.Connectors[i].EffectiveDigest, b.Connectors[i].EffectiveDigest) ||
			!equalOptionalUint64(a.Connectors[i].EffectiveRevision, b.Connectors[i].EffectiveRevision) {
			return false
		}
	}
	return true
}

func equalOptionalString(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func equalOptionalUint64(a, b *uint64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// observationsEnabled reports whether this deployment has the v2 latest-release
// delivery the observations describe. Both switches are required: references
// alone (without managed versions) or managed versions alone (rollout mode) do
// not define the published-pointer/effective semantics A15 observes, and
// writing observations for them would break legacy graphs that predate it.
func (r *Repo) observationsEnabled() bool { return r.versionManagement && r.references }

// RefreshReviewObservations re-resolves a pending request's frozen graph and
// rewrites its observation columns. It is the single internal retry step the
// automatic-review path may perform once between submit and approve; it never
// changes the frozen content, status, or reviewer fields.
func (r *Repo) RefreshReviewObservations(ctx context.Context, scope Scope, reviewID string) (err error) {
	defer func() { err = classifyDeadlock(err) }()
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var pluginID string
	var relationBytes []byte
	err = tx.QueryRowContext(ctx,
		`SELECT plugin_id,relations_json FROM plugin_review_requests
		  WHERE review_id=? AND status='pending' AND deleted_at IS NULL AND space_id=?
		  FOR UPDATE`, reviewID, scope.SpaceID).Scan(&pluginID, &relationBytes)
	if errors.Is(err, sql.ErrNoRows) {
		return classifyMissingPending(ctx, tx, reviewID, scope.SpaceID)
	}
	if err != nil {
		return err
	}
	current, err := getReviewedPluginForUpdate(ctx, tx, scope.SpaceID, pluginID)
	if err != nil {
		return err
	}
	rels, err := model.DecodeSnapshotRelations(relationBytes)
	if err != nil {
		return wrapped("decode frozen relations", err)
	}
	obs, err := resolveReviewObservations(ctx, tx, *current, rels)
	if err != nil {
		return observationResolutionError(err)
	}
	deps, conns, err := encodeReviewObservations(obs)
	if err != nil {
		return err
	}
	now := r.now()
	res, err := tx.ExecContext(ctx,
		`UPDATE plugin_review_requests
		    SET resolved_dependency_observations_json=?,connector_observations_json=?,updated_at=?
		  WHERE review_id=? AND status='pending'`, string(deps), string(conns), now, reviewID)
	if err != nil {
		return err
	}
	if err := mustAffect(res); err != nil {
		return ErrConflict
	}
	return tx.Commit()
}
