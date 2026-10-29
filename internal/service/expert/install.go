package expert

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/fleet"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/logging"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/service/parse"
	"go.uber.org/zap"
)

// installTimeout bounds source-content reads and the single atomic Fleet
// installation request.
const installTimeout = 5 * time.Minute

// maxSkillFilesPerSkill caps the UTF-8 supporting files extracted from one
// packaged Skill. Oversized or unreadable archives contribute no supporting files.
const maxSkillFilesPerSkill = 50

// maxSkillFilesPerInstall caps supporting files across every Skill/member in
// one install.
const maxSkillFilesPerInstall = 500

// ErrFleetNotConfigured is returned when no Fleet client is wired. The handler
// maps it to UPSTREAM_UNAVAILABLE.
var ErrFleetNotConfigured = errors.New("fleet not configured")

// ErrInstallTooLarge is returned when an install exceeds the aggregate request
// byte budget or the aggregate Skill file limit. The handler maps it to 400.
var ErrInstallTooLarge = errors.New("install exceeds resource limits")

// ErrFleetInstallUnavailable marks a capability request whose result could not
// be determined from Fleet (for example, a transport failure).
var ErrFleetInstallUnavailable = errors.New("fleet capability install unavailable")

// InstallDispatchError wraps a capability install failure that was already
// dispatched to Fleet and carries the idempotency key actually sent, so a
// caller that supplied none can recover a 503 by retrying with that key.
// Unwrap preserves the original classification (transport unavailability,
// *fleet.CapabilityAPIError); handlers only expose the key on 503 responses.
type InstallDispatchError struct {
	IdempotencyKey string
	Err            error
}

func (e *InstallDispatchError) Error() string { return e.Err.Error() }

func (e *InstallDispatchError) Unwrap() error { return e.Err }

// fileBudget caps supporting files across every Skill/member in one install.
type fileBudget struct{ remaining int }

func (b *fileBudget) take() bool {
	if b.remaining <= 0 {
		return false
	}
	b.remaining--
	return true
}

// capabilityBudget tracks the exact encoded size of a capability install
// request while it is assembled. The initial size comes from marshalling a real
// empty request, so root-level fields stay accounted automatically. Every
// charge is taken before the next resource read, so an oversized definition
// never materializes its remaining Skills.
type capabilityBudget struct {
	remaining    int64
	skillCount   int
	expertCount  int
	bindingCount int
}

func newCapabilityBudget(name string) *capabilityBudget {
	initial, _ := json.Marshal(fleet.CapabilityInstallRequest{
		Definition: fleet.CapabilityDefinition{
			SchemaVersion: "1.0",
			Name:          name,
			Experts:       []fleet.CapabilityExpert{},
		},
		Bindings: fleet.CapabilityBindings{Experts: []fleet.CapabilityExpertBinding{}},
	})
	return &capabilityBudget{remaining: fleet.MaxCapabilityRequestBytes - int64(len(initial))}
}

func (b *capabilityBudget) take(n int64) error {
	if n > b.remaining {
		return ErrInstallTooLarge
	}
	b.remaining -= n
	return nil
}

func (b *capabilityBudget) addJSON(value any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return b.take(int64(len(encoded)))
}

// beginSkill charges the optional skills field and its empty array on the first
// element, then one comma per subsequent element.
func (b *capabilityBudget) beginSkill() error {
	if b.skillCount == 0 {
		if err := b.take(int64(len(`,"skills":[]`))); err != nil {
			return err
		}
		b.skillCount = 1
		return nil
	}
	if err := b.take(1); err != nil {
		return err
	}
	b.skillCount++
	return nil
}

// beginExpert charges only the comma for experts after the first; the initial
// request already contains the empty experts array.
func (b *capabilityBudget) beginExpert() error {
	if b.expertCount > 0 {
		if err := b.take(1); err != nil {
			return err
		}
	}
	b.expertCount++
	return nil
}

// beginBinding charges only the comma for bindings after the first; the initial
// request already contains the empty bindings array.
func (b *capabilityBudget) beginBinding() error {
	if b.bindingCount > 0 {
		if err := b.take(1); err != nil {
			return err
		}
	}
	b.bindingCount++
	return nil
}

