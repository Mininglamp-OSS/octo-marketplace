package fleet

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

var ErrCapabilityInstallDisabled = errors.New("capability installation is disabled pending Fleet contract confirmation")

// CapabilityAPIError deliberately retains only machine-readable status/code.
// Fleet messages can echo submitted content or environment values.
type CapabilityAPIError struct {
	Status     int
	Code       string
	RetryAfter string
}

func (e *CapabilityAPIError) Error() string { return "Fleet capability installation failed" }

func (c *Client) WithCapabilityInstall(enabled bool) *Client {
	c.capabilityInstallEnabled = enabled
	return c
}

func (c *Client) CapabilityInstallEnabled() bool { return c.capabilityInstallEnabled }

// InstallCapability makes exactly one mutation. The caller must reuse the key
// and identical assembled bytes on retry; an uncertain result never falls back
// to the legacy per-resource installer. c.http also disables redirects.
func (c *Client) InstallCapability(ctx context.Context, token, spaceID, workspaceID, key string, in CapabilityInstallRequest) (*CapabilityInstallResult, error) {
	if !c.CapabilityInstallEnabled() {
		return nil, ErrCapabilityInstallDisabled
	}
	body, err := json.Marshal(in)
	if err != nil {
		return nil, errors.New("cannot encode capability installation")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/capabilities/install", bytes.NewReader(body))
	if err != nil {
		return nil, errors.New("cannot create capability installation request")
	}
	// Go's transport treats Idempotency-Key + GetBody as permission to retry
	// POST on a reused connection. Keep retry ownership with the caller.
	req.GetBody = nil
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Token", token)
	req.Header.Set("X-Space-Id", spaceID)
	req.Header.Set("X-Workspace-Id", workspaceID)
	req.Header.Set("Idempotency-Key", key)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, errors.New("capability installation outcome is unknown; retry with the same idempotency key")
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxRespBytes+1))
	if err != nil || len(raw) > maxRespBytes {
		return nil, errors.New("invalid capability installation response; retry with the same idempotency key")
	}
	if resp.StatusCode != http.StatusOK {
		var envelope struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		_ = json.Unmarshal(raw, &envelope)
		return nil, &CapabilityAPIError{Status: resp.StatusCode, Code: envelope.Error.Code, RetryAfter: resp.Header.Get("Retry-After")}
	}
	var out CapabilityInstallResult
	if json.Unmarshal(raw, &out) != nil || !validCapabilityResult(in.Definition, out) {
		return nil, errors.New("invalid capability installation result; retry with the same idempotency key")
	}
	out.Replayed = resp.Header.Get("Idempotency-Replayed") == "true"
	return &out, nil
}

func validCapabilityResult(def CapabilityDefinition, out CapabilityInstallResult) bool {
	if len(out.Experts) != len(def.Experts) || len(out.Skills) != len(def.Skills) {
		return false
	}
	experts := make(map[string]string, len(out.Experts))
	ids := make(map[string]bool, len(out.Experts))
	for _, expert := range out.Experts {
		if expert.ExpertID == "" || experts[expert.Name] != "" || ids[expert.ExpertID] {
			return false
		}
		experts[expert.Name], ids[expert.ExpertID] = expert.ExpertID, true
	}
	for _, expert := range def.Experts {
		if experts[expert.Name] == "" {
			return false
		}
	}
	skills := make(map[string]string, len(out.Skills))
	skillIDs := make(map[string]bool, len(out.Skills))
	for _, skill := range out.Skills {
		if skill.SkillID == "" || skills[skill.Name] != "" || skillIDs[skill.SkillID] {
			return false
		}
		skills[skill.Name] = skill.SkillID
		skillIDs[skill.SkillID] = true
	}
	for _, skill := range def.Skills {
		if skills[skill.Name] == "" {
			return false
		}
	}
	expectedSkills := make(map[string]map[string]bool, len(def.Experts))
	for _, expert := range def.Experts {
		refs := make(map[string]bool, len(expert.SkillNames))
		for _, name := range expert.SkillNames {
			if skills[name] == "" {
				return false
			}
			refs[skills[name]] = true
		}
		expectedSkills[expert.Name] = refs
	}
	for _, expert := range out.Experts {
		refs := expectedSkills[expert.Name]
		if len(refs) != len(expert.SkillIDs) {
			return false
		}
		for _, id := range expert.SkillIDs {
			if !refs[id] {
				return false
			}
			delete(refs, id)
		}
	}
	if def.ExpertTeam == nil {
		return out.Type == "expert" && len(def.Experts) == 1 && out.ExpertID == experts[def.Experts[0].Name] && out.ExpertTeamID == ""
	}
	if out.Type != "expert_team" || out.ExpertTeamID == "" || out.ExpertID != "" {
		return false
	}
	for _, member := range def.ExpertTeam.Members {
		if member.IsLeader {
			return out.LeaderExpertID == experts[member.ExpertName]
		}
	}
	return false
}
