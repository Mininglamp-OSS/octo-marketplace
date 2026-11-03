package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/fleet"
	expertsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/expert"
)

type CapabilityInstaller interface {
	InstallCapability(context.Context, string, string, string, string, fleet.CapabilityInstallRequest) (*fleet.CapabilityInstallResult, error)
	// HasCapabilityInstallationReceipt only reads whether an existing receipt
	// for this Workspace, caller and key is still valid.
	HasCapabilityInstallationReceipt(context.Context, string, string, string, string) (bool, error)
	// ReplayCapability rereads the stored result for matching request bytes
	// without creating resources; (nil, nil) means the receipt is gone.
	ReplayCapability(context.Context, string, string, string, string, fleet.CapabilityInstallRequest) (*fleet.CapabilityInstallResult, error)
}

func (s *Service) WithCapabilityInstaller(installer CapabilityInstaller) *Service {
	s.capabilityInstaller = installer
	return s
}

type InstallationParams struct {
	WorkspaceID                  string
	RuntimeID                    string
	ResourceName                 string
	CustomEnv                    map[string]string
	IdempotencyKey               string
	Token                        string
	WorkspaceConnectors          bool
	WorkspaceConnectorsDefaulted bool
	// IdempotencyKeyGenerated marks the legacy adapter's keyless call, whose
	// key was minted locally. Such a key cannot name a pre-existing receipt.
	IdempotencyKeyGenerated bool
	// The original installation route accepts expert and expert-team roots only.
	expertRootsOnly bool
}

type InstallationOutcome struct {
	AgentID    string
	SquadID    string
	Replayed   bool
	Connectors []fleet.CapabilityConnectorResult
}

// InstallationAttemptError marks the boundary after the Fleet adapter was
// invoked. Unknown errors here may follow a commit and require same-key retry.
// Never log the wrapped error verbatim: an upstream may echo submitted secrets.
type InstallationAttemptError struct {
	Err        error
	Unknown    bool
	AttemptID  string
	RetryUntil *time.Time
}

func (e *InstallationAttemptError) Error() string { return "capability installation attempt failed" }
func (e *InstallationAttemptError) Unwrap() error { return e.Err }

// Field and Reason are fixed, developer-owned labels; never echo environment
// values, plugin instructions, object keys, or credentials through an error.
type InstallationValidationError struct{ Field, Reason string }

func (e *InstallationValidationError) Error() string { return "invalid plugin installation" }
func installationInvalid(field, reason string) error {
	return &InstallationValidationError{Field: field, Reason: reason}
}

var runtimeUUID = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

const maxInstallationNameRunes = 128

func NormalizeInstallationParams(p InstallationParams) (InstallationParams, error) {
	return normalizeInstallationParams(p, true)
}

func normalizeInstallationParams(p InstallationParams, strictRuntime bool) (InstallationParams, error) {
	p.WorkspaceID = strings.TrimSpace(p.WorkspaceID)
	p.RuntimeID = strings.TrimSpace(p.RuntimeID)
	p.ResourceName = strings.TrimSpace(p.ResourceName)
	if p.WorkspaceID == "" || len(p.WorkspaceID) > 200 || !printableHeader(p.WorkspaceID) {
		return p, installationInvalid("X-Workspace-ID", "required")
	}
	if (strictRuntime && !runtimeUUID.MatchString(p.RuntimeID) && !(p.WorkspaceConnectors && p.RuntimeID == "")) || (!strictRuntime && (p.RuntimeID == "" || len(p.RuntimeID) > 200 || !printableHeader(p.RuntimeID))) {
		return p, installationInvalid("runtime_id", "invalid_uuid")
	}
	if len(p.IdempotencyKey) < 1 || len(p.IdempotencyKey) > 200 || !printableHeader(p.IdempotencyKey) {
		return p, installationInvalid("Idempotency-Key", "invalid")
	}
	if p.ResourceName != "" && !installationName(p.ResourceName, maxInstallationNameRunes) {
		return p, installationInvalid("resource_name", "invalid")
	}
	if len(p.CustomEnv) > 100 {
		return p, installationInvalid("custom_env", "too_many_keys")
	}
	var total int
	for key, value := range p.CustomEnv {
		if len(key) > 128 || !envName.MatchString(key) || len(value) > 32<<10 || !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
			return p, installationInvalid("custom_env", "invalid_entry")
		}
		total += len(key) + len(value)
	}
	if total > 256<<10 {
		return p, installationInvalid("custom_env", "too_large")
	}
	return p, nil
}

func printableHeader(value string) bool {
	for _, r := range value {
		if r < 33 || r > 126 {
			return false
		}
	}
	return true
}

