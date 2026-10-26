package plugin

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// InstallAttempt contains identities and one-way fingerprints only. Tokens,
// custom_env and complete Fleet requests must never be stored here.
type InstallAttempt struct {
	WorkspaceID, KeyDigest, PluginID, RootVersionID, AdapterVersion string
	InputFingerprint, RequestFingerprint                            string
	AgentID, TeamID                                                 string
	Completed                                                       bool
	CreatedAt                                                       time.Time
	FrozenPlan                                                      json.RawMessage
	HMACKeyID, IntentDigest, State                                  string
	HadUnknown                                                      bool
	LeaseToken                                                      string
	LeaseUntil, RetryUntil                                          *time.Time
}

func (r *Repo) GetInstallAttempt(ctx context.Context, sc Scope, workspace, key string) (*InstallAttempt, error) {
	a := &InstallAttempt{WorkspaceID: workspace, KeyDigest: key}
	var frozen []byte
	err := r.db.QueryRowContext(ctx, `SELECT plugin_id,root_version_id,adapter_version,input_fingerprint,request_fingerprint,agent_id,team_id,completed,created_at,frozen_plan_json,hmac_key_id,intent_digest,attempt_state,had_unknown,COALESCE(lease_token,''),lease_until,retry_until FROM plugin_install_attempts WHERE space_id=? AND caller_uid=? AND workspace_id=? AND key_digest=?`, sc.SpaceID, sc.CallerUID, workspace, key).Scan(&a.PluginID, &a.RootVersionID, &a.AdapterVersion, &a.InputFingerprint, &a.RequestFingerprint, &a.AgentID, &a.TeamID, &a.Completed, &a.CreatedAt, &frozen, &a.HMACKeyID, &a.IntentDigest, &a.State, &a.HadUnknown, &a.LeaseToken, &a.LeaseUntil, &a.RetryUntil)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	a.FrozenPlan = cloneJSON(frozen)
	return a, err
}

// ClaimInstallAttempt chooses the first successfully prepared request. Concurrent
// contenders read that winner and compare fingerprints before any remote call.
func (r *Repo) ClaimInstallAttempt(ctx context.Context, sc Scope, a InstallAttempt) (*InstallAttempt, error) {
	_, err := r.db.ExecContext(ctx, `INSERT INTO plugin_install_attempts(space_id,caller_uid,workspace_id,key_digest,plugin_id,root_version_id,adapter_version,input_fingerprint,request_fingerprint,created_at,frozen_plan_json,hmac_key_id,intent_digest,attempt_state,had_unknown) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON DUPLICATE KEY UPDATE key_digest=key_digest`, sc.SpaceID, sc.CallerUID, a.WorkspaceID, a.KeyDigest, a.PluginID, a.RootVersionID, a.AdapterVersion, a.InputFingerprint, a.RequestFingerprint, r.now(), jsonColumn(a.FrozenPlan), a.HMACKeyID, a.IntentDigest, installInitialState(a), len(a.FrozenPlan) == 0)
	if err != nil {
		return nil, err
	}
	return r.GetInstallAttempt(ctx, sc, a.WorkspaceID, a.KeyDigest)
}
func (r *Repo) CompleteInstallAttempt(ctx context.Context, sc Scope, a InstallAttempt) error {
	result, err := r.db.ExecContext(ctx, `UPDATE plugin_install_attempts SET completed=TRUE,agent_id=?,team_id=? WHERE space_id=? AND caller_uid=? AND workspace_id=? AND key_digest=? AND request_fingerprint=? AND root_version_id=? AND (completed=FALSE OR (agent_id=? AND team_id=?))`, a.AgentID, a.TeamID, sc.SpaceID, sc.CallerUID, a.WorkspaceID, a.KeyDigest, a.RequestFingerprint, a.RootVersionID, a.AgentID, a.TeamID)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		existing, err := r.GetInstallAttempt(ctx, sc, a.WorkspaceID, a.KeyDigest)
		if err != nil {
			return err
		}
		if existing == nil || !existing.Completed || existing.RootVersionID != a.RootVersionID || existing.RequestFingerprint != a.RequestFingerprint || existing.AgentID != a.AgentID || existing.TeamID != a.TeamID {
			return ErrConflict
		}
	}
	return nil
}

