package stockmigration

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	pluginrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/plugin"
)

// ExecuteOptions carries only the verified operator identity. It never carries
// a public key, an approval claim or a plan override.
type ExecuteOptions struct {
	ActorID   string
	ActorName string
	Now       time.Time
}

// ExecuteRun executes the persisted frozen plan. It is idempotent per map row:
// published/source_switched nodes are skipped, created nodes resume at publish,
// and waiting_review nodes are re-checked against the original review.
//
// This slice supports private and Space versioned resources and private/system
// Connectors. Space Connectors and system versioned resources are blocked with a
// safe reason rather than bypassing review/effective ownership.
func ExecuteRun(ctx context.Context, db *sql.DB, run *MigrationRun, d Dataset, opts ExecuteOptions) (RunStatus, string, error) {
	if opts.Now.IsZero() {
		opts.Now = time.Now().UTC()
	}
	if opts.ActorID == "" {
		return RunBlocked, "authorization_invalid", ErrRunAuthorization
	}
	if run.Status == RunCompleted {
		return RunCompleted, "", nil
	}
	d.Labels = planLabels(run.Plan)
	g, unit, err := expectedForRoot(d, run.SourcePluginID)
	if err != nil {
		return blockRun(ctx, db, run, "plan_stale", err)
	}
	byKey := map[string]expectedNode{}
	for _, en := range g.nodes {
		byKey[en.key] = en
	}
	for _, en := range g.reuses {
		byKey[en.key] = en
	}
	planned := map[string]MigrationPlanResource{}
	for _, resource := range run.Plan.Resources {
		planned[resource.SourceNodeKey] = resource
	}
	if len(planned) != len(byKey) {
		return blockRun(ctx, db, run, "plan_stale", ErrPlanStale)
	}
	for key := range byKey {
		if _, ok := planned[key]; !ok {
			return blockRun(ctx, db, run, "plan_stale", ErrPlanStale)
		}
	}
	// Reused targets already exist; record the verified mapping without writing
	// the target. This is the audit trail resume uses to distinguish reuse from
	// create.
	for _, en := range g.reuses {
		resource := planned[en.key]
		node := newRunNode(run, resource, en, nil, nil, StepPublished)
		if err = upsertRunNode(ctx, db, node); err != nil {
			return blockRun(ctx, db, run, "run_conflict", err)
		}
	}
	if err = SetRunStatus(ctx, db, run.RunID, RunBlocked, "", opts.Now); err != nil {
		return RunBlocked, "run_conflict", err
	}
	for _, n := range unit.Nodes {
		key := executionNodeKey(n, run.SourcePluginID)
		en, ok := byKey[key]
		if !ok {
			return blockRun(ctx, db, run, "plan_stale", ErrPlanStale)
		}
		resource := planned[key]
		mapNode, err := FindNode(ctx, db, run.RunID, key)
		if err != nil {
			return blockRun(ctx, db, run, "run_conflict", err)
		}
		if mapNode != nil {
			switch mapNode.StepState {
			case StepPublished, StepSourceSwitched:
				continue
			case StepBlocked:
				return RunBlocked, derefOr(mapNode.ErrorReason, "blocked"), ErrRunConflict
			case StepWaitingReview:
				status, version, reviewErr := resumeReview(ctx, db, mapNode)
				if reviewErr != nil {
					return blockNode(ctx, db, run, mapNode, "review_state_invalid", reviewErr)
				}
				switch status {
				case RunWaitingReview:
					if err = SetRunStatus(ctx, db, run.RunID, RunWaitingReview, "", opts.Now); err != nil {
						return RunBlocked, "run_conflict", err
					}
					return RunWaitingReview, "", nil
				case RunBlocked:
					return blockNode(ctx, db, run, mapNode, "review_rejected", ErrRunConflict)
				case RunCompleted:
					if err = markPublished(ctx, db, run, mapNode, resource, en, version, mapNode.ReviewID, opts.Now); err != nil {
						return blockRun(ctx, db, run, "run_conflict", err)
					}
					continue
				}
			case StepCreated:
				// Resume at publish.
			}
		}
		if mapNode == nil || mapNode.StepState == "" {
			versionID, stageErr := stageNode(ctx, db, run, resource, en, opts)
			if stageErr != nil {
				return blockRun(ctx, db, run, "write_failed", stageErr)
			}
			mapNode, err = FindNode(ctx, db, run.RunID, key)
			if err != nil || mapNode == nil {
				return blockRun(ctx, db, run, "run_conflict", ErrRunConflict)
			}
			_ = versionID
		}
		status, version, reviewID, publishErr := publishNode(ctx, db, run, resource, en, mapNode.TargetVersionID, opts)
		if publishErr != nil {
			return blockNode(ctx, db, run, mapNode, "publish_failed", publishErr)
		}
		if status == RunWaitingReview {
			if err = UpsertRunNode(ctx, db, withNodeState(*mapNode, StepWaitingReview, mapNode.TargetVersionID, reviewID, nil, "")); err != nil {
				return blockRun(ctx, db, run, "run_conflict", err)
			}
			if err = SetRunStatus(ctx, db, run.RunID, RunWaitingReview, "", opts.Now); err != nil {
				return RunBlocked, "run_conflict", err
			}
			return RunWaitingReview, "", nil
		}
		if err = markPublished(ctx, db, run, mapNode, resource, en, version, reviewID, opts.Now); err != nil {
			return blockRun(ctx, db, run, "run_conflict", err)
		}
	}
	// Completion requires every planned node to be published/source-switched.
	rows, err := ListNode(ctx, db, run.RunID)
	if err != nil {
		return blockRun(ctx, db, run, "run_conflict", err)
	}
	states := map[string]MapStepState{}
	for _, row := range rows {
		states[row.SourceNodeKey] = row.StepState
	}
	for _, resource := range run.Plan.Resources {
		state := states[resource.SourceNodeKey]
		if state != StepPublished && state != StepSourceSwitched {
			return blockRun(ctx, db, run, "incomplete", ErrRunConflict)
		}
	}
	if states[RootNodeKey] != StepSourceSwitched {
		return blockRun(ctx, db, run, "source_not_switched", ErrRunConflict)
	}
	if err = recordRootAudit(ctx, db, run, d, opts.Now); err != nil {
		return blockRun(ctx, db, run, "audit_failed", err)
	}
	if err = SetRunStatus(ctx, db, run.RunID, RunCompleted, "", opts.Now); err != nil {
		return blockRun(ctx, db, run, "run_conflict", err)
	}
	return RunCompleted, "", nil
}

