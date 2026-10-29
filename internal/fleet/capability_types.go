package fleet

import "encoding/json"

// CapabilityInstallRequest is the versioned Skill installation contract with Fleet.
type CapabilityInstallRequest struct {
	Definition      CapabilityDefinition `json:"definition"`
	Bindings        CapabilityBindings   `json:"bindings"`
	LegacySkillWire bool                 `json:"-"`
}

// MarshalCapabilityInstallRequest preserves the original mk1 Skill shape only
// when replaying a receipt written before version and source fields were added.
func MarshalCapabilityInstallRequest(in CapabilityInstallRequest) ([]byte, error) {
	if !in.LegacySkillWire {
		return json.Marshal(in)
	}
	var skills []legacyCapabilitySkill
	if in.Definition.Skills != nil {
		skills = make([]legacyCapabilitySkill, 0, len(in.Definition.Skills))
		for _, skill := range in.Definition.Skills {
			skills = append(skills, legacyCapabilitySkill{
				Name: skill.Name, Description: skill.Description, Content: skill.Content,
				Config: skill.Config, Files: skill.Files,
			})
		}
	}
	var experts []legacyCapabilityExpert
	if in.Definition.Experts != nil {
		experts = make([]legacyCapabilityExpert, 0, len(in.Definition.Experts))
		for _, expert := range in.Definition.Experts {
			experts = append(experts, legacyCapabilityExpert{
				Name: expert.Name, Description: expert.Description, Instructions: expert.Instructions,
				SkillNames: expert.SkillNames, ConnectorNames: expert.ConnectorNames,
				CustomEnv: expert.CustomEnv, MCPConfig: expert.MCPConfig,
			})
		}
	}
	var team *legacyCapabilityExpertTeam
	if in.Definition.ExpertTeam != nil {
		team = &legacyCapabilityExpertTeam{
			Name: in.Definition.ExpertTeam.Name, Members: in.Definition.ExpertTeam.Members,
			Description: in.Definition.ExpertTeam.Description, Instructions: in.Definition.ExpertTeam.Instructions,
		}
	}
	request := legacyCapabilityInstallRequest{
		Definition: legacyCapabilityDefinition{
			SchemaVersion: in.Definition.SchemaVersion,
			Name:          in.Definition.Name,
			Skills:        skills,
			Connectors:    in.Definition.Connectors,
			Experts:       experts,
			ExpertTeam:    team,
		},
		Bindings: in.Bindings,
	}
	return json.Marshal(request)
}

type legacyCapabilityInstallRequest struct {
	Definition legacyCapabilityDefinition `json:"definition"`
	Bindings   CapabilityBindings         `json:"bindings"`
}

type legacyCapabilityDefinition struct {
	SchemaVersion string                      `json:"schema_version"`
	Name          string                      `json:"name"`
	Skills        []legacyCapabilitySkill     `json:"skills,omitempty"`
	Connectors    []CapabilityConnector       `json:"connectors,omitempty"`
	Experts       []legacyCapabilityExpert    `json:"experts"`
	ExpertTeam    *legacyCapabilityExpertTeam `json:"expert_team,omitempty"`
}

type legacyCapabilityExpert struct {
	Name           string            `json:"name"`
	Description    string            `json:"description"`
	Instructions   string            `json:"instructions"`
	SkillNames     []string          `json:"skill_names,omitempty"`
	ConnectorNames []string          `json:"connector_names,omitempty"`
	CustomEnv      map[string]string `json:"custom_env,omitempty"`
	MCPConfig      json.RawMessage   `json:"mcp_config,omitempty"`
}

type legacyCapabilityExpertTeam struct {
	Name         string                 `json:"name"`
	Members      []CapabilityTeamMember `json:"members"`
	Description  string                 `json:"description"`
	Instructions string                 `json:"instructions"`
}

type legacyCapabilitySkill struct {
	Name        string                `json:"name"`
	Description string                `json:"description"`
	Content     string                `json:"content"`
	Config      map[string]any        `json:"config"`
	Files       []CapabilitySkillFile `json:"files,omitempty"`
}

type CapabilityDefinition struct {
	SchemaVersion string                `json:"schema_version"`
	Name          string                `json:"name"`
	Skills        []CapabilitySkill     `json:"skills,omitempty"`
	Connectors    []CapabilityConnector `json:"connectors,omitempty"`
	Experts       []CapabilityExpert    `json:"experts"`
	ExpertTeam    *CapabilityExpertTeam `json:"expert_team,omitempty"`
}