func installInitialState(a InstallAttempt) string {
	if len(a.FrozenPlan) == 0 {
		return "UNKNOWN"
	}
	return "PREPARED"
}

// AcquireInstallDispatch atomically claims a short send lease. retry_until is
// fixed on the first possible send and is never extended by a retry.
func (r *Repo) AcquireInstallDispatch(ctx context.Context, sc Scope, a InstallAttempt, token string, now time.Time, lease, retry time.Duration) (bool, error) {
	result, err := r.db.ExecContext(ctx, `UPDATE plugin_install_attempts SET
 had_unknown=IF(attempt_state='DISPATCHING',TRUE,had_unknown),attempt_state='DISPATCHING',lease_token=?,lease_until=?,retry_until=COALESCE(retry_until,?)
 WHERE space_id=? AND caller_uid=? AND workspace_id=? AND key_digest=?
 AND frozen_plan_json IS NOT NULL AND request_fingerprint=?
 AND attempt_state IN ('PREPARED','UNKNOWN','DISPATCHING')
 AND (lease_until IS NULL OR lease_until<=?) AND (retry_until IS NULL OR retry_until>?)`, token, now.Add(lease), now.Add(retry), sc.SpaceID, sc.CallerUID, a.WorkspaceID, a.KeyDigest, a.RequestFingerprint, now, now)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n == 1, err
}

// SetInstallDispatchUnknown preserves evidence of every possible remote commit.
// A stale worker cannot release another sender's lease or overwrite success.
func (r *Repo) SetInstallDispatchUnknown(ctx context.Context, sc Scope, a InstallAttempt, token string) error {
	_, err := r.db.ExecContext(ctx, `UPDATE plugin_install_attempts SET attempt_state='UNKNOWN',had_unknown=TRUE,lease_token=NULL,lease_until=NULL WHERE space_id=? AND caller_uid=? AND workspace_id=? AND key_digest=? AND lease_token=? AND attempt_state='DISPATCHING'`, sc.SpaceID, sc.CallerUID, a.WorkspaceID, a.KeyDigest, token)
	return err
}

// FinishInstallDispatch returns true only for the first successful transition,
// allowing installation metrics to count a commit exactly once. Success from a
// superseded lease is not allowed to overwrite the current worker's result.
func (r *Repo) FinishInstallDispatch(ctx context.Context, sc Scope, a InstallAttempt, token string) (bool, error) {
	result, err := r.db.ExecContext(ctx, `UPDATE plugin_install_attempts SET completed=TRUE,attempt_state='SUCCEEDED',agent_id=?,team_id=?,lease_token=NULL,lease_until=NULL WHERE space_id=? AND caller_uid=? AND workspace_id=? AND key_digest=? AND lease_token=? AND attempt_state='DISPATCHING' AND request_fingerprint=?`, a.AgentID, a.TeamID, sc.SpaceID, sc.CallerUID, a.WorkspaceID, a.KeyDigest, token, a.RequestFingerprint)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n == 1, err
}

// FailInstallDispatch records a definite rejection only when no earlier send
// was uncertain. A later 4xx can never erase a possible earlier remote commit.
func (r *Repo) FailInstallDispatch(ctx context.Context, sc Scope, a InstallAttempt, token string) (bool, error) {
	result, err := r.db.ExecContext(ctx, `UPDATE plugin_install_attempts SET attempt_state='FAILED',lease_token=NULL,lease_until=NULL WHERE space_id=? AND caller_uid=? AND workspace_id=? AND key_digest=? AND lease_token=? AND attempt_state='DISPATCHING' AND had_unknown=FALSE`, sc.SpaceID, sc.CallerUID, a.WorkspaceID, a.KeyDigest, token)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n == 1, err
}