func stageNode(ctx context.Context, db *sql.DB, run *MigrationRun, resource MigrationPlanResource, en expectedNode, opts ExecuteOptions) (*string, error) {
	n := en.node
	root := resource.Action == PlanActionUpdate
	spaceScope := normalizeVisibility(n.Plugin.Visibility) == model.PluginVisibilitySpace
	withVersion := !spaceScope && n.Type != model.PluginTypeConnector
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, ErrRunConflict
	}
	defer tx.Rollback()
	var versionID *string
	if n.Type == model.PluginTypeConnector {
		if normalizeVisibility(n.Plugin.Visibility) == model.PluginVisibilitySpace {
			return nil, fmt.Errorf("%w: space Connector review is not part of this slice", ErrUnsupportedPlan)
		}
		if err = stageConnector(ctx, tx, run, resource, en, opts.Now); err != nil {
			return nil, err
		}
	} else if normalizeVisibility(n.Plugin.Visibility) == model.PluginVisibilitySystem {
		return nil, fmt.Errorf("%w: system versioned resource direct-publish is not part of this slice", ErrUnsupportedPlan)
	} else if root {
		if err = stageRoot(ctx, tx, run, resource, en, withVersion, opts.Now); err != nil {
			return nil, err
		}
		if withVersion {
			value, err := currentVersion(ctx, tx, n.Plugin.ID)
			if err != nil {
				return nil, err
			}
			versionID = value
		}
	} else {
		if err = stageChild(ctx, tx, run, resource, en, withVersion, opts.Now); err != nil {
			return nil, err
		}
		if withVersion {
			value, err := currentVersion(ctx, tx, n.Plugin.ID)
			if err != nil {
				return nil, err
			}
			versionID = value
		}
	}
	node := newRunNode(run, resource, en, versionID, nil, StepCreated)
	if err = upsertRunNode(ctx, tx, node); err != nil {
		return nil, ErrRunConflict
	}
	if err = tx.Commit(); err != nil {
		return nil, ErrRunConflict
	}
	return versionID, nil
}

