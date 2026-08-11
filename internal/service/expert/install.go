package expert

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/fleet"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/service/parse"
)

// cleanupTimeout bounds the detached rollback deletes so they can't hang.
const cleanupTimeout = 15 * time.Second

// maxSkillFilesPerSkill caps how many supporting files one packaged skill
// contributes to the created fleet skill, bounding the PUT fan-out.
const maxSkillFilesPerSkill = 50

// ErrFleetNotConfigured is returned by InstallExpert when no fleet client is
// wired (OCTO_FLEET_URL unset). The handler maps it to UPSTREAM_UNAVAILABLE.
var ErrFleetNotConfigured = errors.New("fleet not configured")

// FleetProvisioner is the octo-fleet surface InstallExpert drives. *fleet.Client
// satisfies it; tests provide a fake. Every method forwards the end user's octo
// token + space + workspace so fleet authorizes the call as that user.
type FleetProvisioner interface {
	CreateAgent(ctx context.Context, token, spaceID, workspaceID string, spec fleet.AgentSpec) (agentID string, err error)
	CreateSkill(ctx context.Context, token, spaceID, workspaceID string, spec fleet.SkillSpec) (skillID string, err error)
	UpsertSkillFile(ctx context.Context, token, spaceID, workspaceID, skillID, path, content string) error
	SetAgentSkills(ctx context.Context, token, spaceID, workspaceID, agentID string, skillIDs []string) error
	DeleteAgent(ctx context.Context, token, spaceID, workspaceID, agentID string) error
	DeleteSkill(ctx context.Context, token, spaceID, workspaceID, skillID string) error
}

// WithFleet wires the fleet provisioner and returns the Service for chaining
// at construction (router). Kept off New so existing callers/tests are unchanged.
func (s *Service) WithFleet(f FleetProvisioner) *Service {
	s.fleet = f
	return s
}

// InstallInput carries the per-request install parameters. WorkspaceID and
// RuntimeID come from the request body; SpaceID and Token are the caller's
// forwarded credentials (Token is re-read from the request header by the
// handler because middleware discards it).
type InstallInput struct {
	WorkspaceID string
	RuntimeID   string
	SpaceID     string
	Token       string
}

// InstallResult is the created agent's id.
type InstallResult struct {
	AgentID string
}

// InstallExpert provisions the expert as a Loop agent in the caller's chosen
// workspace/runtime, aggregating fleet calls: create the agent (seeded with the
// expert's instruction + mcp_config), create one workspace skill per packaged
// skill (SKILL.md content), then bind those skills to the agent. Any failure
// after the agent exists rolls back everything created so far. It acts as the
// calling user (forwarded token), so fleet enforces workspace membership,
// runtime access, and space scoping — this layer does not re-check them.
func (s *Service) InstallExpert(ctx context.Context, caller Caller, expertID string, in InstallInput) (InstallResult, error) {
	if s.fleet == nil {
		return InstallResult{}, ErrFleetNotConfigured
	}
	if strings.TrimSpace(in.WorkspaceID) == "" || strings.TrimSpace(in.RuntimeID) == "" {
		return InstallResult{}, ErrInvalidRequest
	}

	m, err := s.loadVisibleExpert(ctx, caller, expertID)
	if err != nil {
		return InstallResult{}, err
	}

	agentSpec := fleet.AgentSpec{
		Name:         m.Name,
		Description:  m.Summary,
		Instructions: m.Instruction,
		RuntimeID:    in.RuntimeID,
	}
	if mc := strings.TrimSpace(m.MCPConfig); mc != "" {
		agentSpec.MCPConfig = json.RawMessage(mc)
	}

	agentID, err := s.fleet.CreateAgent(ctx, in.Token, in.SpaceID, in.WorkspaceID, agentSpec)
	if err != nil {
		return InstallResult{}, err
	}

	// From here on, roll back the created agent (and any skills) on failure so a
	// partial install never leaves an orphaned agent behind.
	skillIDs, err := s.installExpertSkills(ctx, caller, m, in)
	if err != nil {
		s.rollbackInstall(ctx, in, agentID, skillIDs)
		return InstallResult{}, err
	}

	if len(skillIDs) > 0 {
		if err := s.fleet.SetAgentSkills(ctx, in.Token, in.SpaceID, in.WorkspaceID, agentID, skillIDs); err != nil {
			s.rollbackInstall(ctx, in, agentID, skillIDs)
			return InstallResult{}, err
		}
	}

	return InstallResult{AgentID: agentID}, nil
}