// addTeam charges the optional expert_team field and its JSON payload.
func (b *capabilityBudget) addTeam(team *fleet.CapabilityExpertTeam) error {
	encoded, err := json.Marshal(team)
	if err != nil {
		return err
	}
	return b.take(int64(len(`,"expert_team":`)) + int64(len(encoded)))
}

// addSkillName charges one skill_names reference: the field and its empty array
// on the first name, then one comma per subsequent name.
func (b *capabilityBudget) addSkillName(first bool, name string) error {
	encoded, _ := json.Marshal(name)
	if first {
		return b.take(int64(len(`,"skill_names":[]`)) + int64(len(encoded)))
	}
	return b.take(int64(len(encoded)) + 1)
}

// addSkillFile charges one supporting file: the files field and its empty array
// on the first file, then one comma per subsequent file.
func (b *capabilityBudget) addSkillFile(first bool, file fleet.CapabilitySkillFile) error {
	encoded, err := json.Marshal(file)
	if err != nil {
		return err
	}
	if first {
		return b.take(int64(len(`,"files":[]`)) + int64(len(encoded)))
	}
	return b.take(int64(len(encoded)) + 1)
}

// FleetProvisioner is the single atomic octo-fleet installation surface used by
// legacy expert and squad endpoints. *fleet.Client satisfies it.
type FleetProvisioner interface {
	InstallCapability(ctx context.Context, token, spaceID, workspaceID, key string, in fleet.CapabilityInstallRequest) (*fleet.CapabilityInstallResult, error)
}

// WithFleet wires the provisioner at construction (router).
func (s *Service) WithFleet(f FleetProvisioner) *Service {
	s.fleet = f
	return s
}

// InstallTracker is the metrics surface used to bump install_count after a
// successful install. *metricssvc.Service satisfies it; the interface lives
// here because the metrics package imports this package for visibility.
type InstallTracker interface {
	TrackInstall(ctx context.Context, resourceType, resourceID string) error
}

// WithMetrics wires the install counter (chainable at construction, like
// WithFleet). A nil / unwired tracker makes trackInstall a no-op.
func (s *Service) WithMetrics(m InstallTracker) *Service {
	s.metrics = m
	return s
}

// trackTimeout bounds the best-effort install counter bump on the response path.
const trackTimeout = 2 * time.Second

// trackInstall bumps the install counter after a successful atomic install.
// It is best-effort and detached from request cancellation.
func (s *Service) trackInstall(ctx context.Context, resourceType, resourceID string) {
	if s.metrics == nil {
		return
	}
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), trackTimeout)
	defer cancel()
	if err := s.metrics.TrackInstall(cctx, resourceType, resourceID); err != nil {
		logging.Warn("install_metric_track_failed",
			zap.String("operation", resourceType+".install.track"),
			zap.String("resource_type", resourceType),
			zap.String("resource_id", resourceID),
			logging.ErrorField(err),
		)
	}
}

// InstallInput carries the per-request install parameters. WorkspaceID and
// RuntimeID come from the request body; SpaceID and Token are the caller's
// forwarded credentials (Token is re-read from the request header by the
// handler because middleware discards it).
type InstallInput struct {
	WorkspaceID string
	RuntimeID   string
	CustomEnv   map[string]string
	SpaceID     string
	Token       string
	// IdempotencyKey is forwarded unchanged; empty preserves generated-key behavior.
	IdempotencyKey string
}

// InstallResult carries the installed Loop agent id.
type InstallResult struct {
	AgentID string
}

