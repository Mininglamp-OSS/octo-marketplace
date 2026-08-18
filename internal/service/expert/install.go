package expert

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/fleet"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/logging"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/service/parse"
	"go.uber.org/zap"
)

// cleanupTimeout bounds the detached rollback deletes so they can't hang.
const cleanupTimeout = 15 * time.Second

// squadInstructionUpdateAttempts gives the post-create instructions update a
// small bounded retry window before destructive rollback. A transient failure at
// this late stage would otherwise archive every member agent and can make a
// clean retry collide with Fleet's archived-name uniqueness.
const squadInstructionUpdateAttempts = 3

// installTimeout bounds a whole install (expert or squad). The per-fleet-call
// timeout on the http client bounds each hop, but nothing bounds their sum: a
// squad of many members each with many packaged skills fans out to a large
// number of sequential upstream calls. This aggregate deadline caps the total.
const installTimeout = 5 * time.Minute

// maxSkillFilesPerSkill caps how many supporting files one packaged skill
// contributes to the created fleet skill, bounding the PUT fan-out.
const maxSkillFilesPerSkill = 50

// maxSkillFilesPerInstall caps the TOTAL supporting files one install may push
// to fleet across every skill and (for a squad) every member — a bound on the
// aggregate UpsertSkillFile fan-out that maxSkillFilesPerSkill alone can't give
// (30 members × 20 skills × 50 files would otherwise be ~30k PUTs per request).
const maxSkillFilesPerInstall = 500

// ErrFleetNotConfigured is returned by InstallExpert when no fleet client is
// wired (OCTO_FLEET_URL unset). The handler maps it to UPSTREAM_UNAVAILABLE.
var ErrFleetNotConfigured = errors.New("fleet not configured")

// ErrInstallTooLarge is returned when an install would exceed the aggregate
// skill-file fan-out cap (maxSkillFilesPerInstall). The handler maps it to 400.
var ErrInstallTooLarge = errors.New("install exceeds resource limits")

// fileBudget bounds the total supporting files an install may push to fleet. It
// is created once per install and shared across all skills/members, so the cap
// is on the aggregate — not reset per skill the way maxSkillFilesPerSkill is.
type fileBudget struct{ remaining int }

// take consumes one file from the budget, reporting false once it's exhausted.
func (b *fileBudget) take() bool {
	if b == nil {
		return true
	}
	if b.remaining <= 0 {
		return false
	}
	b.remaining--
	return true
}

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
	// Squad provisioning (used by InstallSquad): create a squad led by an
	// already-created member agent, write its instructions (fleet's create
	// endpoint doesn't accept them), attach the remaining members, and archive
	// the squad on rollback.
	CreateSquad(ctx context.Context, token, spaceID, workspaceID string, spec fleet.SquadSpec) (squadID string, err error)
	UpdateSquadInstructions(ctx context.Context, token, spaceID, workspaceID, squadID, instructions string) error
	AddSquadMember(ctx context.Context, token, spaceID, workspaceID, squadID string, m fleet.SquadMemberSpec) error
	DeleteSquad(ctx context.Context, token, spaceID, workspaceID, squadID string) error
}

// WithFleet wires the fleet provisioner and returns the Service for chaining
// at construction (router). Kept off New so existing callers/tests are unchanged.
func (s *Service) WithFleet(f FleetProvisioner) *Service {
	s.fleet = f
	return s
}

// InstallTracker is the metrics surface the install paths use to bump
// install_count after a successful provision. *metricssvc.Service satisfies it;
// the interface lives here (not an import) because the metrics package already
// imports this one for its visibility resolvers.
type InstallTracker interface {
	TrackInstall(ctx context.Context, resourceType, resourceID string) error
}

// WithMetrics wires the install counter (chainable at construction, like
// WithFleet). A nil / unwired tracker makes trackInstall a no-op.
func (s *Service) WithMetrics(m InstallTracker) *Service {
	s.metrics = m
	return s
}

// trackTimeout bounds the best-effort install counter bump. Deliberately much
// tighter than cleanupTimeout: tracking runs on the response path AFTER the
// install succeeded, and install is not idempotent — a stalled Redis must not
// hold a successful response long enough for a gateway/client timeout to
// trigger a duplicate-provisioning retry.
const trackTimeout = 2 * time.Second

// trackInstall bumps the install counter after a successful install.
// Best-effort and detached from the request context (a client disconnect right
// after a successful provision must not cancel the count); failures are logged,
// never returned — the install itself already succeeded.
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
	SpaceID     string
	Token       string
}

// InstallResult is the created agent's id.
type InstallResult struct {
	AgentID string
}

// InstallExpert provisions the expert as a Loop agent in the caller's chosen
// workspace/runtime. It acts as the calling user (forwarded token), so fleet
// enforces workspace membership, runtime access, and space scoping — this layer
// does not re-check them. The per-agent provisioning (create agent → create
// skills → bind, with rollback on partial failure) is shared with InstallSquad
// via provisionAgent.
func (s *Service) InstallExpert(ctx context.Context, caller Caller, expertID string, in InstallInput) (InstallResult, error) {
	if s.fleet == nil {
		return InstallResult{}, ErrFleetNotConfigured
	}
	if strings.TrimSpace(in.WorkspaceID) == "" || strings.TrimSpace(in.RuntimeID) == "" {
		return InstallResult{}, ErrInvalidRequest
	}

	// Bound the whole install; rollback runs on a detached context so it still
	// fires if this deadline is what trips.
	ctx, cancel := context.WithTimeout(ctx, installTimeout)
	defer cancel()

	m, err := s.loadVisibleExpert(ctx, caller, expertID)
	if err != nil {
		return InstallResult{}, err
	}

	agentID, _, err := s.provisionAgent(ctx, in, agentProvisionSpec{
		Name:        m.Name,
		Summary:     m.Summary,
		Instruction: m.Instruction,
		MCPConfig:   m.MCPConfig,
		Skills:      m.Skills,
	}, &fileBudget{remaining: maxSkillFilesPerInstall}, nil)
	if err != nil {
		return InstallResult{}, err
	}
	s.trackInstall(ctx, "expert", expertID)
	return InstallResult{AgentID: agentID}, nil
}

