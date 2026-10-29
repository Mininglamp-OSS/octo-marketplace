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

// ErrInstallTooLarge is returned when an install exceeds the aggregate Skill
// file limit. The handler maps it to 400.
var ErrInstallTooLarge = errors.New("install exceeds resource limits")

// ErrFleetInstallUnavailable marks a capability request whose result could not
// be determined from Fleet (for example, a transport failure).
var ErrFleetInstallUnavailable = errors.New("fleet capability install unavailable")

// fileBudget caps supporting files across every Skill/member in one install.
type fileBudget struct{ remaining int }

func (b *fileBudget) take() bool {
	if b.remaining <= 0 {
		return false
	}
	b.remaining--
	return true
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
	skillNames, skills, err := s.capabilitySkills(ctx, m.Skills, m.Summary, &fileBudget{remaining: maxSkillFilesPerInstall}, nil)
	if err != nil {
		return InstallResult{}, err
	}
	expert := fleet.CapabilityExpert{
		Name: m.Name, Version: nil, MarketExpertID: m.ID, Description: m.Summary, Instructions: m.Instruction,
		SkillNames: skillNames, CustomEnv: in.CustomEnv, MCPConfig: capabilityMCPConfig(m.MCPConfig),
	}
	request := fleet.CapabilityInstallRequest{
		Definition: fleet.CapabilityDefinition{
			SchemaVersion: "1.0", Name: "marketplace:" + m.ID, Skills: skills,
			Experts: []fleet.CapabilityExpert{expert},
		},
		Bindings: fleet.CapabilityBindings{Experts: []fleet.CapabilityExpertBinding{{
			ExpertName: m.Name, RuntimeID: in.RuntimeID,
		}}},
	}
	result, err := s.installCapability(ctx, in, request)
	if err != nil {
		return InstallResult{}, err
	}
	s.trackInstall(ctx, "expert", expertID)
	return InstallResult{AgentID: result.ExpertID}, nil
}

func (s *Service) installCapability(ctx context.Context, in InstallInput, request fleet.CapabilityInstallRequest) (*fleet.CapabilityInstallResult, error) {
	result, err := s.fleet.InstallCapability(ctx, in.Token, in.SpaceID, in.WorkspaceID, s.idGen(), request)
	if err == nil {
		return result, nil
	}
	var apiErr *fleet.CapabilityAPIError
	if errors.As(err, &apiErr) {
		return nil, err
	}
	return nil, fmt.Errorf("%w: %w", ErrFleetInstallUnavailable, err)
}

func capabilityMCPConfig(raw string) json.RawMessage {
	if raw = strings.TrimSpace(raw); raw != "" {
		return json.RawMessage(raw)
	}
	return nil
}

func (s *Service) capabilitySkills(ctx context.Context, refs []model.SkillRef, description string, budget *fileBudget, seen map[string]struct{}) ([]string, []fleet.CapabilitySkill, error) {
	names := make([]string, 0, len(refs))
	skills := make([]fleet.CapabilitySkill, 0, len(refs))
	for i := range refs {
		ref := refs[i]
		if ref.ObjectKey == "" && ref.Markdown == "" {
			continue
		}
		if seen != nil {
			if _, exists := seen[ref.Name]; exists {
				names = append(names, ref.Name)
				continue
			}
		}
		content, err := s.readSkillContent(ctx, refs, i)
		if err != nil {
			return nil, nil, err
		}
		files, err := s.capabilitySkillFiles(ctx, ref, budget)
		if err != nil {
			return nil, nil, err
		}
		skills = append(skills, fleet.CapabilitySkill{
			Name: ref.Name, Version: nil, Description: description, Content: content,
			Config: map[string]any{}, Files: files,
		})
		names = append(names, ref.Name)
		if seen != nil {
			seen[ref.Name] = struct{}{}
		}
	}
	return names, skills, nil
}

func (s *Service) capabilitySkillFiles(ctx context.Context, ref model.SkillRef, budget *fileBudget) ([]fleet.CapabilitySkillFile, error) {
	if ref.Markdown != "" {
		files := make([]fleet.CapabilitySkillFile, 0, len(ref.SupportingFiles))
		for _, file := range ref.SupportingFiles {
			if !budget.take() {
				return nil, ErrInstallTooLarge
			}
			files = append(files, fleet.CapabilitySkillFile{
				Path: file.Path, Source: fleet.CapabilityInlineTextSource{Type: "inline_text", Content: file.Content},
			})
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
		files = append(files, fleet.CapabilitySkillFile{
			Path: file.Path, Source: fleet.CapabilityInlineTextSource{Type: "inline_text", Content: file.Content},
		})
	}
	return files, nil
}
