package plugin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/fleet"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	pluginrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/plugin"
)

// Fleet retains receipts for 24 hours. A local record never licenses resubmission
// after that window: an uncertain call might otherwise create a second resource.
const installReceiptWindow = 24 * time.Hour

type InstallationAttemptStore interface {
	GetInstallAttempt(context.Context, pluginrepo.Scope, string, string) (*pluginrepo.InstallAttempt, error)
	ClaimInstallAttempt(context.Context, pluginrepo.Scope, pluginrepo.InstallAttempt) (*pluginrepo.InstallAttempt, error)
	CompleteInstallAttempt(context.Context, pluginrepo.Scope, pluginrepo.InstallAttempt) error
	AcquireInstallDispatch(context.Context, pluginrepo.Scope, pluginrepo.InstallAttempt, string, time.Time, time.Duration, time.Duration) (bool, error)
	SetInstallDispatchUnknown(context.Context, pluginrepo.Scope, pluginrepo.InstallAttempt, string) error
	FinishInstallDispatch(context.Context, pluginrepo.Scope, pluginrepo.InstallAttempt, string) (bool, error)
	FailInstallDispatch(context.Context, pluginrepo.Scope, pluginrepo.InstallAttempt, string) (bool, error)
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
	attempt, err := store.GetInstallAttempt(ctx, scope(caller), p.WorkspaceID, key)
	if err != nil {
		return nil, mapStoreError(err)
	}
	keyID := s.installHMACKeyID
	if attempt != nil {
		keyID = attempt.HMACKeyID
	}
	intent := digestInstall(struct{ Root, Space, Workspace, Runtime, Name string }{id, caller.SpaceID, p.WorkspaceID, p.RuntimeID, p.ResourceName})
	input, err := s.installationHMAC(keyID, "input", struct {
		Intent string
		Env    map[string]string
	}{intent, p.CustomEnv})
	if err != nil {
		return nil, err
	}
	if attempt == nil {
		plan, err := s.resolvePinnedInstallation(ctx, caller, id, "")
		if err != nil {
			return nil, err
		}
		frozen, err := json.Marshal(plan)
		if err != nil {
			return nil, ErrIntegrity
		}
		request := pinnedPlanRequest(plan, p)
		fingerprint, err := s.installationHMAC(keyID, "request", request)
		if err != nil {
			return nil, err
		}
		attempt, err = store.ClaimInstallAttempt(ctx, scope(caller), pluginrepo.InstallAttempt{WorkspaceID: p.WorkspaceID, KeyDigest: key, PluginID: id, RootVersionID: plan.RootVersionID, AdapterVersion: SkillInstallAdapterVersion, InputFingerprint: input, RequestFingerprint: fingerprint, FrozenPlan: frozen, HMACKeyID: keyID, IntentDigest: intent})
		if err != nil {
			return nil, mapStoreError(err)
		}
	}
	if attempt == nil || attempt.PluginID != id || attempt.IntentDigest != intent || attempt.AdapterVersion != SkillInstallAdapterVersion {
		return nil, ErrConflict
	}
	// A concurrent contender may have won under the previous active HMAC key.
	input, err = s.installationHMAC(attempt.HMACKeyID, "input", struct {
		Intent string
		Env    map[string]string
	}{intent, p.CustomEnv})
	if err != nil {
		return nil, err
	}
	if !equalInstallationHMAC(attempt.InputFingerprint, input) {
		return nil, ErrConflict
	}
	if len(attempt.FrozenPlan) == 0 {
		return nil, ErrIntegrity
	}
	var plan pinnedInstallationPlan
	if json.Unmarshal(attempt.FrozenPlan, &plan) != nil || plan.RootVersionID != attempt.RootVersionID || len(plan.NodeIDs) == 0 {
		return nil, ErrIntegrity
	}
	// Governance is current; content and selected versions remain frozen.
	read, ok := s.repo.(InstallationReadStore)
	if !ok {
		return nil, ErrVersionBaselineUnavailable
	}
	current, err := read.GetInstallPlugins(ctx, scope(caller), plan.NodeIDs)
	if err != nil {
		return nil, mapStoreError(err)
	}
	root := current[id]
	if root == nil {
		return nil, ErrNotFound
	}
	for _, node := range plan.NodeIDs {
		p := current[node]
		if p == nil || p.IsEmbedded || p.Status != 1 || p.DeletedAt != nil || p.ListingState != model.PluginListingStatePublished || !installAudience(root, p) {
			return nil, ErrDependencyHidden
		}
	}
	for _, edge := range plan.Edges {
		source, target := current[edge.Source], current[edge.Target]
		if source == nil || target == nil || !installAudience(source, target) {
			return nil, ErrDependencyHidden
		}
	}
	request := pinnedPlanRequest(&plan, p)
	body, err := json.Marshal(request)
	if err != nil {
		return nil, ErrIntegrity
	}
	if len(body) > maxCapabilityBytes {
		return nil, ErrTooLarge
	}
	fingerprint, err := s.installationHMAC(attempt.HMACKeyID, "request", request)
	if err != nil {
		return nil, err
	}
	if !equalInstallationHMAC(attempt.RequestFingerprint, fingerprint) {
		return nil, ErrIntegrity
	}
	now := s.now()
	if attempt.RetryUntil != nil && !now.Before(*attempt.RetryUntil) {
		return nil, ErrConflict
	}
	completed := attempt.Completed
	token := s.id()
	if !completed {
		acquired, err := store.AcquireInstallDispatch(ctx, scope(caller), *attempt, token, now, 3*time.Minute, installReceiptWindow-time.Minute)
		if err != nil {
			return nil, mapStoreError(err)
		}
		if !acquired {
			return nil, ErrConflict
		}
	}
	// Until local Workspace authorization is wired, Fleet remains the authority
	// even for a known result: submit exactly the frozen same-key request.
	result, err := s.capabilityInstaller.InstallCapability(ctx, p.Token, caller.SpaceID, p.WorkspaceID, p.IdempotencyKey, *request)
	if err != nil || result == nil {
		if err == nil {
			err = errors.New("missing installation result")
		}
		unknown := true
		if !completed {
			recovery, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			var upstream *fleet.CapabilityAPIError
			if errors.As(err, &upstream) && (upstream.Status == 400 || upstream.Status == 401 || upstream.Status == 403 || upstream.Status == 404 || upstream.Status == 413 || upstream.Status == 415 || upstream.Status == 422) {
				rejected, writeErr := store.FailInstallDispatch(recovery, scope(caller), *attempt, token)
				unknown = writeErr != nil || !rejected
			}
			if unknown {
				_ = store.SetInstallDispatchUnknown(recovery, scope(caller), *attempt, token)
			}
			cancel()
		}
		cutoff := attempt.RetryUntil
		if cutoff == nil {
			v := now.Add(installReceiptWindow - time.Minute)
			cutoff = &v
		}
		return nil, &InstallationAttemptError{Err: err, Unknown: unknown, AttemptID: digestInstall([]string{caller.SpaceID, caller.UID, p.WorkspaceID, key}), RetryUntil: cutoff}
	}
	if completed {
		if attempt.AgentID != result.ExpertID || attempt.TeamID != result.ExpertTeamID {
			return nil, ErrIntegrity
		}
		return &InstallationOutcome{AgentID: attempt.AgentID, SquadID: attempt.TeamID, Replayed: true}, nil
	}
	attempt.AgentID, attempt.TeamID = result.ExpertID, result.ExpertTeamID
	recovery, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	transitioned, err := store.FinishInstallDispatch(recovery, scope(caller), *attempt, token)
	if err != nil || !transitioned {
		return nil, &InstallationAttemptError{Err: errors.New("installation result persistence uncertain"), Unknown: true, AttemptID: digestInstall([]string{caller.SpaceID, caller.UID, p.WorkspaceID, key}), RetryUntil: attempt.RetryUntil}
	}
	s.trackInstall(recovery, id)
	return &InstallationOutcome{AgentID: result.ExpertID, SquadID: result.ExpertTeamID, Replayed: result.ReplayKnown && result.Replayed}, nil
}
func pinnedPlanRequest(plan *pinnedInstallationPlan, p InstallationParams) *fleet.CapabilityInstallRequest {
	// Copy nested slices/pointers before adding per-request runtime secrets.
	raw, _ := json.Marshal(plan.Definition)
	var def fleet.CapabilityDefinition
	_ = json.Unmarshal(raw, &def)
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
