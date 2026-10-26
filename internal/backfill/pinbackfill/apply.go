package pinbackfill

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/id"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/migrationauth"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

// ApplyOptions carries a frozen plan and the deployment-verified authorization
// artifact. The private key never enters this process; TrustConfig is loaded
// from deployment-managed configuration by the CLI.
type ApplyOptions struct {
	Plan          Plan
	PlanDigest    string
	Trust         *migrationauth.TrustConfig
	Authorization migrationauth.Envelope
	Now           time.Time
}

// Result reports exactly what the run did. Counts come from the committed audit
// row, not from optimistic in-memory intentions.
type Result struct {
	Status     string          `json:"status"`
	PlanDigest string          `json:"plan_digest"`
	BatchID    string          `json:"batch_id"`
	ActorID    string          `json:"actor_id"`
	Sources    []SourceResult  `json:"sources"`
	Blocked    []BlockedSource `json:"blocked"`
}

// SourceResult is the per-source-version audit summary.
type SourceResult struct {
	PluginID      string `json:"plugin_id"`
	VersionID     string `json:"version_id"`
	Source        string `json:"source"`
	Status        string `json:"status"`
	RunID         string `json:"run_id,omitempty"`
	RelationCount int    `json:"relation_count"`
	PinnedCount   int    `json:"pinned_count"`
	InsertedRefs  int    `json:"inserted_refs"`
}

// Apply verifies the offline authorization, then runs one transaction per source
// version. A source that already has an audit row for this plan digest is a
// no-op, which makes an interrupted multi-source run resumable by replaying the
// same plan. A stale source or target refuses that source before any UPDATE.
func Apply(ctx context.Context, db *sql.DB, opts ApplyOptions) (Result, error) {
	if opts.Now.IsZero() {
		opts.Now = time.Now().UTC()
	}
	result := Result{
		Status:     "noop",
		PlanDigest: opts.PlanDigest,
		BatchID:    opts.Plan.BatchID,
		Sources:    []SourceResult{},
		Blocked:    opts.Plan.Blocked,
	}
	if opts.Plan.Blocked == nil {
		result.Blocked = []BlockedSource{}
	}
	if opts.Trust == nil {
		return result, ErrAuthorization
	}
	if err := validatePlan(opts.Plan); err != nil {
		return result, err
	}
	computed, err := Digest(opts.Plan)
	if err != nil || computed != opts.PlanDigest ||
		(opts.Plan.PlanDigest != "" && opts.Plan.PlanDigest != computed) {
		return result, fmt.Errorf("%w: plan digest mismatch", ErrPlanStale)
	}
	payload, err := opts.Trust.Verify(opts.Authorization, migrationauth.Expected{
		SourceScope:    opts.Plan.SourceScope,
		SourceIdentity: opts.Plan.SourceIdentity,
		PlanDigest:     opts.PlanDigest,
		BatchID:        opts.Plan.BatchID,
	}, opts.Now.UTC())
	if err != nil {
		return result, fmt.Errorf("%w: %v", ErrAuthorization, err)
	}
	result.ActorID = payload.ActorID
	for _, source := range opts.Plan.Sources {
		sourceResult, err := applySource(ctx, db, opts.Plan, source, opts.PlanDigest, payload.ActorID, opts.Now.UTC())
		result.Sources = append(result.Sources, sourceResult)
		if err != nil {
			return result, err
		}
		if sourceResult.Status == "applied" {
			result.Status = "applied"
		}
	}
	return result, nil
}