func stageRoot(ctx context.Context, tx *sql.Tx, run *MigrationRun, resource MigrationPlanResource, en expectedNode, withVersion bool, now time.Time) error {
	n := en.node
	category := categoryValue(resource.CategoryID)
	var currentVersion any
	var currentLabel any
	if withVersion {
		versionID, err := insertVersion(ctx, tx, n, resource, now)
		if err != nil {
			return err
		}
		currentVersion, currentLabel = versionID, *resource.VersionLabel
	}
	query := `UPDATE plugins SET manifest_json=?,plugin_json=?,attachment_keys_json=NULL,manifest_hash=?,plugin_hash=?,visibility=?,category_id=?,updated_at=? WHERE plugin_id=? AND edit_revision=? AND current_version_id=? AND status=1 AND deleted_at IS NULL`
	args := []any{string(n.Plugin.Manifest), string(n.Plugin.Package), n.Plugin.ManifestHash, n.Plugin.PluginHash, string(n.Plugin.Visibility), category, now, n.Plugin.ID, derefRevision(run.Plan.ExpectedEditRevision), run.SourceVersionID}
	if withVersion {
		query = `UPDATE plugins SET manifest_json=?,plugin_json=?,attachment_keys_json=NULL,manifest_hash=?,plugin_hash=?,visibility=?,category_id=?,updated_at=?,current_version_id=?,current_version=? WHERE plugin_id=? AND edit_revision=? AND current_version_id=? AND status=1 AND deleted_at IS NULL`
		args = []any{string(n.Plugin.Manifest), string(n.Plugin.Package), n.Plugin.ManifestHash, n.Plugin.PluginHash, string(n.Plugin.Visibility), category, now, currentVersion, currentLabel, n.Plugin.ID, derefRevision(run.Plan.ExpectedEditRevision), run.SourceVersionID}
	}
	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return ErrWrite
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return ErrPlanStale
	}
	return replaceRelations(ctx, tx, n, now)
}

