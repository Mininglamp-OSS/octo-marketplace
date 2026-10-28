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
	AcquireInstallDispatch(context.Context, pluginrepo.Scope, pluginrepo.InstallAttempt, string, time.Time, time.Duration, time.Duration) (bool, error)
	SetInstallDispatchUnknown(context.Context, pluginrepo.Scope, pluginrepo.InstallAttempt, string) error
	FinishInstallDispatch(context.Context, pluginrepo.Scope, pluginrepo.InstallAttempt, string) (bool, error)
	FailInstallDispatch(context.Context, pluginrepo.Scope, pluginrepo.InstallAttempt, string) (bool, error)
	ExpireInstallAttempt(context.Context, pluginrepo.Scope, pluginrepo.InstallAttempt) error
}

func digestInstall(v any) string {
	raw, _ := json.Marshal(v)
	return digestInstallBytes(raw)
}
func digestInstallBytes(raw []byte) string {
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
	// Omitting false keeps default-mode intent HMACs compatible with existing attempts.
	intent := digestInstall(struct {
		Root, Space, Workspace, Runtime, Name string
		WorkspaceConnectors                   bool `json:"workspace_connectors,omitempty"`
	}{id, caller.SpaceID, p.WorkspaceID, p.RuntimeID, p.ResourceName, p.WorkspaceConnectors})
	if attempt != nil && (attempt.PluginID != id || attempt.IntentDigest != intent || attempt.AdapterVersion != SkillInstallAdapterVersion) {
		return nil, ErrConflict
	}
	input, err := s.installationHMAC(keyID, "input", struct {
		Intent string
		Env    map[string]string
	}{intent, p.CustomEnv})
	if err != nil {
		return nil, err
	}
	if attempt == nil {
		plan, err := s.resolvePinnedInstallation(ctx, caller, id, "", p.WorkspaceConnectors)
		if err != nil {
			return nil, err
		}
		if !validPinnedPlanNodeIDs(plan, id) {
			return nil, ErrIntegrity
		}
		if err = validatePinnedInstallMode(plan, p); err != nil {
			return nil, err
		}
		request, err := pinnedPlanRequest(plan, p)
		if err != nil {
			return nil, err
		}
		body, err := fleet.MarshalCapabilityInstallRequest(*request)
		if err != nil {
			return nil, ErrIntegrity
		}
		if len(body) > maxCapabilityBytes {
			return nil, ErrTooLarge
		}
		fingerprint, err := s.installationHMAC(keyID, "request", json.RawMessage(body))
		if err != nil {
			return nil, err
		}
		candidate := pluginrepo.InstallAttempt{
			WorkspaceID: p.WorkspaceID, KeyDigest: key, PluginID: id, RootVersionID: plan.RootVersionID,
			AdapterVersion: SkillInstallAdapterVersion, InputFingerprint: input, RequestFingerprint: fingerprint,
			HMACKeyID: keyID, IntentDigest: intent,
		}
		plan.EligibilityHMAC, err = s.installationEligibilityHMAC(caller, candidate, plan)
		if err != nil {
			return nil, err
		}
		candidate.FrozenPlan, err = json.Marshal(plan)
		if err != nil {
			return nil, ErrIntegrity
		}
		attempt, err = store.ClaimInstallAttempt(ctx, scope(caller), candidate)
		if err != nil {
			return nil, mapStoreError(err)
		}
	}
	if attempt == nil || attempt.PluginID != id || attempt.IntentDigest != intent || attempt.AdapterVersion != SkillInstallAdapterVersion {
		return nil, ErrConflict
	}
	if len(attempt.FrozenPlan) == 0 {
		return nil, ErrIntegrity
	}
	var plan pinnedInstallationPlan
	if json.Unmarshal(attempt.FrozenPlan, &plan) != nil || plan.RootVersionID != attempt.RootVersionID || !validPinnedPlanNodeIDs(&plan, id) {
		return nil, ErrIntegrity
	}
	// Authenticate the persisted eligibility lists before using them as query
	// inputs. Unsigned historical attempts cannot be safely upgraded on retry.
	eligibility, err := s.installationEligibilityHMAC(caller, *attempt, &plan)
	if err != nil {
		return nil, err
	}
	if !equalInstallationHMAC(plan.EligibilityHMAC, eligibility) {
		return nil, ErrIntegrity
	}
	if err = validatePinnedInstallMode(&plan, p); err != nil {
		return nil, err
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
		plugin := current[node]
		if plugin == nil || plugin.IsEmbedded || plugin.Status != 1 || plugin.DeletedAt != nil || plugin.ListingState != model.PluginListingStatePublished || !installAudience(root, plugin) {
			return nil, ErrDependencyHidden
		}
	}
	for _, edge := range plan.Edges {
		source, target := current[edge.Source], current[edge.Target]
		if source == nil || target == nil || !installAudience(source, target) {
			return nil, ErrDependencyHidden
		}
	}
	nodes := make(map[string]bool, len(plan.NodeIDs))
	for _, node := range plan.NodeIDs {
		nodes[node] = true
	}
	for _, connector := range plan.Definition.Connectors {
		if !nodes[connector.MarketMCPID] {
			return nil, ErrIntegrity
		}
		source := current[connector.MarketMCPID]
		if source == nil || source.Type != model.PluginTypeConnector {
			return nil, ErrDependencyHidden
		}
	}
	if plan.Definition.Name != "marketplace:"+id ||
		(plan.ConnectorOnly && root.Type != model.PluginTypeConnector) ||
		(!plan.ConnectorOnly && (root.Type != model.PluginTypeExpert && root.Type != model.PluginTypeExpertTeam)) ||
		(!plan.ConnectorOnly && (root.Type == model.PluginTypeExpertTeam) != (plan.Definition.ExpertTeam != nil)) {
		return nil, ErrIntegrity
	}
	request, err := pinnedPlanRequest(&plan, p)
	if err != nil {
		return nil, err
	}
	body, err := fleet.MarshalCapabilityInstallRequest(*request)
	if err != nil {
		return nil, ErrIntegrity
	}
	fingerprint, err := s.installationHMAC(attempt.HMACKeyID, "request", json.RawMessage(body))
	if err != nil {
		return nil, err
	}
	if !equalInstallationHMAC(attempt.RequestFingerprint, fingerprint) {
		if p.WorkspaceConnectors || plan.ConnectorOnly {
			return nil, ErrConflict
		}
		// A legacy request is accepted only when the old wire bytes match the
		// request fingerprint already authenticated by the frozen-plan HMAC.
		request.LegacySkillWire = true
		body, err = fleet.MarshalCapabilityInstallRequest(*request)
		if err != nil {
			return nil, ErrConflict
		}
		fingerprint, err = s.installationHMAC(attempt.HMACKeyID, "request", json.RawMessage(body))
		if err != nil || !equalInstallationHMAC(attempt.RequestFingerprint, fingerprint) {
			return nil, ErrConflict
		}
	}
	if len(body) > maxCapabilityBytes {
		return nil, ErrTooLarge
	}
	now := s.now()
	if attempt.RetryUntil == nil && attempt.Completed {
		return nil, ErrConflict // historical receipt has no safe resend deadline
	}
	if attempt.RetryUntil != nil && !now.Before(*attempt.RetryUntil) {
		_ = store.ExpireInstallAttempt(context.WithoutCancel(ctx), scope(caller), *attempt)
		return nil, &InstallationAttemptError{Err: ErrAttemptExpired, AttemptID: digestInstall([]string{caller.SpaceID, caller.UID, p.WorkspaceID, key}), RetryUntil: attempt.RetryUntil}
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
		// AcquireInstallDispatch persists the retry deadline with COALESCE so a
		// retry never extends it, but it does not mutate this in-memory attempt.
		// Reload that durable value before any later completion failure can be
		// reported as non-retryable; never derive a fresh deadline here.
		refreshed, readErr := store.GetInstallAttempt(ctx, scope(caller), p.WorkspaceID, key)
		if readErr != nil {
			return nil, mapStoreError(readErr)
		}
		if refreshed == nil || refreshed.RetryUntil == nil {
			return nil, ErrIntegrity
		}
		attempt.RetryUntil = refreshed.RetryUntil
		attempt.LeaseToken = refreshed.LeaseToken
		attempt.LeaseUntil = refreshed.LeaseUntil
		attempt.State = refreshed.State
		attempt.HadUnknown = refreshed.HadUnknown
	}
	// Until local Workspace authorization is wired, Fleet remains the authority
	// even for a known result: submit exactly the frozen same-key request.
	attemptID := digestInstall([]string{caller.SpaceID, caller.UID, p.WorkspaceID, key})
	result, err := s.capabilityInstaller.InstallCapability(ctx, p.Token, caller.SpaceID, p.WorkspaceID, attemptID, *request)
	if err != nil || result == nil {
		if err == nil {
			err = errors.New("missing installation result")
		}
		unknown := true
		if completed && definiteInstallationRejection(err) {
			unknown = false
		}
		if !completed {
			recovery, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			if definiteInstallationRejection(err) {
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
		outcome := &InstallationOutcome{AgentID: attempt.AgentID, SquadID: attempt.TeamID, Replayed: true}
		if p.WorkspaceConnectors {
			outcome.Connectors = result.Connectors
		}
		return outcome, nil
	}
	attempt.AgentID, attempt.TeamID = result.ExpertID, result.ExpertTeamID
	recovery, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	transitioned, err := store.FinishInstallDispatch(recovery, scope(caller), *attempt, token)
	if err != nil || !transitioned {
		return nil, &InstallationAttemptError{Err: errors.New("installation result persistence uncertain"), Unknown: true, AttemptID: digestInstall([]string{caller.SpaceID, caller.UID, p.WorkspaceID, key}), RetryUntil: attempt.RetryUntil}
	}
	s.trackInstall(recovery, id)
	outcome := &InstallationOutcome{AgentID: result.ExpertID, SquadID: result.ExpertTeamID, Replayed: result.ReplayKnown && result.Replayed}
	if p.WorkspaceConnectors {
		outcome.Connectors = result.Connectors
	}
	return outcome, nil
}
func pinnedPlanRequest(plan *pinnedInstallationPlan, p InstallationParams) (*fleet.CapabilityInstallRequest, error) {
	if plan == nil {
		return nil, ErrIntegrity
	}
	// Copy nested slices/pointers before adding per-request runtime secrets.
	raw, err := json.Marshal(plan.Definition)
	if err != nil {
		return nil, ErrIntegrity
	}
	var def fleet.CapabilityDefinition
	if json.Unmarshal(raw, &def) != nil {
		return nil, ErrIntegrity
	}
	for i := range def.Experts {
		if len(def.Experts[i].MCPConfig) == 0 {
			continue
		}
		canonical, err := canonicalJSONObject(def.Experts[i].MCPConfig)
		if err != nil {
			return nil, ErrIntegrity
		}
		def.Experts[i].MCPConfig = canonical
	}
	for i := range def.Connectors {
		canonical, err := canonicalJSONObject(def.Connectors[i].Config)
		if err != nil {
			return nil, ErrIntegrity
		}
		def.Connectors[i].Config = canonical
	}
	if !validPinnedDefinition(def, plan.ConnectorOnly) {
		return nil, ErrIntegrity
	}
	if p.ResourceName != "" {
		if def.ExpertTeam != nil {
			def.ExpertTeam.Name = p.ResourceName
		} else if len(def.Experts) > 0 {
			def.Experts[0].Name = p.ResourceName
		} else if len(def.Connectors) > 0 {
			def.Connectors[0].Name = p.ResourceName
		}
	}
	in := &fleet.CapabilityInstallRequest{Definition: def}
	for i := range in.Definition.Experts {
		e := &in.Definition.Experts[i]
		e.CustomEnv = p.CustomEnv
		in.Bindings.Experts = append(in.Bindings.Experts, fleet.CapabilityExpertBinding{ExpertName: e.Name, RuntimeID: p.RuntimeID})
	}
	return in, nil
}

func validatePinnedInstallMode(plan *pinnedInstallationPlan, p InstallationParams) error {
	if plan == nil {
		return ErrIntegrity
	}
	if plan.ConnectorOnly {
		if !p.WorkspaceConnectors {
			return ErrIntegrity
		}
		if len(p.CustomEnv) != 0 {
			return installationInvalid("custom_env", "unsupported_for_connector")
		}
		return nil
	}
	if p.WorkspaceConnectors && p.RuntimeID == "" {
		return installationInvalid("runtime_id", "invalid_uuid")
	}
	return nil
}

func validPinnedPlanNodeIDs(plan *pinnedInstallationPlan, rootID string) bool {
	if plan == nil || len(plan.NodeIDs) == 0 {
		return false
	}
	if !plan.ConnectorOnly {
		return true
	}
	return rootID != "" && plan.RootVersionID == "" && len(plan.NodeIDs) == 1 && plan.NodeIDs[0] == rootID && len(plan.Edges) == 0
}

// Recovery checks structure before indexing or binding names. The request HMAC
// still verifies frozen content after runtime inputs are added.
func validPinnedDefinition(def fleet.CapabilityDefinition, connectorOnly bool) bool {
	if def.SchemaVersion != "1.0" || len(def.Experts) > 30 {
		return false
	}
	connectors := make(map[string]bool, len(def.Connectors))
	for _, connector := range def.Connectors {
		if !installationName(connector.Name, 128) || connectors[connector.Name] ||
			!installationName(connector.MarketMCPID, 128) {
			return false
		}
		if _, present, err := normalizeCapabilityConnectorMCP(string(connector.Config)); err != nil || !present {
			return false
		}
		connectors[connector.Name] = true
	}
	if connectorOnly {
		return len(def.Experts) == 0 && len(def.Skills) == 0 && def.ExpertTeam == nil && len(def.Connectors) == 1
	}
	if len(def.Experts) == 0 {
		return false
	}
	skills := map[string]bool{}
	for _, skill := range def.Skills {
		if !installationName(skill.Name, 128) || skills[skill.Name] {
			return false
		}
		skills[skill.Name] = true
	}
	experts := make(map[string]bool, len(def.Experts))
	for _, expert := range def.Experts {
		if !installationName(expert.Name, 128) || experts[expert.Name] || expert.Instructions == "" || len(expert.CustomEnv) != 0 {
			return false
		}
		experts[expert.Name] = true
		seenSkills := make(map[string]bool, len(expert.SkillNames))
		for _, name := range expert.SkillNames {
			if !skills[name] || seenSkills[name] {
				return false
			}
			seenSkills[name] = true
		}
		seenConnectors := make(map[string]bool, len(expert.ConnectorNames))
		for _, name := range expert.ConnectorNames {
			if !connectors[name] || seenConnectors[name] {
				return false
			}
			seenConnectors[name] = true
		}
	}
	if def.ExpertTeam == nil {
		return len(def.Experts) == 1
	}
	team := def.ExpertTeam
	if !installationName(team.Name, 128) || len(team.Members) != len(def.Experts) {
		return false
	}
	seen := make(map[string]bool, len(team.Members))
	leaders := 0
	for _, member := range team.Members {
		if !experts[member.ExpertName] || seen[member.ExpertName] || (!member.IsLeader && member.Role == "leader") {
			return false
		}
		seen[member.ExpertName] = true
		if member.IsLeader {
			leaders++
		}
	}
	return leaders == 1
}