func validatePlan(plan Plan) error {
	if plan.SchemaVersion != PlanSchemaVersion {
		return ErrInvalidPlan
	}
	// The plan digest and the signed Expected{...} bind these fields. Preview
	// always emits the fixed cutover constants above, but apply accepts a
	// deployment that deliberately signs a different scope/identity/batch as
	// long as the trust issuer allows it and the digest matches.
	if !lowerHex64(plan.SourceScope) || strings.TrimSpace(plan.SourceIdentity) == "" ||
		strings.TrimSpace(plan.BatchID) == "" || strings.ContainsAny(plan.SourceIdentity, "\x00\r\n") {
		return ErrInvalidPlan
	}
	seen := map[string]bool{}
	for _, source := range plan.Sources {
		if source.PluginID == "" || source.VersionID == "" {
			return ErrInvalidPlan
		}
		if source.Source != "" && source.Source != source.PluginID+"@"+source.VersionID {
			return ErrInvalidPlan
		}
		if source.PluginType != model.PluginTypeExpert && source.PluginType != model.PluginTypeExpertTeam {
			return ErrInvalidPlan
		}
		if source.SnapshotDigest == "" || source.ExpectedSnapshotDigest == "" {
			return ErrInvalidPlan
		}
		if len(source.Relations) == 0 {
			return ErrInvalidPlan
		}
		key := source.PluginID + "\x00" + source.VersionID
		if seen[key] {
			return ErrInvalidPlan
		}
		seen[key] = true
		relations := map[string]bool{}
		for _, change := range source.Relations {
			if change.RelationID == "" || change.TargetPluginID == "" ||
				strings.TrimSpace(change.TargetVersionID) == "" {
				return ErrInvalidPlan
			}
			if change.RelationType != "expert_skill" && change.RelationType != "expert_team_expert" {
				return ErrInvalidPlan
			}
			switch change.Resolution {
			case ResolutionCurrentRelease, ResolutionSnapshotPin, ResolutionReferencePin:
			default:
				return ErrInvalidPlan
			}
			if relations[change.RelationID] {
				return ErrInvalidPlan
			}
			relations[change.RelationID] = true
		}
	}
	return nil
}

