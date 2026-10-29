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

	budget := &fileBudget{remaining: maxSkillFilesPerInstall}
	seenSkillNames := make(map[string]struct{})
	definition := fleet.CapabilityDefinition{
		SchemaVersion: "1.0", Name: "marketplace:" + m.ID,
		Experts: make([]fleet.CapabilityExpert, 0, len(m.Members)),
	}
	bindings := fleet.CapabilityBindings{
		Experts: make([]fleet.CapabilityExpertBinding, 0, len(m.Members)),
	}
	teamMembers := make([]fleet.CapabilityTeamMember, len(m.Members))
	leaderIdx := squadLeaderIndex(m)
	for i := range m.Members {
		member := m.Members[i]
		skillNames, skills, err := s.capabilitySkills(ctx, member.Skills, m.Summary, budget, seenSkillNames)
		if err != nil {
			return InstallSquadResult{}, err
		}
		mcpConfig, err := capabilityMCPConfig(member.MCPConfig)
		if err != nil {
			return InstallSquadResult{}, err
		}
		definition.Skills = append(definition.Skills, skills...)
		definition.Experts = append(definition.Experts, fleet.CapabilityExpert{
			Name: member.Name, Version: nil, Description: m.Summary, Instructions: member.Instruction,
			SkillNames: skillNames, CustomEnv: in.CustomEnv, MCPConfig: mcpConfig,
		})
		bindings.Experts = append(bindings.Experts, fleet.CapabilityExpertBinding{
			ExpertName: member.Name, RuntimeID: in.RuntimeID,
		})
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
	definition.ExpertTeam = &fleet.CapabilityExpertTeam{
		Name: m.Name, Version: nil, MarketExpertTeamID: m.ID, Members: teamMembers, Description: m.Summary,
		Instructions: squadDispatchInstructions(m),
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