// InstallExpert installs the visible expert into the caller's workspace and
// runtime through one Fleet capability transaction. Fleet enforces workspace
// membership, runtime access, and Space scoping using the forwarded token.
func (s *Service) InstallExpert(ctx context.Context, caller Caller, expertID string, in InstallInput) (InstallResult, error) {
	if s.fleet == nil {
		return InstallResult{}, ErrFleetNotConfigured
	}
	if strings.TrimSpace(in.WorkspaceID) == "" || strings.TrimSpace(in.RuntimeID) == "" {
		return InstallResult{}, ErrInvalidRequest
	}

	ctx, cancel := context.WithTimeout(ctx, installTimeout)
	defer cancel()

	m, err := s.loadVisibleExpert(ctx, caller, expertID)
	if err != nil {
		return InstallResult{}, err
	}
	budget := newCapabilityBudget("marketplace:" + m.ID)
	mcpConfig, err := capabilityMCPConfig(m.MCPConfig)
	if err != nil {
		return InstallResult{}, err
	}
	expert := fleet.CapabilityExpert{
		Name: m.Name, Version: nil, MarketExpertID: m.ID, Description: m.Summary, Instructions: m.Instruction,
		CustomEnv: in.CustomEnv, MCPConfig: mcpConfig,
	}
	if err := budget.beginExpert(); err != nil {
		return InstallResult{}, err
	}
	if err := budget.addJSON(expert); err != nil {
		return InstallResult{}, err
	}
	binding := fleet.CapabilityExpertBinding{ExpertName: m.Name, RuntimeID: in.RuntimeID}
	if err := budget.beginBinding(); err != nil {
		return InstallResult{}, err
	}
	if err := budget.addJSON(binding); err != nil {
		return InstallResult{}, err
	}
	skillNames, skills, err := s.capabilitySkills(ctx, m.Skills, m.Summary, &fileBudget{remaining: maxSkillFilesPerInstall}, budget, nil)
	if err != nil {
		return InstallResult{}, err
	}
	expert.SkillNames = skillNames
	request := fleet.CapabilityInstallRequest{
		Definition: fleet.CapabilityDefinition{
			SchemaVersion: "1.0", Name: "marketplace:" + m.ID, Skills: skills,
			Experts: []fleet.CapabilityExpert{expert},
		},
		Bindings: fleet.CapabilityBindings{Experts: []fleet.CapabilityExpertBinding{binding}},
	}
	result, err := s.installCapability(ctx, in, request)
	if err != nil {
		return InstallResult{}, err
	}
	s.trackInstall(ctx, "expert", expertID)
	return InstallResult{AgentID: result.ExpertID}, nil
}

func (s *Service) installCapability(ctx context.Context, in InstallInput, request fleet.CapabilityInstallRequest) (*fleet.CapabilityInstallResult, error) {
	key := in.IdempotencyKey
	if key == "" {
		key = s.idGen()
	}
	result, err := s.fleet.InstallCapability(ctx, in.Token, in.SpaceID, in.WorkspaceID, key, request)
	if err == nil {
		return result, nil
	}
	var apiErr *fleet.CapabilityAPIError
	if errors.As(err, &apiErr) {
		return nil, &InstallDispatchError{IdempotencyKey: key, Err: err}
	}
	return nil, &InstallDispatchError{IdempotencyKey: key, Err: fmt.Errorf("%w: %w", ErrFleetInstallUnavailable, err)}
}

// capabilityMCPConfig translates only the legacy stdio discriminator to the
// command-only stdio shape required by Fleet. Stored source configuration is
// not changed; empty and remote configurations remain untouched.
func capabilityMCPConfig(raw string) (json.RawMessage, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}

	config := make(map[string]json.RawMessage)
	if err := json.Unmarshal([]byte(raw), &config); err != nil {
		var typeErr *json.UnmarshalTypeError
		if errors.As(err, &typeErr) {
			return json.RawMessage(raw), nil
		}
		return nil, err
	}
	serversRaw, ok := config["mcpServers"]
	if !ok {
		return json.RawMessage(raw), nil
	}
	servers := make(map[string]json.RawMessage)
	if err := json.Unmarshal(serversRaw, &servers); err != nil {
		var typeErr *json.UnmarshalTypeError
		if errors.As(err, &typeErr) {
			return json.RawMessage(raw), nil
		}
		return nil, err
	}

	changed := false
	for name, serverRaw := range servers {
		var server map[string]json.RawMessage
		if err := json.Unmarshal(serverRaw, &server); err != nil {
			var typeErr *json.UnmarshalTypeError
			if errors.As(err, &typeErr) {
				return json.RawMessage(raw), nil
			}
			return nil, err
		}
		typeRaw, hasType := server["type"]
		if hasType {
			var serverType string
			if err := json.Unmarshal(typeRaw, &serverType); err != nil {
				var typeErr *json.UnmarshalTypeError
				if errors.As(err, &typeErr) {
					return json.RawMessage(raw), nil
				}
				return nil, err
			}
			if serverType == "stdio" {
				commandRaw, hasCommand := server["command"]
				if hasCommand {
					var command string
					if err := json.Unmarshal(commandRaw, &command); err != nil {
						var typeErr *json.UnmarshalTypeError
						if errors.As(err, &typeErr) {
							return json.RawMessage(raw), nil
						}
						return nil, err
					}
					if command != "" {
						delete(server, "type")
						normalizedServer, err := json.Marshal(server)
						if err != nil {
							return nil, err
						}
						servers[name] = normalizedServer
						changed = true
					}
				}
			}
		}
	}
	if !changed {
		return json.RawMessage(raw), nil
	}
	normalizedServers, err := json.Marshal(servers)
	if err != nil {
		return nil, err
	}
	config["mcpServers"] = normalizedServers
	normalized, err := json.Marshal(config)
	if err != nil {
		return nil, err
	}
	return normalized, nil
}