// agentProvisionSpec is the per-agent input to provisionAgent: the expert (or
// squad member) fields that seed one Loop agent and its skills.
type agentProvisionSpec struct {
	Name        string
	Summary     string
	Instruction string
	MCPConfig   string
	Skills      []model.SkillRef
}

// provisionAgent creates one Loop agent (seeded with the instruction +
// mcp_config), creates one workspace skill per packaged skill, then binds those
// skills to the agent. It is atomic: on any failure after the agent exists it
// rolls back everything IT created (skills + agent) and returns the error, so
// the caller has nothing to unwind for this agent. Shared by InstallExpert (one
// agent) and InstallSquad (one per member).
func (s *Service) provisionAgent(ctx context.Context, in InstallInput, spec agentProvisionSpec, budget *fileBudget, seenSkillNames map[string]struct{}) (string, []string, error) {
	agentSpec := fleet.AgentSpec{
		Name:         spec.Name,
		Description:  spec.Summary,
		Instructions: spec.Instruction,
		RuntimeID:    in.RuntimeID,
	}
	if mc := strings.TrimSpace(spec.MCPConfig); mc != "" {
		agentSpec.MCPConfig = json.RawMessage(mc)
	}

	agentID, err := s.fleet.CreateAgent(ctx, in.Token, in.SpaceID, in.WorkspaceID, agentSpec)
	if err != nil {
		return "", nil, err
	}

	// From here on, roll back the created agent (and any skills) on failure so a
	// partial provision never leaves an orphaned agent behind.
	skillIDs, err := s.installSkills(ctx, spec.Skills, spec.Summary, in, budget, seenSkillNames)
	if err != nil {
		s.rollbackAgent(ctx, in, agentID, skillIDs)
		return "", nil, err
	}

	if len(skillIDs) > 0 {
		if err := s.fleet.SetAgentSkills(ctx, in.Token, in.SpaceID, in.WorkspaceID, agentID, skillIDs); err != nil {
			s.rollbackAgent(ctx, in, agentID, skillIDs)
			return "", nil, err
		}
	}

	return agentID, skillIDs, nil
}

// installSkills creates one fleet workspace skill per packaged skill (those with
// stored SKILL.md content), then attaches each skill package's supporting files,
// returning the new skill ids. Name-only skills (no ObjectKey) carry nothing to
// install and are skipped. On the first failure it deletes the skills it already
// created and returns the error, so the caller only has the agent left to unwind.
func (s *Service) installSkills(ctx context.Context, skills []model.SkillRef, summary string, in InstallInput, budget *fileBudget, seenSkillNames map[string]struct{}) ([]string, error) {
	created := make([]string, 0, len(skills))
	for i := range skills {
		if skills[i].ObjectKey == "" {
			continue
		}
		// Fleet's UNIQUE(workspace_id, name) constraint is byte-exact. Deduplicate
		// only the names that Fleet itself would reject; case/whitespace variants
		// remain distinct packaged Skills and must both be installed.
		nameKey := skills[i].Name
		if seenSkillNames != nil {
			if _, exists := seenSkillNames[nameKey]; exists {
				continue
			}
		}
		content, err := s.readSkillContent(ctx, skills, i)
		if err != nil {
			s.deleteSkills(ctx, in, created)
			return nil, err
		}
		skillID, err := s.fleet.CreateSkill(ctx, in.Token, in.SpaceID, in.WorkspaceID, fleet.SkillSpec{
			Name:        skills[i].Name,
			Description: summary,
			Content:     content,
		})
		if err != nil {
			s.deleteSkills(ctx, in, created)
			return nil, err
		}
		// Track before attaching files so a file failure also unwinds this skill.
		created = append(created, skillID)
		if err := s.attachSkillFiles(ctx, in, skills[i], skillID, budget); err != nil {
			s.deleteSkills(ctx, in, created)
			return nil, err
		}
		if seenSkillNames != nil {
			seenSkillNames[nameKey] = struct{}{}
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
func (s *Service) attachSkillFiles(ctx context.Context, in InstallInput, ref model.SkillRef, skillID string, budget *fileBudget) error {
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
		// Charge the aggregate budget first: an install that would push more than
		// maxSkillFilesPerInstall files across all skills/members is rejected
		// (and rolled back) rather than allowed to fan out unbounded.
		if !budget.take() {
			return ErrInstallTooLarge
		}
		if err := s.fleet.UpsertSkillFile(ctx, in.Token, in.SpaceID, in.WorkspaceID, skillID, f.Path, f.Content); err != nil {
			return err
		}
	}
	return nil
}

// rollbackAgent best-effort deletes the created skills then the agent. Errors
// are ignored: the original failure is what the caller reports, and fleet GC /
// the user can clean up any residue.
func (s *Service) rollbackAgent(ctx context.Context, in InstallInput, agentID string, skillIDs []string) {
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
