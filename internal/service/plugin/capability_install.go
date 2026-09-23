package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/fleet"
)

type CapabilityInstaller interface {
	CapabilityInstallEnabled() bool
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

// Field and Reason are fixed, developer-owned labels; never echo environment
// values, plugin instructions, object keys, or credentials through an error.
type InstallationValidationError struct{ Field, Reason string }

func (e *InstallationValidationError) Error() string { return "invalid plugin installation" }
func installationInvalid(field, reason string) error {
	return &InstallationValidationError{Field: field, Reason: reason}
}

var runtimeUUID = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

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
	if p.ResourceName != "" && !installationName(p.ResourceName, 200) {
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
	if s.capabilityInstaller == nil || !s.capabilityInstaller.CapabilityInstallEnabled() {
		return nil, fleet.ErrCapabilityInstallDisabled
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
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
		return nil, err
	}
	if result == nil {
		return nil, errors.New("capability installation returned no result")
	}
	if !result.Replayed {
		s.trackInstall(ctx, pluginID)
	}
	return &InstallationOutcome{AgentID: result.ExpertID, SquadID: result.ExpertTeamID, Replayed: result.Replayed}, nil
}