func applySource(ctx context.Context, db *sql.DB, plan Plan, source SourcePlan, planDigest, actor string, now time.Time) (SourceResult, error) {
	identity := source.Source
	if identity == "" {
		identity = source.PluginID + "@" + source.VersionID
	}
	result := SourceResult{
		PluginID:    source.PluginID,
		VersionID:   source.VersionID,
		Source:      identity,
		Status:      "noop",
		PinnedCount: len(source.Relations),
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return result, fmt.Errorf("%w: begin source transaction", ErrWrite)
	}
	defer tx.Rollback()

	var existingRun, existingStatus string
	var existingRelations, existingPins, existingRefs int
	err = tx.QueryRowContext(ctx, `SELECT run_id,relation_count,pinned_count,inserted_refs,status
		FROM plugin_pin_backfill_runs WHERE plan_digest=? AND source_version_id=? FOR UPDATE`,
		planDigest, source.VersionID).Scan(&existingRun, &existingRelations, &existingPins, &existingRefs, &existingStatus)
	if err == nil {
		// The committed audit row is the idempotency key. Replaying the same signed
		// plan must not move a pin, rewrite JSON, or append another run row.
		result.RunID = existingRun
		result.RelationCount = existingRelations
		result.PinnedCount = existingPins
		result.InsertedRefs = existingRefs
		return result, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return result, fmt.Errorf("%w: read audit state", ErrWrite)
	}

	locked, err := lockSource(ctx, tx, source)
	if err != nil {
		return result, err
	}
	sourcePlugin := locked.Plugin

	var raw []byte
	if err = tx.QueryRowContext(ctx, `SELECT relations_json FROM plugin_versions WHERE plugin_id=? AND version_id=? FOR UPDATE`,
		source.PluginID, source.VersionID).Scan(&raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return result, stale(source, "source version disappeared")
		}
		return result, fmt.Errorf("%w: lock source snapshot", ErrWrite)
	}
	var releaseLabel string
	if err = tx.QueryRowContext(ctx, `SELECT version_label FROM plugin_version_releases WHERE plugin_id=? AND version_id=? FOR UPDATE`,
		source.PluginID, source.VersionID).Scan(&releaseLabel); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return result, stale(source, "source version is no longer a release")
		}
		return result, fmt.Errorf("%w: lock source release", ErrWrite)
	}
	currentDigest, err := snapshotDigest(raw)
	if err != nil {
		return result, stale(source, "source snapshot is not canonical JSON")
	}
	if currentDigest != source.SnapshotDigest && currentDigest != source.ExpectedSnapshotDigest {
		return result, stale(source, "source snapshot changed")
	}
	rels, err := model.DecodeSnapshotRelations(raw)
	if err != nil {
		return result, stale(source, "source snapshot is invalid")
	}
	refs, err := lockReferences(ctx, tx, source)
	if err != nil {
		return result, err
	}

	snapshotPins, refPins, refsToInsert, planned, err := planSourceWrites(source, rels, refs)
	if err != nil {
		return result, err
	}
	if err = validateTargets(ctx, tx, sourcePlugin, rels, planned, snapshotPins, refPins, refsToInsert); err != nil {
		return result, err
	}

	changed := false
	insertedRefs := 0
	if len(snapshotPins) > 0 {
		updatedRaw, err := encodeSnapshotWithPins(raw, rels, snapshotPins)
		if err != nil {
			return result, stale(source, "source snapshot cannot be rewritten")
		}
		res, err := tx.ExecContext(ctx, `UPDATE plugin_versions SET relations_json=? WHERE plugin_id=? AND version_id=?`,
			string(updatedRaw), source.PluginID, source.VersionID)
		if err != nil {
			return result, fmt.Errorf("%w: update source snapshot", ErrWrite)
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return result, stale(source, "source snapshot update affected no row")
		}
		raw = updatedRaw
		changed = true
	}
	for _, relationID := range sortedKeys(refPins) {
		res, err := tx.ExecContext(ctx, `UPDATE plugin_version_relation_refs SET target_version_id=?
			WHERE source_plugin_id=? AND source_version_id=? AND relation_id=?`,
			refPins[relationID], source.PluginID, source.VersionID, relationID)
		if err != nil {
			return result, fmt.Errorf("%w: update reference pin", ErrWrite)
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return result, stale(source, "reference pin update affected no row")
		}
		changed = true
	}
	for _, relationID := range sortedKeys(refsToInsert) {
		rel := relationByID(rels, relationID)
		if rel == nil {
			return result, stale(source, "reference row has no snapshot edge")
		}
		var pin any
		if rel.Type != "expert_connector" {
			pin = refsToInsert[relationID]
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO plugin_version_relation_refs
			(source_plugin_id,source_version_id,relation_id,relation_type,target_plugin_id,target_version_id)
			VALUES (?,?,?,?,?,?)`, source.PluginID, source.VersionID, rel.ID, rel.Type, rel.TargetPluginID, pin)
		if err != nil {
			return result, fmt.Errorf("%w: insert reference pin", ErrWrite)
		}
		insertedRefs++
		changed = true
	}

	indexChanged, err := refreshReferenceIndex(ctx, tx, source, len(rels), now)
	if err != nil {
		return result, err
	}
	changed = changed || indexChanged

	status := "noop"
	if changed {
		status = "applied"
	}
	runID := id.New()
	if _, err = tx.ExecContext(ctx, `INSERT INTO plugin_pin_backfill_runs
		(run_id,plan_digest,source_scope,source_identity,batch_id,actor_id,source_plugin_id,source_version_id,
		 relation_count,pinned_count,inserted_refs,status,created_at,applied_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		runID, planDigest, plan.SourceScope, identity, plan.BatchID, actor,
		source.PluginID, source.VersionID, len(rels), len(source.Relations), insertedRefs, status, now, now); err != nil {
		return result, fmt.Errorf("%w: append audit row", ErrWrite)
	}
	if err = tx.Commit(); err != nil {
		return result, fmt.Errorf("%w: commit source transaction", ErrWrite)
	}
	result.Status = status
	result.RunID = runID
	result.RelationCount = len(rels)
	result.InsertedRefs = insertedRefs
	return result, nil
}

type lockedSourceState struct {
	Plugin model.Plugin
}

func lockSource(ctx context.Context, tx *sql.Tx, source SourcePlan) (lockedSourceState, error) {
	var (
		state     lockedSourceState
		sid       sql.NullString
		published sql.NullString
	)
	err := tx.QueryRowContext(ctx, `SELECT plugin_type,is_embedded,owner_uid,space_id,visibility,listing_state,
		published_version_id,edit_revision
		FROM plugins WHERE plugin_id=? AND status=1 AND deleted_at IS NULL FOR UPDATE`,
		source.PluginID).Scan(&state.Plugin.Type, &state.Plugin.IsEmbedded, &state.Plugin.OwnerUID, &sid,
		&state.Plugin.Visibility, &state.Plugin.ListingState, &published, &state.Plugin.EditRevision)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return state, stale(source, "source plugin disappeared")
		}
		return state, fmt.Errorf("%w: lock source plugin", ErrWrite)
	}
	state.Plugin.ID = source.PluginID
	state.Plugin.SpaceID = nullStringPtr(sid)
	state.Plugin.PublishedVersionID = nullStringPtr(published)
	p := state.Plugin
	if p.Type != source.PluginType || p.IsEmbedded || p.OwnerUID != source.OwnerUID ||
		p.ListingState != model.PluginListingStatePublished || p.Visibility != source.Visibility ||
		!equalStringPtr(p.SpaceID, source.SpaceID) || !equalStringPtr(p.PublishedVersionID, source.PublishedVersionID) ||
		p.EditRevision != source.EditRevision {
		return state, stale(source, "source plugin state changed")
	}
	return state, nil
}