// installExpertSkills creates one fleet workspace skill per packaged skill on
// the expert (those with stored SKILL.md content), then attaches the skill
// package's supporting files, returning the new skill ids. Name-only skills (no
// ObjectKey) carry nothing to install and are skipped. On the first failure it
// deletes the skills it already created and returns the error, so the caller
// only has the agent left to unwind.
func (s *Service) installExpertSkills(ctx context.Context, caller Caller, m *model.Expert, in InstallInput) ([]string, error) {
	created := make([]string, 0, len(m.Skills))
	for i := range m.Skills {
		if m.Skills[i].ObjectKey == "" {
			continue
		}
		content, err := s.readSkillContent(ctx, m.Skills, i)
		if err != nil {
			s.deleteSkills(ctx, in, created)
			return nil, err
		}
		skillID, err := s.fleet.CreateSkill(ctx, in.Token, in.SpaceID, in.WorkspaceID, fleet.SkillSpec{
			Name:        m.Skills[i].Name,
			Description: m.Summary,
			Content:     content,
		})
		if err != nil {
			s.deleteSkills(ctx, in, created)
			return nil, err
		}
		// Track before attaching files so a file failure also unwinds this skill.
		created = append(created, skillID)
		if err := s.attachSkillFiles(ctx, in, m.Skills[i], skillID); err != nil {
			s.deleteSkills(ctx, in, created)
			return nil, err
		}
	}
	return created, nil
}

// attachSkillFiles pushes the packaged skill's supporting files (everything but
// SKILL.md) onto the freshly-created fleet skill via UpsertSkillFile. It reads
// the stored .zip, extracting UTF-8 text files only (binaries are skipped by
// ExtractSkillFiles). A missing/unreadable/unparseable package is treated as
// "no extra files" — the SKILL.md-backed skill is already usable — so it does
// NOT fail the install; only an actual fleet PUT error does.
func (s *Service) attachSkillFiles(ctx context.Context, in InstallInput, ref model.SkillRef, skillID string) error {
	if ref.ZipObjectKey == "" || s.store == nil {
		return nil
	}
	tmpPath, _, cleanup, err := s.downloadToTempFile(ctx, ref.ZipObjectKey)
	if err != nil {
		return nil
	}
	defer cleanup()

	files, _, code, _ := parse.ExtractSkillFiles(tmpPath, maxSkillPackageBytes, maxSkillFilesPerSkill)
	if code != "" {
		return nil
	}
	for _, f := range files {
		if err := s.fleet.UpsertSkillFile(ctx, in.Token, in.SpaceID, in.WorkspaceID, skillID, f.Path, f.Content); err != nil {
			return err
		}
	}
	return nil
}

// rollbackInstall best-effort deletes the created skills then the agent. Errors
// are ignored: the original failure is what the caller reports, and fleet GC /
// the user can clean up any residue.
func (s *Service) rollbackInstall(ctx context.Context, in InstallInput, agentID string, skillIDs []string) {
	s.deleteSkills(ctx, in, skillIDs)
	if agentID != "" {
		cctx, cancel := cleanupContext(ctx)
		defer cancel()
		_ = s.fleet.DeleteAgent(cctx, in.Token, in.SpaceID, in.WorkspaceID, agentID)
	}
}

func (s *Service) deleteSkills(ctx context.Context, in InstallInput, skillIDs []string) {
	if len(skillIDs) == 0 {
		return
	}
	cctx, cancel := cleanupContext(ctx)
	defer cancel()
	for _, id := range skillIDs {
		_ = s.fleet.DeleteSkill(cctx, in.Token, in.SpaceID, in.WorkspaceID, id)
	}
}

// cleanupContext derives the context for best-effort rollback deletes. It is
// DETACHED from the request's cancellation/deadline (via WithoutCancel) so the
// deletes still run when the install failed *because* the request was canceled
// or timed out mid-flight — otherwise the rollback would no-op on a canceled ctx
// and leave exactly the orphaned agent/skills it exists to prevent. A fresh
// timeout keeps the detached calls bounded.
func cleanupContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
}
