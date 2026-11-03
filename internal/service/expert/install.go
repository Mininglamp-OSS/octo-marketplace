package expert

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/fleet"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/logging"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/service/parse"
	"go.uber.org/zap"
)

// installTimeout bounds source-content reads and the single atomic Fleet
// installation request.
const installTimeout = 5 * time.Minute

// ErrFleetNotConfigured is returned when no Fleet client is wired. The handler
// maps it to UPSTREAM_UNAVAILABLE.
var ErrFleetNotConfigured = errors.New("fleet not configured")

// ErrInstallTooLarge marks an installation above the complete JSON byte budget.
var ErrInstallTooLarge = errors.New("install exceeds resource limits")

// ErrFleetInstallUnavailable marks a capability request whose result could not
// be determined from Fleet (for example, a transport failure).
var ErrFleetInstallUnavailable = errors.New("fleet capability install unavailable")

// ErrInstallReceiptUnavailable marks a same-key retry whose Fleet receipt
// disappeared after the read-only presence check. The original operation's
// result is no longer recoverable; start no new mutation from that read.
var ErrInstallReceiptUnavailable = errors.New("fleet installation receipt unavailable")

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

// capabilityBudget tracks the exact encoded size of a capability install
// request while it is assembled. The initial size comes from marshalling a real
// empty request, so root-level fields stay accounted automatically. Every
// charge is taken before the next resource read, so an oversized definition
// never materializes its remaining Skills.
type capabilityBudget struct {
	remaining      int64
	skillCount     int
	expertCount    int
	connectorCount int
	bindingCount   int
	skill          string
	path           string
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
		return &installCapacityError{actualBytes: int64(fleet.MaxCapabilityRequestBytes) - b.remaining + n, skill: b.skill, path: b.path}
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

// beginConnector charges the optional connectors field and its empty array on
// the first connector, then one comma per subsequent connector.
func (b *capabilityBudget) beginConnector() error {
	if b.connectorCount == 0 {
		if err := b.take(int64(len(`,"connectors":[]`))); err != nil {
			return err
		}
		b.connectorCount = 1
		return nil
	}
	if err := b.take(1); err != nil {
		return err
	}
	b.connectorCount++
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
	// HasCapabilityInstallationReceipt reads whether a receipt for this
	// Workspace, caller and key still exists. It never creates one.
	HasCapabilityInstallationReceipt(ctx context.Context, token, spaceID, workspaceID, key string) (bool, error)
	// ReplayCapability rereads a matching stored result without installing;
	// (nil, nil) means no valid receipt remains for the key.
	ReplayCapability(ctx context.Context, token, spaceID, workspaceID, key string, in fleet.CapabilityInstallRequest) (*fleet.CapabilityInstallResult, error)
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
	result, err := s.installCapability(ctx, in,
		func() (*fleet.CapabilityInstallRequest, error) {
			return s.buildExpertInstallation(ctx, m, in, true)
		},
		func(current *fleet.CapabilityInstallRequest) (*fleet.CapabilityInstallRequest, error) {
			if current != nil {
				return legacyExpertInstallRequest(*current, m.MCPConfig), nil
			}
			return s.buildExpertInstallation(ctx, m, in, false)
		})
	if err != nil {
		return InstallResult{}, err
	}
	s.trackInstall(ctx, "expert", expertID)
	return InstallResult{AgentID: result.ExpertID}, nil
}

// buildExpertInstallation bounds each wire independently before dispatch.
// Legacy MCP is assembled only for read-only receipt recovery.
func (s *Service) buildExpertInstallation(ctx context.Context, m *model.Expert, in InstallInput, workspaceConnectors bool) (*fleet.CapabilityInstallRequest, error) {
	budget := newCapabilityBudget("marketplace:" + m.ID)
	expert := fleet.CapabilityExpert{
		Name: m.Name, Version: nil, MarketExpertID: m.ID, Description: m.Summary, Instructions: m.Instruction,
		CustomEnv: in.CustomEnv,
	}
	var connectors []fleet.CapabilityConnector
	if workspaceConnectors {
		connector, hasConnector, err := capabilityMCPConnector(m.MCPConfig, m.Name)
		if err != nil {
			return nil, err
		}
		if hasConnector {
			expert.ConnectorNames = []string{connector.Name}
			if err := budget.beginConnector(); err != nil {
				return nil, err
			}
			if err := budget.addJSON(connector); err != nil {
				return nil, err
			}
			connectors = append(connectors, connector)
		}
	} else {
		config, err := capabilityMCPConfig(m.MCPConfig)
		if err != nil {
			return nil, err
		}
		expert.MCPConfig = config
	}
	if err := budget.beginExpert(); err != nil {
		return nil, err
	}
	if err := budget.addJSON(expert); err != nil {
		return nil, err
	}
	binding := fleet.CapabilityExpertBinding{ExpertName: m.Name, RuntimeID: in.RuntimeID}
	if err := budget.beginBinding(); err != nil {
		return nil, err
	}
	if err := budget.addJSON(binding); err != nil {
		return nil, err
	}
	skillNames, skills, err := s.capabilitySkills(ctx, m.Skills, m.Summary, budget, nil)
	if err != nil {
		return nil, err
	}
	expert.SkillNames = skillNames
	return &fleet.CapabilityInstallRequest{
		Definition: fleet.CapabilityDefinition{
			SchemaVersion: "1.0", Name: "marketplace:" + m.ID, Skills: skills, Connectors: connectors,
			Experts: []fleet.CapabilityExpert{expert},
		},
		Bindings: fleet.CapabilityBindings{Experts: []fleet.CapabilityExpertBinding{binding}},
	}, nil
}

// installCapability resolves an existing caller-keyed receipt before writing.
// The current connector wire is the only candidate eligible for a new install;
// the legacy candidate is used only by ReplayCapability.
func (s *Service) installCapability(ctx context.Context, in InstallInput, buildCurrent func() (*fleet.CapabilityInstallRequest, error), buildLegacy func(*fleet.CapabilityInstallRequest) (*fleet.CapabilityInstallRequest, error)) (*fleet.CapabilityInstallResult, error) {
	key := in.IdempotencyKey
	hasReceipt := false
	if key != "" {
		var err error
		hasReceipt, err = s.fleet.HasCapabilityInstallationReceipt(ctx, in.Token, in.SpaceID, in.WorkspaceID, key)
		if err != nil {
			return nil, s.capabilityDispatchError(key, err)
		}
	}
	current, err := buildCurrent()
	if err != nil {
		if !hasReceipt || !errors.Is(err, ErrInstallTooLarge) {
			return nil, err
		}
		// Current-wire capacity is not a constraint on a retained legacy wire.
		legacy, err := buildLegacy(nil)
		if err != nil {
			return nil, err
		}
		return s.replayCapability(ctx, in, key, legacy)
	}
	if key == "" {
		key = s.idGen()
		return s.installCapabilityOnce(ctx, in, key, *current)
	}
	if !hasReceipt {
		result, err := s.installCapabilityOnce(ctx, in, key, *current)
		if err == nil {
			return result, nil
		}
		if !capabilityReceiptMismatch(err) {
			return nil, err
		}
		// A concurrent same-key operation committed between the presence read
		// and this send. Only its stored wire may recover the operation.
		legacy, buildErr := buildLegacy(current)
		if buildErr != nil {
			return nil, buildErr
		}
		recovered, replayErr := s.replayCapability(ctx, in, key, legacy)
		if replayErr == nil {
			return recovered, nil
		}
		if capabilityReceiptMismatch(replayErr) || errors.Is(replayErr, ErrInstallReceiptUnavailable) {
			return nil, err
		}
		return nil, replayErr
	}
	// Prefer the current connector wire; the legacy candidate is tried only if
	// Fleet explicitly reports a fingerprint mismatch.
	result, err := s.replayCapability(ctx, in, key, current)
	if err == nil || !capabilityReceiptMismatch(err) {
		return result, err
	}
	candidate, buildErr := buildLegacy(current)
	if buildErr != nil {
		return nil, buildErr
	}
	if candidate == nil {
		return nil, err
	}
	return s.replayCapability(ctx, in, key, candidate)
}

func (s *Service) installCapabilityOnce(ctx context.Context, in InstallInput, key string, request fleet.CapabilityInstallRequest) (*fleet.CapabilityInstallResult, error) {
	result, err := s.fleet.InstallCapability(ctx, in.Token, in.SpaceID, in.WorkspaceID, key, request)
	if err == nil {
		return result, nil
	}
	return nil, s.capabilityDispatchError(key, err)
}

// replayCapability verifies one wire against Fleet's existing receipt without installing.
func (s *Service) replayCapability(ctx context.Context, in InstallInput, key string, candidate *fleet.CapabilityInstallRequest) (*fleet.CapabilityInstallResult, error) {
	if candidate == nil {
		return nil, &InstallDispatchError{IdempotencyKey: key, Err: ErrInstallReceiptUnavailable}
	}
	result, err := s.fleet.ReplayCapability(ctx, in.Token, in.SpaceID, in.WorkspaceID, key, *candidate)
	if err != nil {
		return nil, s.capabilityDispatchError(key, err)
	}
	if result == nil {
		return nil, &InstallDispatchError{IdempotencyKey: key, Err: ErrInstallReceiptUnavailable}
	}
	return result, nil
}

func (s *Service) capabilityDispatchError(key string, err error) error {
	var apiErr *fleet.CapabilityAPIError
	if errors.As(err, &apiErr) {
		return &InstallDispatchError{IdempotencyKey: key, Err: err}
	}
	return &InstallDispatchError{IdempotencyKey: key, Err: fmt.Errorf("%w: %w", ErrFleetInstallUnavailable, err)}
}

// capabilityReceiptMismatch recognizes only Fleet's explicit same-key
// fingerprint conflict; generic/in-progress conflicts stay uncertain.
func capabilityReceiptMismatch(err error) bool {
	var apiErr *fleet.CapabilityAPIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusConflict {
		return false
	}
	return apiErr.IdempotencyKeyReused || apiErr.Code == "IDEMPOTENCY_KEY_REUSED"
}

// legacyExpertInstallRequest derives the pre-connector-default wire from the
// current one. The returned candidate is only ever sent to ReplayCapability.
func legacyExpertInstallRequest(current fleet.CapabilityInstallRequest, raw string) *fleet.CapabilityInstallRequest {
	config, err := capabilityMCPConfig(raw)
	if err != nil {
		return nil
	}
	legacy := current
	legacy.Definition.Connectors = nil
	legacy.Definition.Experts = []fleet.CapabilityExpert{current.Definition.Experts[0]}
	legacy.Definition.Experts[0].ConnectorNames = nil
	legacy.Definition.Experts[0].MCPConfig = config
	return &legacy
}

// legacySquadReceiptCandidate derives the pre-connector-default squad wire using
// each member's own MCP config. It is only used for read-only receipt matching.
func legacySquadReceiptCandidate(current fleet.CapabilityInstallRequest, members []model.SquadMember) *fleet.CapabilityInstallRequest {
	legacy := current
	legacy.Definition.Connectors = nil
	legacy.Definition.Experts = append([]fleet.CapabilityExpert(nil), current.Definition.Experts...)
	for i := range legacy.Definition.Experts {
		config, err := capabilityMCPConfig(members[i].MCPConfig)
		if err != nil {
			return nil
		}
		legacy.Definition.Experts[i].ConnectorNames = nil
		legacy.Definition.Experts[i].MCPConfig = config
	}
	return &legacy
}

// capabilityMCPConfig is the historical pre-connector-default normalizer kept
// solely to reconstruct a read-only receipt candidate. It must not be used to
// start an installation.
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
	return json.Marshal(config)
}

