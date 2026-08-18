package expert

import (
	"context"
	"strconv"
	"strings"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/fleet"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

// InstallSquadResult carries the created Loop squad's id and its leader agent's
// id.
type InstallSquadResult struct {
	SquadID       string
	LeaderAgentID string
}

// createdAgent tracks one provisioned member agent (its id + created skill ids)
// so a later-step failure can roll the whole squad install back.
type createdAgent struct {
	agentID  string
	skillIDs []string
}

// InstallSquad provisions a marketplace squad into the caller's Loop
// workspace/runtime. It first installs each member as a Loop agent (create agent
// → create skills → bind, reusing provisionAgent), then forms the squad (create
// the squad led by the leader member, write the squad's dispatch strategies as
// its instructions, then attach the remaining members). Any
// failure rolls back the squad (if created) and every member agent provisioned
// so far, so a partial install never leaves orphans behind. It acts as the
// calling user (forwarded token), so fleet enforces workspace membership (squad
// create requires owner/admin), runtime access, and space scoping — this layer
// does not re-check them.
func (s *Service) InstallSquad(ctx context.Context, caller Caller, squadID string, in InstallInput) (InstallSquadResult, error) {
	if s.fleet == nil {
		return InstallSquadResult{}, ErrFleetNotConfigured
	}
	if strings.TrimSpace(in.WorkspaceID) == "" || strings.TrimSpace(in.RuntimeID) == "" {
		return InstallSquadResult{}, ErrInvalidRequest
	}

	// Bound the whole squad install (its fan-out is the largest: members ×
	// skills × files). Rollback runs on a detached context, so it still fires if
	// this deadline is what trips.
	ctx, cancel := context.WithTimeout(ctx, installTimeout)
	defer cancel()

	m, err := s.loadVisibleSquad(ctx, caller, squadID)
	if err != nil {
		return InstallSquadResult{}, err
	}
	if len(m.Members) == 0 {
		return InstallSquadResult{}, ErrInvalidRequest
	}

	leaderIdx := squadLeaderIndex(m)

	// One file budget shared across every member so the cap is on the whole
	// install, not per member.
	budget := &fileBudget{remaining: maxSkillFilesPerInstall}

	// Provision every member as a Loop agent, tracking created agents so a
	// later failure unwinds them all. provisionAgent is atomic per member, so a
	// failure here leaves only the *earlier* members to unwind.
	created := make([]createdAgent, 0, len(m.Members))
	memberAgentIDs := make([]string, len(m.Members))
	// Fleet skill names are workspace-wide. Keep the first packaged skill with a
	// given normalized name and skip later duplicates across squad members.
	seenSkillNames := make(map[string]struct{})
	for i := range m.Members {
		agentID, skillIDs, err := s.provisionAgent(ctx, in, agentProvisionSpec{
			Name:        m.Members[i].Name,
			Summary:     m.Summary,
			Instruction: m.Members[i].Instruction,
			MCPConfig:   m.Members[i].MCPConfig,
			Skills:      m.Members[i].Skills,
		}, budget, seenSkillNames)
		if err != nil {
			s.rollbackSquad(ctx, in, "", created)
			return InstallSquadResult{}, err
		}
		created = append(created, createdAgent{agentID: agentID, skillIDs: skillIDs})
		memberAgentIDs[i] = agentID
	}

	// Form the squad. Fleet auto-adds the leader as a member (role "leader").
	leaderAgentID := memberAgentIDs[leaderIdx]
	fleetSquadID, err := s.fleet.CreateSquad(ctx, in.Token, in.SpaceID, in.WorkspaceID, fleet.SquadSpec{
		Name:          m.Name,
		Description:   m.Summary,
		LeaderAgentID: leaderAgentID,
	})
	if err != nil {
		s.rollbackSquad(ctx, in, "", created)
		return InstallSquadResult{}, err
	}

	// Write the squad's dispatch strategies as its Loop instructions. This is a
	// second call because fleet's create endpoint doesn't accept instructions,
	// and it fails the install (with full rollback) rather than best-effort — a
	// squad silently missing its dispatch rules is exactly the defect this write
	// exists to prevent. No strategies → leave fleet's instructions empty
	// instead of injecting a client-side default.
	if instructions := squadInstructions(m); instructions != "" {
		if err := s.updateSquadInstructions(ctx, in, fleetSquadID, instructions); err != nil {
			s.rollbackSquad(ctx, in, fleetSquadID, created)
			return InstallSquadResult{}, err
		}
	}

	// Attach the remaining (non-leader) members.
	for i := range m.Members {
		if i == leaderIdx {
			continue
		}
		role := strings.TrimSpace(m.Members[i].Role)
		if role == "" {
			role = "member"
		}
		if err := s.fleet.AddSquadMember(ctx, in.Token, in.SpaceID, in.WorkspaceID, fleetSquadID, fleet.SquadMemberSpec{
			MemberType: "agent",
			MemberID:   memberAgentIDs[i],
			Role:       role,
		}); err != nil {
			s.rollbackSquad(ctx, in, fleetSquadID, created)
			return InstallSquadResult{}, err
		}
	}

	// Only the squad's own counter is bumped — member experts are self-contained
	// snapshots inside the squad, so a squad install never inflates expert counts.
	s.trackInstall(ctx, "squad", squadID)
	return InstallSquadResult{SquadID: fleetSquadID, LeaderAgentID: leaderAgentID}, nil
}

// squadInstructions renders the squad's ordered dispatch strategies (专家团的
// 调度策略) as the Loop squad's instructions: one numbered line per rule. Blank
// rules are skipped; no usable rules → "".
func squadInstructions(m *model.Squad) string {
	var b strings.Builder
	n := 0
	for _, raw := range m.Strategies {
		rule := strings.TrimSpace(raw)
		if rule == "" {
			continue
		}
		n++
		if n > 1 {
			b.WriteByte('\n')
		}
		b.WriteString(strconv.Itoa(n))
		b.WriteString(". ")
		b.WriteString(rule)
	}
	return b.String()
}

// updateSquadInstructions retries the small, idempotent partial update before
// allowing its failure to trigger destructive rollback. There is intentionally
// no sleep: Fleet's HTTP request already consumes bounded time and immediate
// retries cover transient connection resets/5xx without extending installs.
func (s *Service) updateSquadInstructions(ctx context.Context, in InstallInput, squadID, instructions string) error {
	var err error
	for attempt := 0; attempt < squadInstructionUpdateAttempts; attempt++ {
		err = s.fleet.UpdateSquadInstructions(ctx, in.Token, in.SpaceID, in.WorkspaceID, squadID, instructions)
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			break
		}
	}
	return err
}