func lockReferences(ctx context.Context, tx *sql.Tx, source SourcePlan) (map[string]referenceRow, error) {
	rows, err := tx.QueryContext(ctx, `SELECT source_plugin_id,relation_id,relation_type,target_plugin_id,target_version_id
		FROM plugin_version_relation_refs WHERE source_version_id=? ORDER BY relation_id FOR UPDATE`, source.VersionID)
	if err != nil {
		return nil, fmt.Errorf("%w: lock reference index", ErrWrite)
	}
	defer rows.Close()
	out := map[string]referenceRow{}
	for rows.Next() {
		var (
			ref referenceRow
			pin sql.NullString
		)
		if err := rows.Scan(&ref.SourcePluginID, &ref.RelationID, &ref.RelationType, &ref.TargetPluginID, &pin); err != nil {
			return nil, fmt.Errorf("%w: read reference index", ErrWrite)
		}
		ref.TargetVersionID = nullStringPtr(pin)
		out[ref.RelationID] = ref
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%w: read reference index", ErrWrite)
	}
	return out, nil
}

func planSourceWrites(source SourcePlan, rels []model.PluginRelation, refs map[string]referenceRow) (map[string]string, map[string]string, map[string]string, map[string]RelationChange, error) {
	relsByID := map[string]*model.PluginRelation{}
	for i := range rels {
		rel := &rels[i]
		if rel.ID == "" || rel.TargetPluginID == "" || rel.Status != 1 || rel.DeletedAt != nil {
			return nil, nil, nil, nil, stale(source, "source snapshot edge is invalid")
		}
		if _, exists := relsByID[rel.ID]; exists {
			return nil, nil, nil, nil, stale(source, "source snapshot has duplicate relation IDs")
		}
		relsByID[rel.ID] = rel
	}
	planned := map[string]RelationChange{}
	for _, change := range source.Relations {
		rel, ok := relsByID[change.RelationID]
		if !ok || rel.Type != change.RelationType || rel.TargetPluginID != change.TargetPluginID {
			return nil, nil, nil, nil, stale(source, "planned relation is absent or changed")
		}
		if rel.Type == "expert_connector" {
			return nil, nil, nil, nil, stale(source, "connector edge cannot be pinned")
		}
		planned[change.RelationID] = change
	}
	for relationID, ref := range refs {
		rel, ok := relsByID[relationID]
		if !ok || ref.SourcePluginID != source.PluginID || ref.RelationType != rel.Type || ref.TargetPluginID != rel.TargetPluginID {
			return nil, nil, nil, nil, stale(source, "reference index drifted from the source snapshot")
		}
	}
	snapshotPins := map[string]string{}
	refPins := map[string]string{}
	refsToInsert := map[string]string{}
	for i := range rels {
		rel := &rels[i]
		ref, hasRef := refs[rel.ID]
		snapshotPin := trimPin(rel.TargetVersionID)
		referencePin := ""
		if hasRef {
			referencePin = trimPin(ref.TargetVersionID)
		}
		change, isPlanned := planned[rel.ID]
		switch rel.Type {
		case "expert_connector":
			// A connector version is corruption, never a hint. This tool does not
			// rewrite connector pins; an existing bad pin is refused.
			if rel.TargetVersionID != nil || referencePin != "" {
				return nil, nil, nil, nil, stale(source, "connector edge carries a version")
			}
			if !hasRef {
				refsToInsert[rel.ID] = ""
			}
			continue
		case "expert_skill", "expert_team_expert":
		default:
			return nil, nil, nil, nil, stale(source, "source snapshot edge type is invalid")
		}
		if isPlanned {
			wanted := strings.TrimSpace(change.TargetVersionID)
			if snapshotPin != "" && snapshotPin != wanted {
				return nil, nil, nil, nil, stale(source, "an existing pin would be moved")
			}
			if referencePin != "" && referencePin != wanted {
				return nil, nil, nil, nil, stale(source, "an existing reference pin would be moved")
			}
			if snapshotPin == "" {
				snapshotPins[rel.ID] = wanted
			}
			if !hasRef {
				refsToInsert[rel.ID] = wanted
			} else if referencePin == "" {
				refPins[rel.ID] = wanted
			}
			continue
		}
		// Unplanned edges may only receive a missing reverse-index row from a pin
		// that already exists in the snapshot. Their pin value is never changed.
		if snapshotPin == "" {
			return nil, nil, nil, nil, stale(source, "a missing pin is not covered by the plan")
		}
		if !hasRef {
			refsToInsert[rel.ID] = snapshotPin
		} else if referencePin == "" {
			refPins[rel.ID] = snapshotPin
		} else if referencePin != snapshotPin {
			return nil, nil, nil, nil, stale(source, "reference and snapshot pins disagree")
		}
	}
	return snapshotPins, refPins, refsToInsert, planned, nil
}