func stageChild(ctx context.Context, tx *sql.Tx, run *MigrationRun, resource MigrationPlanResource, en expectedNode, withVersion bool, now time.Time) error {
	n := en.node
	source := n.SourceID
	if n.Type == model.PluginTypeConnector {
		source = strings.TrimSuffix(source, ":mcp")
	}
	category := categoryValue(resource.CategoryID)
	result, err := tx.ExecContext(ctx, `INSERT INTO plugins(plugin_id,plugin_name,plugin_type,is_embedded,category_id,tags_json,publisher,owner_uid,space_id,visibility,listing_state,creator_name,created_by_type,created_by_bot_uid,created_by_bot_name,icon,tool_count,manifest_json,plugin_json,attachment_keys_json,manifest_hash,plugin_hash,status,created_at,updated_at)
 SELECT ?,?,?,0,?,?,publisher,?,?,?,'draft',creator_name,created_by_type,created_by_bot_uid,created_by_bot_name,icon,0,?,?,NULL,?,?,1,?,? FROM plugins WHERE plugin_id=?`,
		n.Plugin.ID, n.Plugin.Name, n.Type, category, string(n.Plugin.Tags), n.Plugin.OwnerUID, n.Plugin.SpaceID, string(n.Plugin.Visibility), string(n.Plugin.Manifest), string(n.Plugin.Package), n.Plugin.ManifestHash, n.Plugin.PluginHash, now, now, source)
	if err != nil {
		return ErrWrite
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return ErrWrite
	}
	if err = replaceRelations(ctx, tx, n, now); err != nil {
		return err
	}
	if withVersion {
		versionID, err := insertVersion(ctx, tx, n, resource, now)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE plugins SET current_version_id=?,current_version=?,updated_at=? WHERE plugin_id=?`, versionID, *resource.VersionLabel, now, n.Plugin.ID); err != nil {
			return ErrWrite
		}
	}
	return nil
}

func stageConnector(ctx context.Context, tx *sql.Tx, run *MigrationRun, resource MigrationPlanResource, en expectedNode, now time.Time) error {
	n := en.node
	result, err := tx.ExecContext(ctx, `INSERT INTO plugins(plugin_id,plugin_name,plugin_type,is_embedded,category_id,tags_json,publisher,owner_uid,space_id,visibility,listing_state,creator_name,created_by_type,created_by_bot_uid,created_by_bot_name,icon,tool_count,manifest_json,plugin_json,attachment_keys_json,manifest_hash,plugin_hash,status,created_at,updated_at)
 SELECT ?,?,?,0,NULL,?,publisher,?,?,?,'draft',creator_name,created_by_type,created_by_bot_uid,created_by_bot_name,icon,0,?,?,NULL,?,?,1,?,? FROM plugins WHERE plugin_id=?`,
		n.Plugin.ID, n.Plugin.Name, n.Type, string(n.Plugin.Tags), n.Plugin.OwnerUID, n.Plugin.SpaceID, string(n.Plugin.Visibility), string(n.Plugin.Manifest), string(n.Plugin.Package), n.Plugin.ManifestHash, n.Plugin.PluginHash, now, now, strings.TrimSuffix(n.SourceID, ":mcp"))
	if err != nil {
		return ErrWrite
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return ErrWrite
	}
	return replaceRelations(ctx, tx, n, now)
}

func insertVersion(ctx context.Context, tx *sql.Tx, n Node, resource MigrationPlanResource, now time.Time) (string, error) {
	var count int
	if tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM plugin_versions WHERE plugin_id=?`, n.Plugin.ID).Scan(&count) != nil {
		return "", ErrRead
	}
	relations, err := model.EncodeSnapshotRelations(n.Relations)
	if err != nil {
		return "", ErrWrite
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO plugin_versions(version_id,plugin_id,version,declared_version,manifest_json,plugin_json,attachment_keys_json,manifest_hash,plugin_hash,relations_json,changelog,created_by,created_at,snapshot_schema_version,artifacts_json) VALUES (?,?,?,?,?,?,NULL,?,?,?,?,?,?,?,?)`,
		n.VersionID, n.Plugin.ID, count+1, *resource.VersionLabel, string(n.Plugin.Manifest), string(n.Plugin.Package), n.Plugin.ManifestHash, n.Plugin.PluginHash, string(relations), "Offline-authorized dependency migration", "offline-migration", now, model.PluginSnapshotSchemaVersion, string(n.Artifacts)); err != nil {
		return "", ErrWrite
	}
	for _, relation := range n.Relations {
		if _, err = tx.ExecContext(ctx, `INSERT INTO plugin_version_relation_refs(source_plugin_id,source_version_id,relation_id,relation_type,target_plugin_id,target_version_id) VALUES (?,?,?,?,?,NULL)`,
			n.Plugin.ID, n.VersionID, relation.ID, relation.Type, relation.TargetPluginID); err != nil {
			return "", ErrWrite
		}
	}
	// MySQL normalizes JSON column storage. Hash the bytes the database actually
	// stores, exactly as the reference-index backfill does, so readiness and later
	// verification agree on the snapshot digest.
	var storedRelations []byte
	if tx.QueryRowContext(ctx, `SELECT relations_json FROM plugin_versions WHERE plugin_id=? AND version_id=?`, n.Plugin.ID, n.VersionID).Scan(&storedRelations) != nil {
		return "", ErrRead
	}
	sum := sha256Hex(storedRelations)
	if _, err = tx.ExecContext(ctx, `INSERT INTO plugin_version_reference_index_state(source_plugin_id,source_version_id,relation_count,snapshot_digest,verified_at) VALUES (?,?,?,?,?)`,
		n.Plugin.ID, n.VersionID, len(n.Relations), sum, now); err != nil {
		return "", ErrWrite
	}
	return n.VersionID, nil
}

func replaceRelations(ctx context.Context, tx *sql.Tx, n Node, now time.Time) error {
	if _, err := tx.ExecContext(ctx, `UPDATE plugin_relations SET deleted_at=?,updated_at=? WHERE source_plugin_id=? AND deleted_at IS NULL`, now, now, n.Plugin.ID); err != nil {
		return ErrWrite
	}
	for _, relation := range n.Relations {
		var data any
		if len(relation.Data) > 0 {
			data = string(relation.Data)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO plugin_relations(relation_id,source_plugin_id,target_plugin_id,target_version_id,relation_type,sort_order,relation_json,status,created_by,created_at,updated_at) VALUES (?,?,?,NULL,?,?,?,1,?,?,?)`,
			relation.ID, n.Plugin.ID, relation.TargetPluginID, relation.Type, relation.SortOrder, data, "offline-migration", now, now); err != nil {
			return ErrWrite
		}
	}
	return nil
}

