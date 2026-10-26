package stockmigration

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/id"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/migrationauth"
)

// RunStatus is the only terminal vocabulary for an offline migration run.
type RunStatus string

const (
	RunCompleted     RunStatus = "completed"
	RunWaitingReview RunStatus = "waiting_review"
	RunBlocked       RunStatus = "blocked"
)

// MapStepState mirrors API §5. A connector never has a target_version_id.
type MapStepState string

const (
	StepReserved       MapStepState = "reserved"
	StepCreated        MapStepState = "created"
	StepWaitingReview  MapStepState = "waiting_review"
	StepPublished      MapStepState = "published"
	StepSourceSwitched MapStepState = "source_switched"
	StepBlocked        MapStepState = "blocked"
)

var (
	ErrRunNotFound         = errors.New("migration run not found")
	ErrRunConflict         = errors.New("migration run conflicts with an existing run")
	ErrRunAuthorization    = errors.New("migration authorization does not bind this run")
	ErrAuthorizationReused = errors.New("migration authorization is already bound to another run")
)

// MigrationRun is one persisted frozen plan execution.
type MigrationRun struct {
	RunID                string
	BatchID              string
	EnvironmentID        string
	SourceKind           string
	SourceScope          string
	SourceIdentity       string
	SourcePluginID       string
	SourceVersionID      string
	ExpectedEditRevision uint64
	InputDigest          string
	PlanDigest           string
	RootNodeKey          string
	Plan                 MigrationPlan
	PlanObjectKey        *string
	Status               RunStatus
	BlockedReason        *string
	LastAuthorizationID  *string
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

// MigrationRunNode is the authoritative per-node mapping and recovery state.
type MigrationRunNode struct {
	RunID                   string
	SourceScope             string
	SourceIdentity          string
	PlanDigest              string
	SourceNodeKey           string
	OriginNodeKey           string
	SourceKind              string
	SourcePluginID          *string
	SourceVersionID         *string
	SourceDigest            string
	Action                  string
	TargetPluginID          *string
	TargetVersionID         *string
	TargetEffectiveRevision *uint64
	TargetEffectiveDigest   *string
	StepState               MapStepState
	ReviewID                *string
	SourceCommitJSON        json.RawMessage
	ErrorReason             *string
	CreatedAt               time.Time
	UpdatedAt               time.Time
}

// OpenRunOptions binds a frozen plan to a verified offline authorization.
type OpenRunOptions struct {
	Plan          MigrationPlan
	PlanDigest    string
	Environment   string
	Authorization migrationauth.Payload
	Signature     string
	Now           time.Time
}

// OpenRun returns the run already arbitrated by
// (source_scope, source_identity, plan_digest, root_node_key=root), or creates
// it and its initial authorization binding atomically. The boolean reports
// whether this call created the run.
func OpenRun(ctx context.Context, db *sql.DB, opts OpenRunOptions) (*MigrationRun, bool, error) {
	if opts.Now.IsZero() {
		opts.Now = time.Now().UTC()
	}
	if opts.Plan.SourceScope == "" || opts.Plan.SourceIdentity == "" || opts.PlanDigest == "" {
		return nil, false, ErrRunConflict
	}
	if opts.Authorization.BatchID == "" || opts.Authorization.AuthorizationID == "" ||
		opts.Authorization.PlanDigest != opts.PlanDigest ||
		opts.Authorization.SourceScope != opts.Plan.SourceScope ||
		opts.Authorization.SourceIdentity != opts.Plan.SourceIdentity ||
		opts.Authorization.EnvironmentID != opts.Environment {
		return nil, false, ErrRunAuthorization
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, ErrRunConflict
	}
	defer tx.Rollback()
	run, err := findRunByIdentity(ctx, tx, opts.Plan.SourceScope, opts.Plan.SourceIdentity, opts.PlanDigest, RootNodeKey, true)
	if err != nil && !errors.Is(err, ErrRunNotFound) {
		return nil, false, err
	}
	if run != nil {
		if err = verifyRunAuthorization(*run, opts.Authorization); err != nil {
			return nil, false, err
		}
		if err = bindAuthorization(ctx, tx, run, opts.Authorization, opts.Signature, opts.Now); err != nil {
			return nil, false, err
		}
		if err = tx.Commit(); err != nil {
			return nil, false, ErrRunConflict
		}
		return run, false, nil
	}
	// An authorization id can bind exactly one run. Checking before insert gives
	// a stable reason instead of a raw duplicate-key error.
	var existingRun string
	err = tx.QueryRowContext(ctx, `SELECT COALESCE(run_id,'') FROM plugin_migration_authorizations WHERE authorization_id=? FOR UPDATE`, opts.Authorization.AuthorizationID).Scan(&existingRun)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, false, ErrRunConflict
	}
	if existingRun != "" {
		return nil, false, ErrAuthorizationReused
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO plugin_stock_migration_batches(batch_id,state,created_at) VALUES (?,'active',?) ON DUPLICATE KEY UPDATE batch_id=batch_id`, opts.Authorization.BatchID, opts.Now); err != nil {
		return nil, false, ErrRunConflict
	}
	run = &MigrationRun{
		RunID:                id.New(),
		BatchID:              opts.Authorization.BatchID,
		EnvironmentID:        opts.Environment,
		SourceKind:           opts.Plan.SourceKind,
		SourceScope:          opts.Plan.SourceScope,
		SourceIdentity:       opts.Plan.SourceIdentity,
		SourcePluginID:       opts.Plan.SourcePluginID,
		SourceVersionID:      opts.Plan.SourceVersionID,
		ExpectedEditRevision: derefRevision(opts.Plan.ExpectedEditRevision),
		InputDigest:          opts.Plan.InputDigest,
		PlanDigest:           opts.PlanDigest,
		RootNodeKey:          RootNodeKey,
		Plan:                 opts.Plan,
		Status:               RunBlocked,
		CreatedAt:            opts.Now,
		UpdatedAt:            opts.Now,
	}
	planJSON, err := json.Marshal(opts.Plan)
	if err != nil {
		return nil, false, ErrRunConflict
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO plugin_dependency_migration_runs
 (run_id,batch_id,environment_id,source_kind,source_scope,source_identity,source_plugin_id,source_version_id,expected_edit_revision,input_digest,plan_digest,root_node_key,plan_json,status,created_at,updated_at)
 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		run.RunID, run.BatchID, run.EnvironmentID, run.SourceKind, run.SourceScope, run.SourceIdentity,
		run.SourcePluginID, run.SourceVersionID, run.ExpectedEditRevision, run.InputDigest, run.PlanDigest,
		run.RootNodeKey, string(planJSON), string(run.Status), opts.Now, opts.Now); err != nil {
		return nil, false, ErrRunConflict
	}
	if _, err = tx.ExecContext(ctx, `UPDATE plugin_stock_migration_batches SET run_id=?,plan_digest=?,environment_id=?,authorization_id=? WHERE batch_id=?`,
		run.RunID, run.PlanDigest, run.EnvironmentID, opts.Authorization.AuthorizationID, run.BatchID); err != nil {
		return nil, false, ErrRunConflict
	}
	if err = bindAuthorization(ctx, tx, run, opts.Authorization, opts.Signature, opts.Now); err != nil {
		return nil, false, err
	}
	if err = tx.Commit(); err != nil {
		return nil, false, ErrRunConflict
	}
	return run, true, nil
}

// LoadRun reads one persisted run. resume never accepts a replacement plan.
func LoadRun(ctx context.Context, db *sql.DB, runID string) (*MigrationRun, error) {
	if strings.TrimSpace(runID) == "" {
		return nil, ErrRunNotFound
	}
	row := db.QueryRowContext(ctx, runColumns+` WHERE run_id=?`, runID)
	run, err := scanRun(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrRunNotFound
	}
	return run, err
}

// BindAuthorization verifies and records a renewal for an existing run.
func BindAuthorization(ctx context.Context, db *sql.DB, run *MigrationRun, auth migrationauth.Payload, signature string, now time.Time) error {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if err := verifyRunAuthorization(*run, auth); err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return ErrRunConflict
	}
	defer tx.Rollback()
	locked, err := findRunByID(ctx, tx, run.RunID, true)
	if err != nil {
		return err
	}
	if err = verifyRunAuthorization(*locked, auth); err != nil {
		return err
	}
	if err = bindAuthorization(ctx, tx, locked, auth, signature, now); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return ErrRunConflict
	}
	run.LastAuthorizationID = locked.LastAuthorizationID
	return nil
}

// SetRunStatus is the only state transition writer. A completed run is terminal
// and cannot be reopened by a later resume.
func SetRunStatus(ctx context.Context, db *sql.DB, runID string, status RunStatus, reason string, now time.Time) error {
	if status != RunCompleted && status != RunWaitingReview && status != RunBlocked {
		return ErrRunConflict
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return ErrRunConflict
	}
	defer tx.Rollback()
	run, err := findRunByID(ctx, tx, runID, true)
	if err != nil {
		return err
	}
	if run.Status == RunCompleted {
		return tx.Commit()
	}
	var blocked any
	if status == RunBlocked && reason != "" {
		blocked = reason
	}
	if _, err = tx.ExecContext(ctx, `UPDATE plugin_dependency_migration_runs SET status=?,blocked_reason=?,updated_at=? WHERE run_id=?`, string(status), blocked, now, runID); err != nil {
		return ErrRunConflict
	}
	if err = tx.Commit(); err != nil {
		return ErrRunConflict
	}
	return nil
}

// UpsertRunNode records the map row for one source node.
func UpsertRunNode(ctx context.Context, db *sql.DB, node MigrationRunNode) error {
	return upsertRunNode(ctx, db, node)
}

type sqlExecer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func upsertRunNode(ctx context.Context, exec sqlExecer, node MigrationRunNode) error {
	now := node.UpdatedAt
	if now.IsZero() {
		now = time.Now().UTC()
	}
	_, err := exec.ExecContext(ctx, `INSERT INTO plugin_dependency_migration_map
 (run_id,source_scope,source_identity,plan_digest,source_node_key,origin_node_key,source_kind,source_plugin_id,source_version_id,source_digest,action,target_plugin_id,target_version_id,target_effective_revision,target_effective_digest,step_state,review_id,source_commit_json,error_reason,created_at,updated_at)
 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
 ON DUPLICATE KEY UPDATE origin_node_key=VALUES(origin_node_key),source_plugin_id=VALUES(source_plugin_id),source_version_id=VALUES(source_version_id),source_digest=VALUES(source_digest),action=VALUES(action),target_plugin_id=VALUES(target_plugin_id),target_version_id=VALUES(target_version_id),target_effective_revision=VALUES(target_effective_revision),target_effective_digest=VALUES(target_effective_digest),step_state=VALUES(step_state),review_id=VALUES(review_id),source_commit_json=VALUES(source_commit_json),error_reason=VALUES(error_reason),updated_at=VALUES(updated_at)`,
		node.RunID, node.SourceScope, node.SourceIdentity, node.PlanDigest, node.SourceNodeKey, node.OriginNodeKey,
		node.SourceKind, node.SourcePluginID, node.SourceVersionID, node.SourceDigest, node.Action,
		node.TargetPluginID, node.TargetVersionID, node.TargetEffectiveRevision, node.TargetEffectiveDigest,
		string(node.StepState), node.ReviewID, nullableJSONBytes(node.SourceCommitJSON), node.ErrorReason, now, now)
	if err != nil {
		return ErrRunConflict
	}
	return nil
}

// ListNode reads all map rows for a run in source_node_key order.
func ListNode(ctx context.Context, db *sql.DB, runID string) ([]MigrationRunNode, error) {
	rows, err := db.QueryContext(ctx, `SELECT run_id,source_scope,source_identity,plan_digest,source_node_key,origin_node_key,source_kind,source_plugin_id,source_version_id,source_digest,action,target_plugin_id,target_version_id,target_effective_revision,target_effective_digest,step_state,review_id,source_commit_json,error_reason,created_at,updated_at FROM plugin_dependency_migration_map WHERE run_id=? ORDER BY source_node_key`, runID)
	if err != nil {
		return nil, ErrRunConflict
	}
	defer rows.Close()
	out := []MigrationRunNode{}
	for rows.Next() {
		var node MigrationRunNode
		var sourcePlugin, sourceVersion, targetPlugin, targetVersion, targetDigest, review, reason sql.NullString
		var effectiveRevision sql.NullInt64
		var commit []byte
		var state string
		if err = rows.Scan(&node.RunID, &node.SourceScope, &node.SourceIdentity, &node.PlanDigest,
			&node.SourceNodeKey, &node.OriginNodeKey, &node.SourceKind, &sourcePlugin, &sourceVersion,
			&node.SourceDigest, &node.Action, &targetPlugin, &targetVersion, &effectiveRevision,
			&targetDigest, &state, &review, &commit, &reason, &node.CreatedAt, &node.UpdatedAt); err != nil {
			return nil, ErrRunConflict
		}
		node.StepState = MapStepState(state)
		node.SourcePluginID = nullableStringPtr(sourcePlugin)
		node.SourceVersionID = nullableStringPtr(sourceVersion)
		node.TargetPluginID = nullableStringPtr(targetPlugin)
		node.TargetVersionID = nullableStringPtr(targetVersion)
		node.TargetEffectiveDigest = nullableStringPtr(targetDigest)
		node.ReviewID = nullableStringPtr(review)
		node.ErrorReason = nullableStringPtr(reason)
		if effectiveRevision.Valid {
			value := uint64(effectiveRevision.Int64)
			node.TargetEffectiveRevision = &value
		}
		if len(commit) > 0 {
			node.SourceCommitJSON = json.RawMessage(commit)
		}
		out = append(out, node)
	}
	if err = rows.Err(); err != nil {
		return nil, ErrRunConflict
	}
	return out, nil
}

// FindNode returns one map row, or nil when it has not been reserved yet.
func FindNode(ctx context.Context, db *sql.DB, runID, sourceNodeKey string) (*MigrationRunNode, error) {
	rows, err := ListNode(ctx, db, runID)
	if err != nil {
		return nil, err
	}
	for i := range rows {
		if rows[i].SourceNodeKey == sourceNodeKey {
			return &rows[i], nil
		}
	}
	return nil, nil
}

const runColumns = `SELECT run_id,batch_id,environment_id,source_kind,source_scope,source_identity,source_plugin_id,source_version_id,expected_edit_revision,input_digest,plan_digest,root_node_key,plan_json,frozen_plan_object_key,status,blocked_reason,last_authorization_id,created_at,updated_at FROM plugin_dependency_migration_runs`

type rowScanner interface {
	Scan(dest ...any) error
}

func scanRun(row rowScanner) (*MigrationRun, error) {
	var run MigrationRun
	var planJSON []byte
	var objectKey, reason, authID sql.NullString
	var status string
	if err := row.Scan(&run.RunID, &run.BatchID, &run.EnvironmentID, &run.SourceKind, &run.SourceScope,
		&run.SourceIdentity, &run.SourcePluginID, &run.SourceVersionID, &run.ExpectedEditRevision,
		&run.InputDigest, &run.PlanDigest, &run.RootNodeKey, &planJSON, &objectKey, &status,
		&reason, &authID, &run.CreatedAt, &run.UpdatedAt); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(planJSON, &run.Plan); err != nil {
		return nil, ErrRunConflict
	}
	digest, err := PlanDigest(run.Plan)
	if err != nil || digest != run.PlanDigest || run.Plan.SourceScope != run.SourceScope ||
		run.Plan.SourceIdentity != run.SourceIdentity {
		return nil, ErrRunConflict
	}
	run.Status = RunStatus(status)
	run.PlanObjectKey = nullableStringPtr(objectKey)
	run.BlockedReason = nullableStringPtr(reason)
	run.LastAuthorizationID = nullableStringPtr(authID)
	return &run, nil
}

func findRunByIdentity(ctx context.Context, tx *sql.Tx, scope, identity, digest, root string, lock bool) (*MigrationRun, error) {
	query := runColumns + ` WHERE source_scope=? AND source_identity=? AND plan_digest=? AND root_node_key=?`
	if lock {
		query += ` FOR UPDATE`
	}
	run, err := scanRun(tx.QueryRowContext(ctx, query, scope, identity, digest, root))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrRunNotFound
	}
	if err != nil {
		return nil, ErrRunConflict
	}
	return run, nil
}

func findRunByID(ctx context.Context, tx *sql.Tx, runID string, lock bool) (*MigrationRun, error) {
	query := runColumns + ` WHERE run_id=?`
	if lock {
		query += ` FOR UPDATE`
	}
	run, err := scanRun(tx.QueryRowContext(ctx, query, runID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrRunNotFound
	}
	if err != nil {
		return nil, ErrRunConflict
	}
	return run, nil
}

func verifyRunAuthorization(run MigrationRun, auth migrationauth.Payload) error {
	if auth.BatchID != run.BatchID || auth.EnvironmentID != run.EnvironmentID ||
		auth.PlanDigest != run.PlanDigest || auth.SourceScope != run.SourceScope ||
		auth.SourceIdentity != run.SourceIdentity {
		return ErrRunAuthorization
	}
	return nil
}

func bindAuthorization(ctx context.Context, tx *sql.Tx, run *MigrationRun, auth migrationauth.Payload, signature string, now time.Time) error {
	if auth.AuthorizationID == "" || signature == "" {
		return ErrRunAuthorization
	}
	if run.LastAuthorizationID != nil && *run.LastAuthorizationID == auth.AuthorizationID {
		return nil
	}
	issued, err := time.Parse(time.RFC3339, auth.IssuedAt)
	if err != nil {
		return ErrRunAuthorization
	}
	expires, err := time.Parse(time.RFC3339, auth.ExpiresAt)
	if err != nil {
		return ErrRunAuthorization
	}
	payload, err := json.Marshal(auth)
	if err != nil {
		return ErrRunAuthorization
	}
	replaced := run.LastAuthorizationID
	var replacedValue any
	if replaced != nil {
		replacedValue = *replaced
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO plugin_migration_authorizations
 (authorization_id,batch_id,run_id,plan_digest,environment_id,key_id,issuer_id,actor_id,source_scope,source_identity,issued_at,expires_at,payload_json,signature,replaced_authorization_id,created_at)
 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		auth.AuthorizationID, auth.BatchID, run.RunID, auth.PlanDigest, auth.EnvironmentID, auth.KeyID,
		auth.IssuerID, auth.ActorID, auth.SourceScope, auth.SourceIdentity, issued, expires, string(payload),
		signature, replacedValue, now)
	if err != nil {
		var existingRun sql.NullString
		if e := tx.QueryRowContext(ctx, `SELECT run_id FROM plugin_migration_authorizations WHERE authorization_id=?`, auth.AuthorizationID).Scan(&existingRun); e != nil {
			return ErrAuthorizationReused
		}
		if !existingRun.Valid || existingRun.String != run.RunID {
			return ErrAuthorizationReused
		}
		return nil
	}
	if result != nil {
		if _, err = tx.ExecContext(ctx, `UPDATE plugin_dependency_migration_runs SET last_authorization_id=?,updated_at=? WHERE run_id=?`, auth.AuthorizationID, now, run.RunID); err != nil {
			return ErrRunConflict
		}
	}
	event := "renewed"
	if replaced == nil {
		event = "bound"
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO plugin_migration_authorization_audit(authorization_id,run_id,event,actor_id,environment_id,replaced_authorization_id,created_at) VALUES (?,?,?,?,?,?,?)`,
		auth.AuthorizationID, run.RunID, event, auth.ActorID, auth.EnvironmentID, replacedValue, now); err != nil {
		return ErrRunConflict
	}
	run.LastAuthorizationID = stringPtr(auth.AuthorizationID)
	return nil
}

func nullableStringPtr(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	return &value.String
}

func nullableJSONBytes(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	return string(raw)
}

func derefRevision(value *uint64) uint64 {
	if value == nil {
		return 0
	}
	return *value
}

func safeReason(err error) string {
	switch {
	case errors.Is(err, ErrPlanStale):
		return "plan_stale"
	case errors.Is(err, ErrRunAuthorization), errors.Is(err, ErrAuthorizationReused):
		return "authorization_invalid"
	case errors.Is(err, ErrRunConflict):
		return "run_conflict"
	default:
		return "blocked"
	}
}