// capabilityMCPConnector validates the stored config at the Fleet boundary and
// creates a source-less Workspace connector snapshot without rewriting it.
func capabilityMCPConnector(raw, name string) (fleet.CapabilityConnector, bool, error) {
	if strings.TrimSpace(raw) == "" {
		return fleet.CapabilityConnector{}, false, nil
	}
	if len(raw) > model.MaxMCPConfigBytes {
		return fleet.CapabilityConnector{}, false, fmt.Errorf("%w: mcp_config exceeds %d bytes", ErrInvalidRequest, model.MaxMCPConfigBytes)
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	var config map[string]json.RawMessage
	if decoder.Decode(&config) != nil || config == nil || decoder.Decode(new(any)) != io.EOF {
		return fleet.CapabilityConnector{}, false, invalidCapabilityMCPConfig()
	}
	if len(config) == 0 {
		return fleet.CapabilityConnector{}, false, nil
	}
	serversRaw, ok := config["mcpServers"]
	if !ok {
		return fleet.CapabilityConnector{}, false, invalidCapabilityMCPConfig()
	}
	var servers map[string]json.RawMessage
	if json.Unmarshal(serversRaw, &servers) != nil || servers == nil {
		return fleet.CapabilityConnector{}, false, invalidCapabilityMCPConfig()
	}
	for _, serverRaw := range servers {
		var server map[string]json.RawMessage
		if json.Unmarshal(serverRaw, &server) != nil || server == nil {
			return fleet.CapabilityConnector{}, false, invalidCapabilityMCPConfig()
		}
	}
	if len(servers) == 0 {
		return fleet.CapabilityConnector{}, false, nil
	}
	return fleet.CapabilityConnector{
		Name: name, Config: json.RawMessage(raw), Version: nil,
	}, true, nil
}

func invalidCapabilityMCPConfig() error {
	return fmt.Errorf("%w: invalid MCP connector configuration", ErrInvalidRequest)
}

func (s *Service) capabilitySkills(ctx context.Context, refs []model.SkillRef, description string, request *capabilityBudget, seen map[string]struct{}) ([]string, []fleet.CapabilitySkill, error) {
	names := make([]string, 0, len(refs))
	skills := make([]fleet.CapabilitySkill, 0, len(refs))
	for i := range refs {
		ref := refs[i]
		request.skill, request.path = ref.Name, "SKILL.md"
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
		content, err := s.readCapabilitySkillContent(ctx, ref, request)
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
		files, err := s.capabilitySkillFiles(ctx, ref, request)
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

func (s *Service) capabilitySkillFiles(ctx context.Context, ref model.SkillRef, request *capabilityBudget) ([]fleet.CapabilitySkillFile, error) {
	files := make([]fleet.CapabilitySkillFile, 0, len(ref.SupportingFiles))
	add := func(path, content string) error {
		if !utf8.ValidString(content) || strings.ContainsRune(content, 0) {
			return fmt.Errorf("%w: skill supporting files must be text", ErrInvalidRequest)
		}
		request.path = path
		file := fleet.CapabilitySkillFile{Path: path, Source: fleet.CapabilityInlineTextSource{Type: "inline_text", Content: content}}
		if err := request.addSkillFile(len(files) == 0, file); err != nil {
			return err
		}
		files = append(files, file)
		return nil
	}
	if ref.Markdown == "" && ref.ZipObjectKey != "" {
		tmpPath, size, cleanup, err := s.downloadToTempFile(ctx, ref.ZipObjectKey)
		if err != nil {
			return nil, err
		}
		defer cleanup()
		archive, err := os.Open(tmpPath)
		if err != nil {
			return nil, err
		}
		defer archive.Close()
		entries, code, message := parse.ExtractArchive(archive, size, maxSkillPackageBytes, fleet.MaxCapabilityRequestBytes, fleet.MaxCapabilityRequestBytes, -1)
		if code == "FILE_TOO_LARGE" {
			return nil, &installCapacityError{actualBytes: int64(fleet.MaxCapabilityRequestBytes) + 1, skill: ref.Name, path: "skill/package.zip"}
		}
		if code != "" {
			return nil, fmt.Errorf("invalid skill package: %s", message)
		}
		paths := make([]string, 0, len(entries))
		for name := range entries {
			if !parse.IsSkillMDCandidate(name) {
				paths = append(paths, name)
			}
		}
		sort.Strings(paths)
		for _, name := range paths {
			if err := add(name, string(entries[name])); err != nil {
				return nil, err
			}
		}
		return files, nil
	}
	for _, file := range ref.SupportingFiles {
		if err := add(file.Path, file.Content); err != nil {
			return nil, err
		}
	}
	return files, nil
}

func (s *Service) readCapabilitySkillContent(ctx context.Context, ref model.SkillRef, request *capabilityBudget) (string, error) {
	if ref.Markdown != "" {
		return ref.Markdown, nil
	}
	reader, err := s.store.GetObject(ctx, ref.ObjectKey)
	if err != nil {
		return "", fmt.Errorf("get skill content: %w", err)
	}
	defer reader.Close()
	content, err := io.ReadAll(io.LimitReader(reader, request.remaining+1))
	if err != nil {
		return "", err
	}
	if int64(len(content)) > request.remaining {
		return "", request.take(int64(len(content)))
	}
	if !utf8.Valid(content) || bytes.IndexByte(content, 0) >= 0 {
		return "", fmt.Errorf("%w: skill content must be text", ErrInvalidRequest)
	}
	return string(content), nil
}