func publishNode(ctx context.Context, db *sql.DB, run *MigrationRun, resource MigrationPlanResource, en expectedNode, knownVersion *string, opts ExecuteOptions) (RunStatus, *string, *string, error) {
	var pluginType, visibility, listing string
	var owner string
	var space sql.NullString
	var current, published sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT plugin_type,visibility,owner_uid,space_id,listing_state,current_version_id,published_version_id FROM plugins WHERE plugin_id=? AND status=1 AND deleted_at IS NULL`, en.node.Plugin.ID).Scan(&pluginType, &visibility, &owner, &space, &listing, &current, &published); err != nil {
		return RunBlocked, nil, nil, ErrRunConflict
	}
	if pluginType == string(model.PluginTypeConnector) {
		if visibility == string(model.PluginVisibilitySpace) {
			return RunBlocked, nil, nil, fmt.Errorf("%w: space Connector review", ErrUnsupportedPlan)
		}
		if err := publishConnector(ctx, db, en.node, opts); err != nil {
			return RunBlocked, nil, nil, err
		}
		return RunCompleted, nil, nil, nil
	}
	version := knownVersion
	if current.Valid {
		version = stringPtr(current.String)
	}
	scope := pluginrepo.Scope{CallerUID: owner, SpaceID: space.String}
	switch visibility {
	case string(model.PluginVisibilityPrivate):
		if version == nil {
			return RunBlocked, nil, nil, ErrRunConflict
		}
		if _, err := migrationRepo(db).PublishManaged(ctx, scope, pluginrepo.PublishParams{
			PluginID: nPluginID(en), ExpectedVersionID: *version,
			OperatorID: opts.ActorID, OperatorName: opts.ActorName, RequestID: run.RunID,
		}); err != nil {
			return RunBlocked, nil, nil, err
		}
		return RunCompleted, version, nil, nil
	case string(model.PluginVisibilitySpace):
		if !space.Valid {
			return RunBlocked, nil, nil, ErrRunConflict
		}
		status, publishedVersion, reviewID, err := publishSpace(ctx, db, run, resource, en, owner, space.String, opts)
		return status, publishedVersion, reviewID, err
	default:
		return RunBlocked, nil, nil, fmt.Errorf("%w: system versioned direct publish is not part of this slice", ErrUnsupportedPlan)
	}
}

func publishSpace(ctx context.Context, db *sql.DB, run *MigrationRun, resource MigrationPlanResource, en expectedNode, owner, space string, opts ExecuteOptions) (RunStatus, *string, *string, error) {
	repo := migrationRepo(db)
	scope := pluginrepo.Scope{CallerUID: owner, SpaceID: space}
	label := ""
	if resource.VersionLabel != nil {
		label = *resource.VersionLabel
	}
	req := &model.PluginReviewRequest{
		PluginID: en.node.Plugin.ID, SpaceID: space, TargetScope: "space", Version: label,
		ApplicantUID: opts.ActorID, ApplicantName: opts.ActorName,
	}
	snapshot := pluginrepo.FrozenSnapshot{
		Manifest: en.node.Plugin.Manifest, Package: en.node.Plugin.Package,
		Relations: en.node.Relations, ManifestHash: en.node.Plugin.ManifestHash,
		PluginHash: en.node.Plugin.PluginHash, DeclaredContent: true,
	}
	if err := repo.InsertReviewRequest(ctx, scope, req, snapshot); err != nil {
		if !errors.Is(err, pluginrepo.ErrReviewPending) {
			return RunBlocked, nil, nil, err
		}
		existing, findErr := pendingReview(ctx, db, en.node.Plugin.ID)
		if findErr != nil || existing == "" {
			return RunBlocked, nil, nil, err
		}
		req.ID = existing
	}
	reviewID := req.ID
	policy, err := repo.GetReviewPolicy(ctx, scope)
	if err != nil {
		return RunBlocked, nil, &reviewID, err
	}
	if !policy.IsAutoApproveEnabled {
		return RunWaitingReview, nil, &reviewID, nil
	}
	if _, err = repo.ApproveReview(ctx, scope, pluginrepo.ApproveReviewParams{
		ReviewID: reviewID, ReviewerUID: opts.ActorID, ReviewerName: opts.ActorName,
		DecisionSource: model.ReviewDecisionSourcePolicy, RequestID: run.RunID, Now: opts.Now,
	}); err != nil {
		return RunBlocked, nil, &reviewID, err
	}
	var current, published sql.NullString
	if err = db.QueryRowContext(ctx, `SELECT current_version_id,published_version_id FROM plugins WHERE plugin_id=?`, en.node.Plugin.ID).Scan(&current, &published); err != nil || !published.Valid {
		return RunBlocked, nil, &reviewID, ErrRunConflict
	}
	return RunCompleted, stringPtr(published.String), &reviewID, nil
}

// migrationRepo enables the same managed-version and reference semantics the
// marketplace service uses. The CLI must not run the legacy publication path.
func migrationRepo(db *sql.DB) *pluginrepo.Repo {
	return pluginrepo.New(db).WithVersionManagement(true).WithReferences(true)
}

func publishConnector(ctx context.Context, db *sql.DB, node Node, opts ExecuteOptions) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return ErrRunConflict
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `INSERT INTO plugin_connector_effective
 (plugin_id,effective_revision,effective_digest,plugin_name,tags_json,tool_count,manifest_json,plugin_json,attachment_keys_json,manifest_hash,plugin_hash,current_version,visibility,updated_by,updated_at)
 SELECT plugin_id,1,CONCAT('sha256:',SHA2(CONCAT('v3',CHAR(31),manifest_hash,CHAR(31),plugin_hash,CHAR(31),plugin_name,CHAR(31),tool_count,CHAR(31),COALESCE(current_version,''),CHAR(31),visibility),256)),plugin_name,tags_json,tool_count,manifest_json,plugin_json,attachment_keys_json,manifest_hash,plugin_hash,current_version,visibility,?,?
 FROM plugins WHERE plugin_id=?
 ON DUPLICATE KEY UPDATE effective_revision=effective_revision+1,effective_digest=VALUES(effective_digest),plugin_name=VALUES(plugin_name),tags_json=VALUES(tags_json),tool_count=VALUES(tool_count),manifest_json=VALUES(manifest_json),plugin_json=VALUES(plugin_json),attachment_keys_json=VALUES(attachment_keys_json),manifest_hash=VALUES(manifest_hash),plugin_hash=VALUES(plugin_hash),current_version=VALUES(current_version),visibility=VALUES(visibility),updated_by=VALUES(updated_by),updated_at=VALUES(updated_at)`,
		opts.ActorID, opts.Now, node.Plugin.ID); err != nil {
		return ErrWrite
	}
	if _, err = tx.ExecContext(ctx, `UPDATE plugins SET listing_state='published',updated_at=? WHERE plugin_id=?`, opts.Now, node.Plugin.ID); err != nil {
		return ErrWrite
	}
	if err = tx.Commit(); err != nil {
		return ErrRunConflict
	}
	return nil
}

func resumeReview(ctx context.Context, db *sql.DB, node *MigrationRunNode) (RunStatus, *string, error) {
	if node.ReviewID == nil {
		return RunBlocked, nil, ErrRunConflict
	}
	var status, pluginID string
	if err := db.QueryRowContext(ctx, `SELECT status,plugin_id FROM plugin_review_requests WHERE review_id=? AND deleted_at IS NULL`, *node.ReviewID).Scan(&status, &pluginID); err != nil {
		return RunBlocked, nil, ErrRunConflict
	}
	switch status {
	case "pending":
		return RunWaitingReview, nil, nil
	case "approved":
		var published sql.NullString
		if err := db.QueryRowContext(ctx, `SELECT published_version_id FROM plugins WHERE plugin_id=?`, pluginID).Scan(&published); err != nil || !published.Valid {
			return RunBlocked, nil, ErrRunConflict
		}
		return RunCompleted, stringPtr(published.String), nil
	default:
		return RunBlocked, nil, nil
	}
}

func markPublished(ctx context.Context, db *sql.DB, run *MigrationRun, mapNode *MigrationRunNode, resource MigrationPlanResource, en expectedNode, version, reviewID *string, now time.Time) error {
	state := StepPublished
	if resource.Action == PlanActionUpdate {
		state = StepSourceSwitched
	}
	commit := map[string]any{}
	if version != nil {
		commit["output_version_id"] = *version
	}
	if reviewID != nil {
		commit["review_id"] = *reviewID
	}
	raw, _ := json.Marshal(commit)
	node := withNodeState(*mapNode, state, version, reviewID, raw, "")
	if err := UpsertRunNode(ctx, db, node); err != nil {
		return err
	}
	return nil
}

func withNodeState(node MigrationRunNode, state MapStepState, version, reviewID *string, commit json.RawMessage, reason string) MigrationRunNode {
	node.StepState = state
	node.TargetVersionID = version
	node.ReviewID = reviewID
	node.SourceCommitJSON = commit
	node.ErrorReason = nil
	if reason != "" {
		node.ErrorReason = stringPtr(reason)
	}
	node.UpdatedAt = time.Now().UTC()
	return node
}

func newRunNode(run *MigrationRun, resource MigrationPlanResource, en expectedNode, versionID, reviewID *string, state MapStepState) MigrationRunNode {
	now := time.Now().UTC()
	node := MigrationRunNode{
		RunID: run.RunID, SourceScope: run.SourceScope, SourceIdentity: run.SourceIdentity,
		PlanDigest: run.PlanDigest, SourceNodeKey: resource.SourceNodeKey, OriginNodeKey: resource.OriginNodeKey,
		SourceKind: run.SourceKind, SourceDigest: run.InputDigest, Action: resource.Action,
		TargetPluginID: stringPtr(en.node.TargetID), TargetVersionID: versionID, ReviewID: reviewID,
		StepState: state, CreatedAt: now, UpdatedAt: now,
	}
	if resource.SourceNodeKey == RootNodeKey {
		node.SourcePluginID = stringPtr(run.SourcePluginID)
		node.SourceVersionID = stringPtr(run.SourceVersionID)
	} else {
		sourceID := strings.TrimSuffix(resource.SourceNodeKey, ":mcp")
		node.SourcePluginID = stringPtr(sourceID)
	}
	if en.node.Type == model.PluginTypeConnector {
		if effective, ok := runPlanEffective(run, en.node.TargetID); ok {
			node.TargetEffectiveRevision = &effective.Revision
			node.TargetEffectiveDigest = stringPtr(effective.Digest)
		}
	}
	return node
}

func runPlanEffective(run *MigrationRun, pluginID string) (ConnectorEffective, bool) {
	for _, resource := range run.Plan.Resources {
		if resource.TargetPluginID == nil || *resource.TargetPluginID != pluginID {
			continue
		}
		if resource.TargetEffectiveRevision != nil && resource.TargetEffectiveDigest != nil {
			return ConnectorEffective{Revision: *resource.TargetEffectiveRevision, Digest: *resource.TargetEffectiveDigest}, true
		}
	}
	return ConnectorEffective{}, false
}

func blockRun(ctx context.Context, db *sql.DB, run *MigrationRun, reason string, cause error) (RunStatus, string, error) {
	_ = SetRunStatus(ctx, db, run.RunID, RunBlocked, reason, time.Now().UTC())
	return RunBlocked, reason, cause
}

func blockNode(ctx context.Context, db *sql.DB, run *MigrationRun, node *MigrationRunNode, reason string, cause error) (RunStatus, string, error) {
	blocked := withNodeState(*node, StepBlocked, node.TargetVersionID, node.ReviewID, node.SourceCommitJSON, reason)
	_ = UpsertRunNode(ctx, db, blocked)
	_ = SetRunStatus(ctx, db, run.RunID, RunBlocked, reason, time.Now().UTC())
	return RunBlocked, reason, cause
}

func recordRootAudit(ctx context.Context, db *sql.DB, run *MigrationRun, d Dataset, now time.Time) error {
	var exists int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM plugin_stock_migration_roots WHERE batch_id=? AND root_plugin_id=?`, run.BatchID, run.SourcePluginID).Scan(&exists); err != nil {
		return ErrRunConflict
	}
	if exists > 0 {
		return nil
	}
	rows, err := ListNode(ctx, db, run.RunID)
	if err != nil {
		return err
	}
	mappings, _ := json.Marshal(rows)
	var before, after string
	if root, ok := findResource(d, run.SourcePluginID); ok {
		raw, _ := json.Marshal(root)
		before = string(raw)
	}
	var manifest, pkg, hash, phash string
	if err := db.QueryRowContext(ctx, `SELECT manifest_json,plugin_json,manifest_hash,plugin_hash FROM plugins WHERE plugin_id=?`, run.SourcePluginID).Scan(&manifest, &pkg, &hash, &phash); err != nil {
		return ErrRunConflict
	}
	after = fmt.Sprintf(`{"plugin_id":%q,"manifest_hash":%q,"plugin_hash":%q}`, run.SourcePluginID, hash, phash)
	if _, err := db.ExecContext(ctx, `INSERT INTO plugin_stock_migration_roots(batch_id,root_plugin_id,source_digest,mappings_json,before_json,after_json,node_states_json,applied_at) VALUES (?,?,?,?,?,?,?,?)`,
		run.BatchID, run.SourcePluginID, run.InputDigest, string(mappings), before, after, "[]", now); err != nil {
		return ErrRunConflict
	}
	return nil
}

