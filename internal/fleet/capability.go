package fleet

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// CapabilityInstallTimeout bounds the new installation operation without
// changing the shared legacy client's 30-second timeout.
const CapabilityInstallTimeout = 2 * time.Minute

// CapabilityAPIError retains only status and structured conflict metadata.
// Upstream display text is never retained: it may contain diagnostics or secrets.
type CapabilityAPIError struct {
	Status               int
	Code                 string
	RetryAfter           string
	IdempotencyKeyReused bool
	ConflictResource     string // Only a recognized conflicting Skill/Expert/Team resource kind.
}

func (e *CapabilityAPIError) Error() string { return "Fleet capability installation failed" }

// InstallCapability makes exactly one mutation. The caller must reuse the key
// and identical assembled bytes on retry; an uncertain result never falls back
// to the legacy per-resource installer. c.http also disables redirects.
func (c *Client) InstallCapability(ctx context.Context, token, spaceID, workspaceID, key string, in CapabilityInstallRequest) (*CapabilityInstallResult, error) {
	body, err := MarshalCapabilityInstallRequest(in)
	if err != nil {
		return nil, errors.New("cannot encode capability installation")
	}
	path := "/v1/capabilities/install"
	// Reuse the legacy Fleet base URL. OCTO's /fleet gateway mount exposes
	// public v1 routes under /api/v1; a direct Fleet service exposes /v1.
	// Select before sending: never probe or retry a mutation at another path.
	if base, err := url.Parse(c.baseURL); err == nil && strings.HasSuffix(base.Path, "/fleet") {
		path = "/api" + path
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(body))
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
	// Copy the client policy, not its transport: concurrent legacy calls retain
	// their timeout, redirect protection and connection pool. A shorter caller
	// deadline still wins, including time already spent assembling the assets.
	httpClient := *c.http
	httpClient.Timeout = CapabilityInstallTimeout
	resp, err := httpClient.Do(req)
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
		apiErr := &CapabilityAPIError{Status: resp.StatusCode, Code: envelope.Error.Code, RetryAfter: resp.Header.Get("Retry-After")}
		if resp.StatusCode == http.StatusConflict {
			var conflict struct {
				Error struct {
					Details json.RawMessage `json:"details"`
				} `json:"error"`
			}
			if json.Unmarshal(raw, &conflict) == nil {
				var details struct {
					Resource string `json:"resource"`
				}
				if json.Unmarshal(conflict.Error.Details, &details) == nil {
					apiErr.IdempotencyKeyReused = apiErr.Code == "DUPLICATE" && details.Resource == "idempotency_key"
					switch details.Resource {
					case "skill", "expert", "expert_team":
						apiErr.ConflictResource = details.Resource
					}
				}
			}
		}
		return nil, apiErr
	}
	var envelope struct {
		Data *CapabilityInstallResult `json:"data"`
	}
	if json.Unmarshal(raw, &envelope) != nil || envelope.Data == nil || !validCapabilityResult(in.Definition, *envelope.Data, in.LegacySkillWire) {
		return nil, errors.New("invalid capability installation result; retry with the same idempotency key")
	}
	out := envelope.Data
	// Fleet test currently omits this header even for receipt replays. Missing
	// or ambiguous metadata must not be mistaken for a confirmed new install.
	if values := resp.Header.Values("Idempotency-Replayed"); len(values) == 1 {
		switch values[0] {
		case "true":
			out.ReplayKnown, out.Replayed = true, true
		case "false":
			out.ReplayKnown = true
		}
	}
	return out, nil
}