type lockedTargetState struct {
	Plugin    model.Plugin
	Published *string
}

func validateTargets(ctx context.Context, tx *sql.Tx, source model.Plugin, rels []model.PluginRelation, planned map[string]RelationChange, snapshotPins, refPins, refsToInsert map[string]string) error {
	wanted := map[string]string{}
	for id, pin := range snapshotPins {
		wanted[id] = pin
	}
	for id, pin := range refPins {
		wanted[id] = pin
	}
	for id, pin := range refsToInsert {
		if pin != "" {
			wanted[id] = pin
		}
	}
	targets := map[string]*lockedTargetState{}
	for _, relationID := range sortedKeys(wanted) {
		rel := relationByID(rels, relationID)
		if rel == nil {
			return ErrInvalidPlan
		}
		requireCurrent := false
		if change, ok := planned[relationID]; ok && change.Resolution == ResolutionCurrentRelease &&
			trimPin(rel.TargetVersionID) == "" {
			requireCurrent = true
		}
		targetID := rel.TargetPluginID
		if requireCurrent {
			target, ok := targets[targetID]
			if !ok {
				var err error
				target, err = lockTarget(ctx, tx, targetID)
				if err != nil {
					return err
				}
				targets[targetID] = target
			}
			if target == nil {
				return fmt.Errorf("%w: target unavailable", ErrPlanStale)
			}
			if target.Plugin.IsEmbedded || target.Plugin.ListingState != model.PluginListingStatePublished ||
				!audienceAllows(source, target.Plugin) || !validRelationKind(source.Type, rel.Type, target.Plugin.Type) {
				return fmt.Errorf("%w: target is no longer an eligible dependency", ErrPlanStale)
			}
			if target.Published == nil || strings.TrimSpace(*target.Published) != wanted[relationID] {
				return fmt.Errorf("%w: target head changed after preview", ErrPlanStale)
			}
		}
		var found string
		err := tx.QueryRowContext(ctx, `SELECT version_id FROM plugin_version_releases WHERE plugin_id=? AND version_id=? FOR UPDATE`,
			targetID, wanted[relationID]).Scan(&found)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("%w: target release disappeared", ErrPlanStale)
			}
			return fmt.Errorf("%w: lock target release", ErrWrite)
		}
	}
	return nil
}

