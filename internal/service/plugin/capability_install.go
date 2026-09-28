package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/fleet"
	expertsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/expert"
)

type CapabilityInstaller interface {
	InstallCapability(context.Context, string, string, string, string, fleet.CapabilityInstallRequest) (*fleet.CapabilityInstallResult, error)
}

func (s *Service) WithCapabilityInstaller(installer CapabilityInstaller) *Service {
	s.capabilityInstaller = installer
	return s
}

type InstallationParams struct {
	WorkspaceID    string
	RuntimeID      string
	ResourceName   string
	CustomEnv      map[string]string
	IdempotencyKey string
	Token          string
}

type InstallationOutcome struct {
	AgentID  string
	SquadID  string
	Replayed bool
}

// InstallationAttemptError marks the boundary after the Fleet adapter was
// invoked. Unknown errors here may follow a commit and require same-key retry.
// Never log the wrapped error verbatim: an upstream may echo submitted secrets.
type InstallationAttemptError struct{ Err error }

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
	p.WorkspaceID = strings.TrimSpace(p.WorkspaceID)
	p.RuntimeID = strings.TrimSpace(p.RuntimeID)
	p.ResourceName = strings.TrimSpace(p.ResourceName)
	if p.WorkspaceID == "" || len(p.WorkspaceID) > 200 || !printableHeader(p.WorkspaceID) {
		return p, installationInvalid("X-Workspace-ID", "required")
	}
	if !runtimeUUID.MatchString(p.RuntimeID) {
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

// CreateInstallation leaves legacy Install untouched. All fallible expansion
// completes before the one Fleet mutation; Fleet owns the resource transaction.
func (s *Service) CreateInstallation(ctx context.Context, caller Caller, pluginID string, p InstallationParams) (*InstallationOutcome, error) {
	if validateCaller(caller) != nil {
		return nil, ErrInvalidRequest
	}
	if caller.BotUID != "" {
		return nil, ErrDependencyHidden
	}
	var err error
	if p, err = NormalizeInstallationParams(p); err != nil {
		return nil, err
	}
	if s.capabilityInstaller == nil {
		return nil, expertsvc.ErrFleetNotConfigured
	}
	ctx, cancel := context.WithTimeout(ctx, fleet.CapabilityInstallTimeout)
	defer cancel()
	in, err := s.buildCapabilityInstall(ctx, caller, pluginID, p)
	if err != nil {
		return nil, err
	}
	// Include JSON escaping/structure and replicated environment bindings in the
	// outgoing limit, not just the sum of unescaped text assets.
	body, err := json.Marshal(in)
	if err != nil {
		return nil, installationInvalid("definition", "invalid_json")
	}
	if len(body) > maxCapabilityBytes {
		return nil, ErrTooLarge
	}
	result, err := s.capabilityInstaller.InstallCapability(ctx, p.Token, caller.SpaceID, p.WorkspaceID, p.IdempotencyKey, *in)
	if err != nil {
		return nil, &InstallationAttemptError{Err: err}
	}
	if result == nil {
		return nil, &InstallationAttemptError{Err: errors.New("capability installation returned no result")}
	}
	// Match legacy /plugins/install: count successful API invocations, including
	// receipt replays. This is not a count of uniquely created Fleet resources.
	s.trackInstall(ctx, pluginID)
	return &InstallationOutcome{AgentID: result.ExpertID, SquadID: result.ExpertTeamID, Replayed: result.ReplayKnown && result.Replayed}, nil
}