func validCapabilityResult(def CapabilityDefinition, out CapabilityInstallResult, legacySkillWire bool) bool {
	connectorIDs, ok := validCapabilityConnectorResults(def.Connectors, out.Connectors)
	if !ok {
		return false
	}
	if len(def.Experts) == 0 {
		return len(def.Connectors) > 0 &&
			len(def.Skills) == 0 &&
			def.ExpertTeam == nil &&
			out.Type == "connectors" &&
			out.ExpertID == "" &&
			out.ExpertTeamID == "" &&
			out.LeaderExpertID == "" &&
			len(out.Experts) == 0 &&
			len(out.Skills) == 0
	}
	if out.Type == "connectors" {
		return false
	}
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
	skills := make(map[string]*CapabilitySkillResult, len(out.Skills))
	skillIDs := make(map[string]bool, len(out.Skills))
	for i := range out.Skills {
		skill := &out.Skills[i]
		if skill.SkillID == "" || skills[skill.Name] != nil || skillIDs[skill.SkillID] {
			return false
		}
		skills[skill.Name] = skill
		skillIDs[skill.SkillID] = true
	}
	for _, skill := range def.Skills {
		actual := skills[skill.Name]
		if actual == nil {
			return false
		}
		if !legacySkillWire && (!sameCapabilityNullableString(actual.Version, skill.Version) ||
			actual.MarketSkillID != skill.MarketSkillID) {
			return false
		}
	}
	expectedSkills := make(map[string]map[string]bool, len(def.Experts))
	expectedConnectors := make(map[string]map[string]bool, len(def.Experts))
	for _, expert := range def.Experts {
		if _, exists := expectedConnectors[expert.Name]; exists {
			return false
		}
		skillRefs := make(map[string]bool, len(expert.SkillNames))
		for _, name := range expert.SkillNames {
			if skills[name] == nil {
				return false
			}
			skillRefs[skills[name].SkillID] = true
		}
		expectedSkills[expert.Name] = skillRefs

		connectorRefs := make(map[string]bool, len(expert.ConnectorNames))
		for _, name := range expert.ConnectorNames {
			id := connectorIDs[name]
			if id == "" {
				return false
			}
			connectorRefs[id] = true
		}
		expectedConnectors[expert.Name] = connectorRefs
	}
	for _, expert := range out.Experts {
		skillRefs := expectedSkills[expert.Name]
		if len(skillRefs) != len(expert.SkillIDs) {
			return false
		}
		for _, id := range expert.SkillIDs {
			if !skillRefs[id] {
				return false
			}
			delete(skillRefs, id)
		}
		connectorRefs := expectedConnectors[expert.Name]
		if len(connectorRefs) != len(expert.ConnectorIDs) {
			return false
		}
		for _, id := range expert.ConnectorIDs {
			if !connectorRefs[id] {
				return false
			}
			delete(connectorRefs, id)
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

func validCapabilityConnectorResults(def []CapabilityConnector, out []CapabilityConnectorResult) (map[string]string, bool) {
	if len(def) != len(out) {
		return nil, false
	}
	expected := make(map[string]CapabilityConnector, len(def))
	for _, connector := range def {
		if connector.Name == "" {
			return nil, false
		}
		if _, exists := expected[connector.Name]; exists {
			return nil, false
		}
		expected[connector.Name] = connector
	}

	idsByName := make(map[string]string, len(out))
	seenIDs := make(map[string]bool, len(out))
	for _, connector := range out {
		if connector.ConnectorID == "" || connector.Name == "" || seenIDs[connector.ConnectorID] {
			return nil, false
		}
		wanted, exists := expected[connector.Name]
		if !exists {
			return nil, false
		}
		if _, exists := idsByName[connector.Name]; exists {
			return nil, false
		}
		var wantedMarketMCPID *string
		if wanted.MarketMCPID != "" {
			wantedMarketMCPID = &wanted.MarketMCPID
		}
		if !sameCapabilityNullableString(connector.Version, wanted.Version) ||
			!sameCapabilityNullableString(connector.MarketMCPID, wantedMarketMCPID) {
			return nil, false
		}
		seenIDs[connector.ConnectorID] = true
		idsByName[connector.Name] = connector.ConnectorID
	}
	return idsByName, true
}

func sameCapabilityNullableString(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}