func lockTarget(ctx context.Context, tx *sql.Tx, targetID string) (*lockedTargetState, error) {
	var (
		target    lockedTargetState
		sid       sql.NullString
		published sql.NullString
	)
	err := tx.QueryRowContext(ctx, `SELECT plugin_id,plugin_type,is_embedded,owner_uid,space_id,visibility,listing_state,published_version_id
		FROM plugins WHERE plugin_id=? AND status=1 AND deleted_at IS NULL FOR UPDATE`, targetID).Scan(
		&target.Plugin.ID, &target.Plugin.Type, &target.Plugin.IsEmbedded, &target.Plugin.OwnerUID, &sid,
		&target.Plugin.Visibility, &target.Plugin.ListingState, &published)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("%w: lock target plugin", ErrWrite)
	}
	target.Plugin.SpaceID = nullStringPtr(sid)
	target.Published = nullStringPtr(published)
	return &target, nil
}

func refreshReferenceIndex(ctx context.Context, tx *sql.Tx, source SourcePlan, relationCount int, now time.Time) (bool, error) {
	// MySQL normalizes JSON on storage. Read back the committed representation
	// before hashing, exactly like writeReferenceIndex, so the coverage check
	// SHA2(CAST(relations_json AS CHAR)) agrees with this marker.
	var stored []byte
	if err := tx.QueryRowContext(ctx, `SELECT relations_json FROM plugin_versions WHERE plugin_id=? AND version_id=?`,
		source.PluginID, source.VersionID).Scan(&stored); err != nil {
		return false, fmt.Errorf("%w: read stored source snapshot", ErrWrite)
	}
	digest := indexDigest(stored)
	var (
		owner   string
		count   int
		current string
	)
	err := tx.QueryRowContext(ctx, `SELECT source_plugin_id,relation_count,snapshot_digest
		FROM plugin_version_reference_index_state WHERE source_version_id=? FOR UPDATE`, source.VersionID).Scan(&owner, &count, &current)
	if err == nil && owner == source.PluginID && count == relationCount && current == digest {
		return false, nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, fmt.Errorf("%w: lock reference index state", ErrWrite)
	}
	if err == nil && owner != source.PluginID {
		return false, stale(source, "reference index state belongs to another source")
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO plugin_version_reference_index_state
		(source_plugin_id,source_version_id,relation_count,snapshot_digest,verified_at)
		VALUES (?,?,?,?,?)
		ON DUPLICATE KEY UPDATE relation_count=?,snapshot_digest=?,verified_at=?`,
		source.PluginID, source.VersionID, relationCount, digest, now, relationCount, digest, now)
	if err != nil {
		return false, fmt.Errorf("%w: refresh reference index state", ErrWrite)
	}
	return true, nil
}

func relationByID(rels []model.PluginRelation, relationID string) *model.PluginRelation {
	for i := range rels {
		if rels[i].ID == relationID {
			return &rels[i]
		}
	}
	return nil
}

func stale(source SourcePlan, reason string) error {
	return fmt.Errorf("%w: %s: %s", ErrPlanStale, source.PluginID+"@"+source.VersionID, reason)
}

func lowerHex64(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, r := range value {
		if !(r >= '0' && r <= '9') && !(r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

func equalStringPtr(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func sortedKeys[V any](in map[string]V) []string {
	keys := make([]string, 0, len(in))
	for key := range in {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