func installationName(value string, max int) bool {
	if value == "" || !utf8.ValidString(value) || utf8.RuneCountInString(value) > max {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// CreateInstallation validates the new wire contract. All fallible expansion
// completes before the one Fleet mutation; Fleet owns the resource transaction.
func (s *Service) CreateInstallation(ctx context.Context, caller Caller, pluginID string, p InstallationParams) (*InstallationOutcome, error) {
	if caller.BotUID != "" {
		return nil, ErrDependencyHidden
	}
	normalized, err := NormalizeInstallationParams(p)
	if err != nil {
		return nil, err
	}
	return s.createInstallation(ctx, caller, pluginID, normalized)
}

// Both adapters enter here; feature flags never select an executor per URL.
func (s *Service) createInstallation(ctx context.Context, caller Caller, pluginID string, p InstallationParams) (*InstallationOutcome, error) {
	if validateCaller(caller) != nil {
		return nil, ErrInvalidRequest
	}
	if s.capabilityInstaller == nil {
		return nil, expertsvc.ErrFleetNotConfigured
	}
	ctx, cancel := context.WithTimeout(ctx, fleet.CapabilityInstallTimeout)
	defer cancel()
	if s.pinnedInstallation {
		return s.createPinnedInstallation(ctx, caller, pluginID, p)
	}
	return s.createCurrentInstallation(ctx, caller, pluginID, p)
}

// createCurrentInstallation is the non-pinned pipeline. An upgrade can leave a
// receipt for an operation that was submitted before connector mode existed, so
// a selector-omitted caller-keyed request is probed read-only before any write.
func (s *Service) createCurrentInstallation(ctx context.Context, caller Caller, pluginID string, p InstallationParams) (*InstallationOutcome, error) {
	if p.IdempotencyKeyGenerated || !p.WorkspaceConnectorsDefaulted {
		return s.installCurrentOperation(ctx, caller, pluginID, p)
	}
	hasReceipt, err := s.capabilityInstaller.HasCapabilityInstallationReceipt(ctx, p.Token, caller.SpaceID, p.WorkspaceID, p.IdempotencyKey)
	if err != nil {
		// A failed presence read never authorizes an installation.
		return nil, &InstallationAttemptError{Err: err, Unknown: !definiteInstallationRejection(err)}
	}
	if !hasReceipt {
		return s.installDefaultOperation(ctx, caller, pluginID, p)
	}
	root, err := s.resolveInstallDetail(ctx, caller, pluginID)
	if err != nil {
		return nil, err
	}
	result, err := s.replayDefaultReceipt(ctx, caller, p, root, newCapabilityBuildCache())
	if err != nil {
		return nil, err
	}
	s.trackInstall(ctx, pluginID)
	return currentInstallationOutcome(result, p), nil
}

// installCurrentOperation is the explicit-mode or generated-key path: exactly
// one Fleet mutation, with the same capacity and error contract as before.
func (s *Service) installCurrentOperation(ctx context.Context, caller Caller, pluginID string, p InstallationParams) (*InstallationOutcome, error) {
	root, err := s.resolveInstallDetail(ctx, caller, pluginID)
	if err != nil {
		return nil, err
	}
	in, err := s.capabilityCandidate(ctx, caller, root, p, newCapabilityBuildCache())
	if err != nil {
		return nil, err
	}
	result, err := s.capabilityInstaller.InstallCapability(ctx, p.Token, caller.SpaceID, p.WorkspaceID, p.IdempotencyKey, *in)
	if err != nil {
		return nil, &InstallationAttemptError{Err: err, Unknown: !definiteInstallationRejection(err)}
	}
	if result == nil {
		return nil, &InstallationAttemptError{Err: errors.New("capability installation returned no result"), Unknown: true}
	}
	s.trackInstall(ctx, pluginID)
	return currentInstallationOutcome(result, p), nil
}

// installDefaultOperation writes the connector default once, unless a
// concurrent same-key operation won the presence race first.
func (s *Service) installDefaultOperation(ctx context.Context, caller Caller, pluginID string, p InstallationParams) (*InstallationOutcome, error) {
	root, err := s.resolveInstallDetail(ctx, caller, pluginID)
	if err != nil {
		return nil, err
	}
	cache := newCapabilityBuildCache()
	in, err := s.capabilityCandidate(ctx, caller, root, p, cache)
	if err != nil {
		return nil, err
	}
	result, err := s.capabilityInstaller.InstallCapability(ctx, p.Token, caller.SpaceID, p.WorkspaceID, p.IdempotencyKey, *in)
	if err != nil || result == nil {
		if err == nil {
			err = errors.New("capability installation returned no result")
		}
		if capabilityReceiptMismatch(err) {
			// Another request committed between the presence read and this
			// send. Recover that receipt read-only; never resend either mode.
			legacyParams := p
			legacyParams.WorkspaceConnectors = false
			legacy, buildErr := s.capabilityCandidate(ctx, caller, root, legacyParams, cache)
			if buildErr == nil {
				recovered, replayErr := s.replayInstallationReceipts(ctx, caller, p, []*fleet.CapabilityInstallRequest{legacy})
				if replayErr == nil {
					s.trackInstall(ctx, pluginID)
					return currentInstallationOutcome(recovered, p), nil
				}
				var attempt *InstallationAttemptError
				if errors.As(replayErr, &attempt) && attempt.Unknown {
					return nil, replayErr
				}
			}
		}
		return nil, &InstallationAttemptError{Err: err, Unknown: !definiteInstallationRejection(err)}
	}
	s.trackInstall(ctx, pluginID)
	return currentInstallationOutcome(result, p), nil
}

// replayDefaultReceipt tries the pre-upgrade MCP wire first. The current
// connector wire is built only after that candidate is unrepresentable or its
// fingerprint is explicitly rejected by Fleet.
func (s *Service) replayDefaultReceipt(ctx context.Context, caller Caller, p InstallationParams, root *Detail, cache *capabilityBuildCache) (*fleet.CapabilityInstallResult, error) {
	legacyParams := p
	legacyParams.WorkspaceConnectors = false
	legacy, legacyErr := s.capabilityCandidate(ctx, caller, root, legacyParams, cache)
	if legacyErr == nil {
		result, replayErr := s.replayInstallationReceipts(ctx, caller, p, []*fleet.CapabilityInstallRequest{legacy})
		if replayErr == nil {
			return result, nil
		}
		if !capabilityReceiptMismatch(replayErr) {
			return nil, replayErr
		}
	} else if !errors.Is(legacyErr, ErrTooLarge) {
		var invalid *InstallationValidationError
		if !errors.As(legacyErr, &invalid) ||
			(invalid.Field != "definition.experts.mcp_config" &&
				!(invalid.Field == "plugin_id" && invalid.Reason == "expert_or_expert_team_required")) {
			return nil, legacyErr
		}
	}

	current, err := s.capabilityCandidate(ctx, caller, root, p, cache)
	if err != nil {
		return nil, err
	}
	return s.replayInstallationReceipts(ctx, caller, p, []*fleet.CapabilityInstallRequest{current})
}

// capabilityCandidate builds, serializes, and bounds one complete wire candidate
// before any Fleet mutation is dispatched.
func (s *Service) capabilityCandidate(ctx context.Context, caller Caller, root *Detail, p InstallationParams, cache *capabilityBuildCache) (*fleet.CapabilityInstallRequest, error) {
	in, err := s.buildCapabilityInstallFrom(ctx, caller, root, p, cache)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(in)
	if err != nil {
		return nil, installationInvalid("definition", "invalid_json")
	}
	if len(body) > fleet.MaxCapabilityRequestBytes {
		return nil, installationRequestCapacityError(int64(len(body)), *in)
	}
	return in, nil
}

// replayInstallationReceipts only reads Fleet receipts. A missing receipt or a
// fingerprint mismatch is a conflict; transport failures stay uncertain.
func (s *Service) replayInstallationReceipts(ctx context.Context, caller Caller, p InstallationParams, candidates []*fleet.CapabilityInstallRequest) (*fleet.CapabilityInstallResult, error) {
	var mismatch error
	for _, candidate := range candidates {
		if candidate == nil {
			continue
		}
		result, err := s.capabilityInstaller.ReplayCapability(ctx, p.Token, caller.SpaceID, p.WorkspaceID, p.IdempotencyKey, *candidate)
		if err == nil {
			if result == nil {
				// The receipt expired between the presence read and the replay.
				return nil, ErrConflict
			}
			return result, nil
		}
		if capabilityReceiptMismatch(err) {
			mismatch = err
			continue
		}
		return nil, &InstallationAttemptError{Err: err, Unknown: !definiteInstallationRejection(err)}
	}
	if mismatch != nil {
		return nil, &InstallationAttemptError{Err: mismatch, Unknown: false}
	}
	return nil, ErrConflict
}

func currentInstallationOutcome(result *fleet.CapabilityInstallResult, p InstallationParams) *InstallationOutcome {
	outcome := &InstallationOutcome{AgentID: result.ExpertID, SquadID: result.ExpertTeamID, Replayed: result.ReplayKnown && result.Replayed}
	if p.WorkspaceConnectors {
		outcome.Connectors = result.Connectors
	}
	return outcome
}

// capabilityReceiptMismatch recognizes only Fleet's explicit same-key
// fingerprint conflict; generic/in-progress conflicts stay uncertain.
func capabilityReceiptMismatch(err error) bool {
	var upstream *fleet.CapabilityAPIError
	if !errors.As(err, &upstream) || upstream.Status != http.StatusConflict {
		return false
	}
	return upstream.IdempotencyKeyReused || upstream.Code == "IDEMPOTENCY_KEY_REUSED"
}

// Only recognized rejections prove that this send did not install anything.
// Generic/in-progress 409s, transport errors and 5xx remain uncertain; never use
// an upstream display message to infer whether a mutation committed.
func definiteInstallationRejection(err error) bool {
	var upstream *fleet.CapabilityAPIError
	if !errors.As(err, &upstream) {
		return false
	}
	switch upstream.Status {
	case 400, 401, 403, 404, 413, 415, 422, 429:
		return true
	case 409:
		return upstream.Code == "DUPLICATE" || upstream.Code == "IDEMPOTENCY_KEY_REUSED" ||
			(upstream.Code == "CONFLICT" && upstream.ConflictResource != "")
	default:
		return false
	}
}
