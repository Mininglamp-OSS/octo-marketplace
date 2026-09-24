package fleet

import "encoding/json"

// CapabilityInstallRequest matches Fleet test commit 49a8266 (2026-09-24).
// Production use is gated by OCTO_FLEET_CAPABILITY_INSTALL_ENABLED (default false).
type CapabilityInstallRequest struct {
	Definition CapabilityDefinition `json:"definition"`
	Bindings   CapabilityBindings   `json:"bindings"`
}

type CapabilityDefinition struct {
	SchemaVersion string                `json:"schema_version"`
	Name          string                `json:"name"`
	Skills        []CapabilitySkill     `json:"skills,omitempty"`
	Experts       []CapabilityExpert    `json:"experts"`
	ExpertTeam    *CapabilityExpertTeam `json:"expert_team,omitempty"`
}

type CapabilitySkill struct {
	Name        string                `json:"name"`
	Description string                `json:"description"`
	Content     string                `json:"content"`
	Config      map[string]any        `json:"config"`
	Files       []CapabilitySkillFile `json:"files,omitempty"`
}

type CapabilitySkillFile struct {
	Path   string                     `json:"path"`
	Source CapabilityInlineTextSource `json:"source"`
}

type CapabilityInlineTextSource struct {
	Type    string `json:"type"`
	Content string `json:"content"`
}

type CapabilityExpert struct {
	Name         string   `json:"name"`
	Description  string   `json:"description"`
	Instructions string   `json:"instructions"`
	SkillNames   []string `json:"skill_names,omitempty"`
	// These transient values may contain secrets; never persist or log Definition.
	CustomEnv map[string]string `json:"custom_env,omitempty"`
	MCPConfig json.RawMessage   `json:"mcp_config,omitempty"`
}

type CapabilityExpertTeam struct {
	Name         string                 `json:"name"`
	Members      []CapabilityTeamMember `json:"members"`
	Description  string                 `json:"description"`
	Instructions string                 `json:"instructions"`
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
	Type           string                   `json:"type"`
	ExpertID       string                   `json:"expert_id,omitempty"`
	ExpertTeamID   string                   `json:"expert_team_id,omitempty"`
	LeaderExpertID string                   `json:"leader_expert_id,omitempty"`
	Experts        []CapabilityExpertResult `json:"experts"`
	Skills         []CapabilitySkillResult  `json:"skills"`
	Replayed       bool                     `json:"-"`
	ReplayKnown    bool                     `json:"-"` // Missing replay metadata is not proof of a new installation.
}

type CapabilityExpertResult struct {
	ExpertID string   `json:"expert_id"`
	Name     string   `json:"name"`
	SkillIDs []string `json:"skill_ids"`
	IsLeader bool     `json:"is_leader,omitempty"`
}

type CapabilitySkillResult struct {
	SkillID string `json:"skill_id"`
	Name    string `json:"name"`
	Created bool   `json:"created"`
}