func (s *Service) capabilitySkills(ctx context.Context, refs []model.SkillRef, description string, budget *fileBudget, request *capabilityBudget, seen map[string]struct{}) ([]string, []fleet.CapabilitySkill, error) {
	names := make([]string, 0, len(refs))
	skills := make([]fleet.CapabilitySkill, 0, len(refs))
	for i := range refs {
		ref := refs[i]
		if ref.ObjectKey == "" && ref.Markdown == "" {
			continue
		}
		if err := request.addSkillName(len(names) == 0, ref.Name); err != nil {
			return nil, nil, err
		}
		if seen != nil {
			if _, exists := seen[ref.Name]; exists {
				names = append(names, ref.Name)
				continue
			}
		}
		if err := request.beginSkill(); err != nil {
			return nil, nil, err
		}
		content, err := s.readSkillContent(ctx, refs, i)
		if err != nil {
			return nil, nil, err
		}
		skill := fleet.CapabilitySkill{
			Name: ref.Name, Version: nil, Description: description, Content: content,
			Config: map[string]any{},
		}
		if err := request.addJSON(skill); err != nil {
			return nil, nil, err
		}
		files, err := s.capabilitySkillFiles(ctx, ref, budget, request)
		if err != nil {
			return nil, nil, err
		}
		skill.Files = files
		skills = append(skills, skill)
		names = append(names, ref.Name)
		if seen != nil {
			seen[ref.Name] = struct{}{}
		}
	}
	return names, skills, nil
}

func (s *Service) capabilitySkillFiles(ctx context.Context, ref model.SkillRef, budget *fileBudget, request *capabilityBudget) ([]fleet.CapabilitySkillFile, error) {
	if ref.Markdown != "" {
		files := make([]fleet.CapabilitySkillFile, 0, len(ref.SupportingFiles))
		for _, file := range ref.SupportingFiles {
			if !budget.take() {
				return nil, ErrInstallTooLarge
			}
			capabilityFile := fleet.CapabilitySkillFile{
				Path: file.Path, Source: fleet.CapabilityInlineTextSource{Type: "inline_text", Content: file.Content},
			}
			if err := request.addSkillFile(len(files) == 0, capabilityFile); err != nil {
				return nil, err
			}
			files = append(files, capabilityFile)
		}
		return files, nil
	}
	if ref.ZipObjectKey == "" || s.store == nil {
		return nil, nil
	}
	tmpPath, _, cleanup, err := s.downloadToTempFile(ctx, ref.ZipObjectKey)
	if err != nil {
		return nil, nil
	}
	defer cleanup()

	extracted, _, code, _ := parse.ExtractSkillFiles(tmpPath, maxSkillPackageBytes, maxSkillFilesPerSkill)
	if code != "" {
		return nil, nil
	}
	files := make([]fleet.CapabilitySkillFile, 0, len(extracted))
	for _, file := range extracted {
		if !budget.take() {
			return nil, ErrInstallTooLarge
		}
		capabilityFile := fleet.CapabilitySkillFile{
			Path: file.Path, Source: fleet.CapabilityInlineTextSource{Type: "inline_text", Content: file.Content},
		}
		if err := request.addSkillFile(len(files) == 0, capabilityFile); err != nil {
			return nil, err
		}
		files = append(files, capabilityFile)
	}
	return files, nil
}