// squadLeaderIndex picks the leader member: the first member flagged IsLeader,
// else the member whose name matches the squad's Leader label, else the first
// member. InstallSquad guarantees len(m.Members) > 0 before calling.
func squadLeaderIndex(m *model.Squad) int {
	for i := range m.Members {
		if m.Members[i].IsLeader {
			return i
		}
	}
	if strings.TrimSpace(m.Leader) != "" {
		for i := range m.Members {
			if m.Members[i].Name == m.Leader {
				return i
			}
		}
	}
	return 0
}

// rollbackSquad best-effort archives the created squad (if any) then deletes
// every provisioned member agent (and its skills). Errors are ignored: the
// original failure is what the caller reports, and fleet GC / the user can clean
// up any residue. The squad delete uses cleanupContext (detached from the
// request's cancellation) for the same reason rollbackAgent does — a request
// that failed *because* it was canceled must still clean up.
func (s *Service) rollbackSquad(ctx context.Context, in InstallInput, fleetSquadID string, created []createdAgent) {
	if fleetSquadID != "" {
		cctx, cancel := cleanupContext(ctx)
		_ = s.fleet.DeleteSquad(cctx, in.Token, in.SpaceID, in.WorkspaceID, fleetSquadID)
		cancel()
	}
	for _, a := range created {
		s.rollbackAgent(ctx, in, a.agentID, a.skillIDs)
	}
}
