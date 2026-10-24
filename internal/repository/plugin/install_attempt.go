package plugin

import (
	"context"
	"database/sql"
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
}

func (r *Repo) GetInstallAttempt(ctx context.Context, sc Scope, workspace, key string) (*InstallAttempt, error) {
	a := &InstallAttempt{WorkspaceID: workspace, KeyDigest: key}
	err := r.db.QueryRowContext(ctx, `SELECT plugin_id,root_version_id,adapter_version,input_fingerprint,request_fingerprint,agent_id,team_id,completed,created_at FROM plugin_install_attempts WHERE space_id=? AND caller_uid=? AND workspace_id=? AND key_digest=?`, sc.SpaceID, sc.CallerUID, workspace, key).Scan(&a.PluginID, &a.RootVersionID, &a.AdapterVersion, &a.InputFingerprint, &a.RequestFingerprint, &a.AgentID, &a.TeamID, &a.Completed, &a.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return a, err
}

// ClaimInstallAttempt chooses the first successfully prepared request. Concurrent
// contenders read that winner and compare fingerprints before any remote call.
func (r *Repo) ClaimInstallAttempt(ctx context.Context, sc Scope, a InstallAttempt) (*InstallAttempt, error) {
	_, err := r.db.ExecContext(ctx, `INSERT INTO plugin_install_attempts(space_id,caller_uid,workspace_id,key_digest,plugin_id,root_version_id,adapter_version,input_fingerprint,request_fingerprint,created_at) VALUES (?,?,?,?,?,?,?,?,?,?) ON DUPLICATE KEY UPDATE key_digest=key_digest`, sc.SpaceID, sc.CallerUID, a.WorkspaceID, a.KeyDigest, a.PluginID, a.RootVersionID, a.AdapterVersion, a.InputFingerprint, a.RequestFingerprint, r.now())
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