type CapabilitySkill struct {
	Name          string                `json:"name"`
	Version       *string               `json:"version"`
	MarketSkillID string                `json:"market_skill_id,omitempty"`
	Description   string                `json:"description"`
	Content       string                `json:"content"`
	Config        map[string]any        `json:"config"`
	Files         []CapabilitySkillFile `json:"files,omitempty"`
}

type CapabilitySkillFile struct {
	Path   string                     `json:"path"`
	Source CapabilityInlineTextSource `json:"source"`
}

type CapabilityInlineTextSource struct {
	Type    string `json:"type"`
	Content string `json:"content"`
}

type CapabilityConnector struct {
	Name          string          `json:"name"`
	Description   string          `json:"description,omitempty"`
	AuthorName    *string         `json:"author_name,omitempty"`
	Slug          string          `json:"slug,omitempty"`
	Category      string          `json:"category,omitempty"`
	Tags          []string        `json:"tags,omitempty"`
	Documentation string          `json:"documentation,omitempty"`
	Config        json.RawMessage `json:"config"`
	Version       *string         `json:"version"`
	MarketMCPID   string          `json:"market_mcp_id,omitempty"`
}

type CapabilityExpert struct {
	Name           string   `json:"name"`
	Version        *string  `json:"version"`
	MarketExpertID string   `json:"market_expert_id,omitempty"`
	Description    string   `json:"description"`
	Instructions   string   `json:"instructions"`
	SkillNames     []string `json:"skill_names,omitempty"`
	ConnectorNames []string `json:"connector_names,omitempty"`
	// These transient values may contain secrets; never persist or log Definition.
	CustomEnv map[string]string `json:"custom_env,omitempty"`
	MCPConfig json.RawMessage   `json:"mcp_config,omitempty"`
}

type CapabilityExpertTeam struct {
	Name               string                 `json:"name"`
	Version            *string                `json:"version"`
	MarketExpertTeamID string                 `json:"market_expert_team_id,omitempty"`
	Members            []CapabilityTeamMember `json:"members"`
	Description        string                 `json:"description"`
	Instructions       string                 `json:"instructions"`
}

type CapabilityTeamMember struct {
	ExpertName string `json:"expert_name"`
	IsLeader   bool   `json:"is_leader"`
	Role       string `json:"role,omitempty"`
}

type CapabilityBindings struct {
	Experts []CapabilityExpertBinding `json:"experts"`
}

type CapabilityExpertBinding struct {
	ExpertName string `json:"expert_name"`
	RuntimeID  string `json:"runtime_id"`
}

type CapabilityInstallResult struct {
	Type           string                      `json:"type"`
	ExpertID       string                      `json:"expert_id,omitempty"`
	ExpertTeamID   string                      `json:"expert_team_id,omitempty"`
	LeaderExpertID string                      `json:"leader_expert_id,omitempty"`
	ExpertTeam     *CapabilityExpertTeamResult `json:"expert_team,omitempty"`
	Experts        []CapabilityExpertResult    `json:"experts"`
	Skills         []CapabilitySkillResult     `json:"skills"`
	Connectors     []CapabilityConnectorResult `json:"connectors,omitempty"`
	Replayed       bool                        `json:"-"`
	ReplayKnown    bool                        `json:"-"` // Missing replay metadata is not proof of a new installation.
}

type CapabilityExpertResult struct {
	ExpertID       string   `json:"expert_id"`
	Name           string   `json:"name"`
	Version        *string  `json:"version,omitempty"`
	MarketExpertID string   `json:"market_expert_id,omitempty"`
	Created        bool     `json:"created"`
	Restored       bool     `json:"restored"`
	SkillIDs       []string `json:"skill_ids"`
	ConnectorIDs   []string `json:"connector_ids,omitempty"`
	IsLeader       bool     `json:"is_leader,omitempty"`
}

type CapabilityExpertTeamResult struct {
	ExpertTeamID       string  `json:"expert_team_id"`
	Name               string  `json:"name"`
	Version            *string `json:"version,omitempty"`
	MarketExpertTeamID string  `json:"market_expert_team_id,omitempty"`
	Created            bool    `json:"created"`
	Restored           bool    `json:"restored"`
}

type CapabilitySkillResult struct {
	SkillID       string  `json:"skill_id"`
	Name          string  `json:"name"`
	Version       *string `json:"version,omitempty"`
	MarketSkillID string  `json:"market_skill_id,omitempty"`
	Created       bool    `json:"created"`
}

type CapabilityConnectorResult struct {
	ConnectorID string  `json:"connector_id"`
	Name        string  `json:"name"`
	Version     *string `json:"version" extensions:"x-nullable"`
	MarketMCPID *string `json:"market_mcp_id" extensions:"x-nullable"`
	Created     bool    `json:"created"`
}
