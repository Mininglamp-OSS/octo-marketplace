package expert

import (
	"context"
	"strconv"
	"strings"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/fleet"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

// InstallSquadResult carries the installed Loop squad id and its leader agent id.
type InstallSquadResult struct {
	SquadID       string
	LeaderAgentID string
}

// InstallSquad installs the visible squad into the caller's workspace/runtime
// through one Fleet capability transaction. Fleet enforces workspace membership,
// runtime access, and Space scoping using the forwarded token.
func (s *Service) InstallSquad(ctx context.Context, caller Caller, squadID string, in InstallInput) (InstallSquadResult, error) {
	if s.fleet == nil {
		return InstallSquadResult{}, ErrFleetNotConfigured
	}
	if strings.TrimSpace(in.WorkspaceID) == "" || strings.TrimSpace(in.RuntimeID) == "" {
		return InstallSquadResult{}, ErrInvalidRequest
	}

	ctx, cancel := context.WithTimeout(ctx, installTimeout)
	defer cancel()

	m, err := s.loadVisibleSquad(ctx, caller, squadID)
	if err != nil {
		return InstallSquadResult{}, err
	}
	if len(m.Members) == 0 {
		return InstallSquadResult{}, ErrInvalidRequest
	}

	fileBudget := &fileBudget{remaining: maxSkillFilesPerInstall}
	requestBudget := newCapabilityBudget("marketplace:" + m.ID)
	seenSkillNames := make(map[string]struct{})
	experts := make([]fleet.CapabilityExpert, len(m.Members))
	bindings := fleet.CapabilityBindings{
		Experts: make([]fleet.CapabilityExpertBinding, 0, len(m.Members)),
	}
	teamMembers := make([]fleet.CapabilityTeamMember, len(m.Members))
	leaderIdx := squadLeaderIndex(m)
	// Charge every member's non-Skill metadata and the team wiring before any
	// object-storage read: oversized metadata must never be amplified by Skill
	// content reads.
	for i := range m.Members {
		member := m.Members[i]
		mcpConfig, err := capabilityMCPConfig(member.MCPConfig)
		if err != nil {
			return InstallSquadResult{}, err
		}
		expert := fleet.CapabilityExpert{
			Name: member.Name, Version: nil, Description: m.Summary, Instructions: member.Instruction,
			CustomEnv: in.CustomEnv, MCPConfig: mcpConfig,
		}
		if err := requestBudget.beginExpert(); err != nil {
			return InstallSquadResult{}, err
		}
		if err := requestBudget.addJSON(expert); err != nil {
			return InstallSquadResult{}, err
		}
		experts[i] = expert
		binding := fleet.CapabilityExpertBinding{
			ExpertName: member.Name, RuntimeID: in.RuntimeID,
		}
		if err := requestBudget.beginBinding(); err != nil {
			return InstallSquadResult{}, err
		}
		if err := requestBudget.addJSON(binding); err != nil {
			return InstallSquadResult{}, err
		}
		bindings.Experts = append(bindings.Experts, binding)
		role := strings.TrimSpace(member.Role)
		if i == leaderIdx {
			role = "leader"
		} else if role == "" {
			role = "member"
		}
		teamMembers[i] = fleet.CapabilityTeamMember{
			ExpertName: member.Name, IsLeader: i == leaderIdx, Role: role,
		}
	}
	team := &fleet.CapabilityExpertTeam{
		Name: m.Name, Version: nil, MarketExpertTeamID: m.ID, Members: teamMembers, Description: m.Summary,
		Instructions: squadDispatchInstructions(m),
	}
	if err := requestBudget.addTeam(team); err != nil {
		return InstallSquadResult{}, err
	}
	definition := fleet.CapabilityDefinition{
		SchemaVersion: "1.0", Name: "marketplace:" + m.ID, Experts: experts, ExpertTeam: team,
	}
	for i := range m.Members {
		member := m.Members[i]
		skillNames, skills, err := s.capabilitySkills(ctx, member.Skills, m.Summary, fileBudget, requestBudget, seenSkillNames)
		if err != nil {
			return InstallSquadResult{}, err
		}
		experts[i].SkillNames = skillNames
		definition.Skills = append(definition.Skills, skills...)
	}
	result, err := s.installCapability(ctx, in, fleet.CapabilityInstallRequest{
		Definition: definition, Bindings: bindings,
	})
	if err != nil {
		return InstallSquadResult{}, err
	}
	s.trackInstall(ctx, "squad", squadID)
	return InstallSquadResult{SquadID: result.ExpertTeamID, LeaderAgentID: result.LeaderExpertID}, nil
}

// squadDispatchInstructions preserves a source dispatch document when present;
// legacy squads fall back to their ordered strategies.
func squadDispatchInstructions(m *model.Squad) string {
	if text := strings.TrimSpace(m.Instructions); text != "" {
		return text
	}
	return squadInstructions(m)
}

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

// squadLeaderIndex picks the first member flagged IsLeader, else the member
// whose name matches the squad's Leader label, else the first member.
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
