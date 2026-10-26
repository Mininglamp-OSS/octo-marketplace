package plugin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/fleet"
	pluginrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/plugin"
)

// Fleet retains receipts for 24 hours. A local record never licenses resubmission
// after that window: an uncertain call might otherwise create a second resource.
const installReceiptWindow = 24 * time.Hour

type InstallationAttemptStore interface {
	GetInstallAttempt(context.Context, pluginrepo.Scope, string, string) (*pluginrepo.InstallAttempt, error)
	ClaimInstallAttempt(context.Context, pluginrepo.Scope, pluginrepo.InstallAttempt) (*pluginrepo.InstallAttempt, error)
	CompleteInstallAttempt(context.Context, pluginrepo.Scope, pluginrepo.InstallAttempt) error
}

func digestInstall(v any) string {
	raw, _ := json.Marshal(v)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func (s *Service) createPinnedInstallation(ctx context.Context, caller Caller, id string, p InstallationParams) (*InstallationOutcome, error) {
	store, ok := s.repo.(InstallationAttemptStore)
	if !ok {
		return nil, ErrInvalidRequest
	}
	key := digestInstall(p.IdempotencyKey)
	// Token rotation does not change the operation. Environment values affect the
	// fingerprint but only its digest is stored; neither values nor payload persist.
	input := digestInstall(struct {
		PluginID, RuntimeID, ResourceName string
		CustomEnv                         map[string]string
	}{id, p.RuntimeID, p.ResourceName, p.CustomEnv})
	attempt, err := store.GetInstallAttempt(ctx, scope(caller), p.WorkspaceID, key)
	if err != nil {
		return nil, mapStoreError(err)
	}
	version := ""
	if attempt != nil {
		if err = s.checkInstallAttempt(attempt, id, input); err != nil {
			return nil, err
		}
		version = attempt.RootVersionID
	}
	plan, err := s.resolvePinnedInstallation(ctx, caller, id, version)
	if err != nil {
		return nil, err
	}
	in := pinnedPlanRequest(plan, p)
	body, err := json.Marshal(in)
	if err != nil {
		return nil, installationInvalid("definition", "invalid_json")
	}
	if len(body) > maxCapabilityBytes {
		return nil, ErrTooLarge
	}
	fingerprint := digestInstall(in)
	if attempt == nil {
		attempt, err = store.ClaimInstallAttempt(ctx, scope(caller), pluginrepo.InstallAttempt{WorkspaceID: p.WorkspaceID, KeyDigest: key, PluginID: id, RootVersionID: plan.RootVersionID, AdapterVersion: SkillInstallAdapterVersion, InputFingerprint: input, RequestFingerprint: fingerprint})
		if err != nil {
			return nil, mapStoreError(err)
		}
	}
	if attempt == nil {
		return nil, ErrConflict
	}
	if err = s.checkInstallAttempt(attempt, id, input); err != nil {
		return nil, err
	}
	if attempt.RootVersionID != plan.RootVersionID || attempt.RequestFingerprint != fingerprint {
		return nil, ErrConflict
	}
	// Fleet remains the workspace authorization and receipt authority even for
	// completed local attempts: always re-submit the identical same-key request.
	completed := attempt.Completed
	result, err := s.capabilityInstaller.InstallCapability(ctx, p.Token, caller.SpaceID, p.WorkspaceID, p.IdempotencyKey, *in)
	if err != nil {
		return nil, &InstallationAttemptError{Err: err}
	}
	if result == nil {
		return nil, &InstallationAttemptError{Err: errors.New("missing installation result")}
	}
	attempt.AgentID, attempt.TeamID = result.ExpertID, result.ExpertTeamID
	// A failed receipt write follows a possible Fleet commit. Same-key retry must
	// go back to Fleet, never fall back to the legacy non-idempotent entrypoint.
	if err = store.CompleteInstallAttempt(ctx, scope(caller), *attempt); err != nil {
		return nil, &InstallationAttemptError{Err: err}
	}
	s.trackInstall(ctx, id)
	return &InstallationOutcome{AgentID: result.ExpertID, SquadID: result.ExpertTeamID, Replayed: completed || (result.ReplayKnown && result.Replayed)}, nil
}
func (s *Service) checkInstallAttempt(a *pluginrepo.InstallAttempt, id, input string) error {
	if a.PluginID != id || a.InputFingerprint != input || a.AdapterVersion != SkillInstallAdapterVersion || a.CreatedAt.IsZero() || s.now().Sub(a.CreatedAt) >= installReceiptWindow || a.CreatedAt.After(s.now().Add(time.Minute)) {
		return ErrConflict
	}
	return nil
}
func pinnedPlanRequest(plan *pinnedInstallationPlan, p InstallationParams) *fleet.CapabilityInstallRequest {
	def := plan.Definition
	if p.ResourceName != "" {
		if def.ExpertTeam != nil {
			def.ExpertTeam.Name = p.ResourceName
		} else {
			def.Experts[0].Name = p.ResourceName
		}
	}
	in := &fleet.CapabilityInstallRequest{Definition: def}
	for i := range in.Definition.Experts {
		e := &in.Definition.Experts[i]
		e.CustomEnv = p.CustomEnv
		in.Bindings.Experts = append(in.Bindings.Experts, fleet.CapabilityExpertBinding{ExpertName: e.Name, RuntimeID: p.RuntimeID})
	}
	return in
}