func planLabels(plan MigrationPlan) map[string]string {
	labels := map[string]string{}
	for _, resource := range plan.Resources {
		if resource.VersionLabel == nil {
			continue
		}
		key := resource.SourceNodeKey
		if key == RootNodeKey {
			key = plan.SourcePluginID
		}
		labels[key] = *resource.VersionLabel
	}
	return labels
}

func executionNodeKey(node Node, rootID string) string {
	if node.SourceID == rootID {
		return RootNodeKey
	}
	return node.SourceID
}

func categoryValue(raw json.RawMessage) any {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var value string
	if json.Unmarshal(raw, &value) != nil || value == "" {
		return nil
	}
	return value
}

func currentVersion(ctx context.Context, tx *sql.Tx, pluginID string) (*string, error) {
	var value sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT current_version_id FROM plugins WHERE plugin_id=?`, pluginID).Scan(&value); err != nil {
		return nil, ErrRead
	}
	if !value.Valid {
		return nil, ErrWrite
	}
	return &value.String, nil
}

func pendingReview(ctx context.Context, db *sql.DB, pluginID string) (string, error) {
	var reviewID string
	err := db.QueryRowContext(ctx, `SELECT review_id FROM plugin_review_requests WHERE plugin_id=? AND status='pending' AND deleted_at IS NULL LIMIT 1`, pluginID).Scan(&reviewID)
	if err != nil {
		return "", err
	}
	return reviewID, nil
}

func sha256Hex(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func nPluginID(en expectedNode) string { return en.node.Plugin.ID }

func derefOr(value *string, fallback string) string {
	if value == nil || *value == "" {
		return fallback
	}
	return *value
}

func sortNodes(nodes []MigrationRunNode) {
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].SourceNodeKey < nodes[j].SourceNodeKey })
}
