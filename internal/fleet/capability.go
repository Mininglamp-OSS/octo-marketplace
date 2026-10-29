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
	if json.Unmarshal(raw, &envelope) != nil || envelope.Data == nil ||
		(len(in.Definition.Connectors) > 0 && !capabilityConnectorNullableFieldsPresent(raw)) ||
		!validCapabilityResult(in.Definition, *envelope.Data, in.LegacySkillWire) {
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

func capabilityConnectorNullableFieldsPresent(raw []byte) bool {
	var envelope struct {
		Data struct {
			Connectors []struct {
				Version     json.RawMessage `json:"version"`
				MarketMCPID json.RawMessage `json:"market_mcp_id"`
			} `json:"connectors"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &envelope) != nil {
		return false
	}
	for _, connector := range envelope.Data.Connectors {
		if connector.Version == nil || connector.MarketMCPID == nil {
			return false
		}
	}
	return true
}

func validCapabilityResult(def CapabilityDefinition, out CapabilityInstallResult, legacySkillWire bool) bool {
	connectorIDs, ok := validCapabilityConnectorResults(out.Connectors)
	if !ok {
		return false
	}
	if len(def.Experts) == 0 {
		return validCapabilityConnectorOnlyResult(def, out)
	}
	if len(out.Experts) == 0 {
		return false
	}
	expertsByID := make(map[string]CapabilityExpertResult, len(out.Experts))
	for _, expert := range out.Experts {
		if expert.ExpertID == "" || expert.Name == "" {
			return false
		}
		if _, exists := expertsByID[expert.ExpertID]; exists {
			return false
		}
		expertsByID[expert.ExpertID] = expert
	}
	skillsByID := make(map[string]CapabilitySkillResult, len(out.Skills))
	for _, skill := range out.Skills {
		if skill.SkillID == "" || skill.Name == "" {
			return false
		}
		if _, exists := skillsByID[skill.SkillID]; exists {
			return false
		}
		skillsByID[skill.SkillID] = skill
	}
	for _, expert := range out.Experts {
		seenSkills := make(map[string]bool, len(expert.SkillIDs))
		for _, id := range expert.SkillIDs {
			if _, exists := skillsByID[id]; !exists || seenSkills[id] {
				return false
			}
			seenSkills[id] = true
		}
		seenConnectors := make(map[string]bool, len(expert.ConnectorIDs))
		for _, id := range expert.ConnectorIDs {
			if _, exists := connectorIDs[id]; !exists || seenConnectors[id] {
				return false
			}
			seenConnectors[id] = true
		}
	}
	if def.ExpertTeam == nil {
		if out.Type != "expert" || len(def.Experts) != 1 || out.ExpertID == "" ||
			out.ExpertTeamID != "" || out.LeaderExpertID != "" || out.ExpertTeam != nil {
			return false
		}
		root, exists := expertsByID[out.ExpertID]
		if !exists || root.Name != def.Experts[0].Name {
			return false
		}
		if !legacySkillWire && !sameCapabilityNullableString(root.Version, def.Experts[0].Version) {
			return false
		}
		if !legacySkillWire && root.Created &&
			(root.MarketExpertID != def.Experts[0].MarketExpertID ||
				!validCreatedExpertBindings(def.Experts[0], root, def.Skills, skillsByID, def.Connectors, connectorIDs)) {
			return false
		}
		return true
	}
	if out.Type != "expert_team" || out.ExpertTeamID == "" || out.ExpertID != "" || out.LeaderExpertID == "" {
		return false
	}
	if _, exists := expertsByID[out.LeaderExpertID]; !exists {
		return false
	}
	for _, expert := range out.Experts {
		if expert.IsLeader && expert.ExpertID != out.LeaderExpertID {
			return false
		}
	}
	team := out.ExpertTeam
	if !legacySkillWire {
		if team == nil {
			if def.ExpertTeam.Version != nil {
				return false
			}
		} else if team.ExpertTeamID != out.ExpertTeamID || team.Name != def.ExpertTeam.Name ||
			!sameCapabilityNullableString(team.Version, def.ExpertTeam.Version) {
			return false
		} else if team.Created && team.MarketExpertTeamID != def.ExpertTeam.MarketExpertTeamID {
			return false
		}
		if team != nil && team.Created &&
			!validCreatedTeamGraph(def, out, skillsByID, connectorIDs) {
			return false
		}
	}
	return true
}

type capabilityResourceIdentity struct {
	name       string
	version    string
	hasVersion bool
}

func capabilityIdentity(name string, version *string) capabilityResourceIdentity {
	if version == nil {
		return capabilityResourceIdentity{name: name}
	}
	return capabilityResourceIdentity{name: name, version: *version, hasVersion: true}
}

func validCreatedTeamGraph(def CapabilityDefinition, out CapabilityInstallResult, skillsByID map[string]CapabilitySkillResult, connectorsByID map[string]CapabilityConnectorResult) bool {
	if len(out.Experts) != len(def.Experts) {
		return false
	}
	wantedByName := make(map[string]CapabilityExpert, len(def.Experts))
	leadersByName := make(map[string]bool, len(def.Experts))
	for _, expert := range def.Experts {
		if expert.Name == "" {
			return false
		}
		if _, exists := wantedByName[expert.Name]; exists {
			return false
		}
		wantedByName[expert.Name] = expert
	}
	leaderCount := 0
	for _, member := range def.ExpertTeam.Members {
		if _, exists := wantedByName[member.ExpertName]; !exists {
			return false
		}
		if _, exists := leadersByName[member.ExpertName]; exists {
			return false
		}
		leadersByName[member.ExpertName] = member.IsLeader
		if member.IsLeader {
			leaderCount++
		}
	}
	if len(leadersByName) != len(wantedByName) || leaderCount != 1 {
		return false
	}
	leaderID := ""
	for _, actual := range out.Experts {
		wanted, exists := wantedByName[actual.Name]
		if !exists || !sameCapabilityNullableString(actual.Version, wanted.Version) {
			return false
		}
		if leadersByName[actual.Name] {
			leaderID = actual.ExpertID
		}
		if actual.Created && (actual.MarketExpertID != wanted.MarketExpertID ||
			!validCreatedExpertBindings(wanted, actual, def.Skills, skillsByID, def.Connectors, connectorsByID)) {
			return false
		}
		delete(wantedByName, actual.Name)
	}
	return len(wantedByName) == 0 && leaderID == out.LeaderExpertID
}

func validCreatedExpertBindings(wanted CapabilityExpert, actual CapabilityExpertResult, skills []CapabilitySkill, skillsByID map[string]CapabilitySkillResult, connectors []CapabilityConnector, connectorsByID map[string]CapabilityConnectorResult) bool {
	expectedSkills := make(map[capabilityResourceIdentity]CapabilitySkill, len(wanted.SkillNames))
	for _, name := range wanted.SkillNames {
		matches := 0
		for _, skill := range skills {
			if skill.Name == name {
				expectedSkills[capabilityIdentity(skill.Name, skill.Version)] = skill
				matches++
			}
		}
		if matches != 1 {
			return false
		}
	}
	actualSkills := make(map[capabilityResourceIdentity]bool, len(actual.SkillIDs))
	for _, id := range actual.SkillIDs {
		skill, exists := skillsByID[id]
		if !exists {
			return false
		}
		identity := capabilityIdentity(skill.Name, skill.Version)
		wantedSkill, exists := expectedSkills[identity]
		if !exists || actualSkills[identity] {
			return false
		}
		if skill.Created && skill.MarketSkillID != wantedSkill.MarketSkillID {
			return false
		}
		actualSkills[identity] = true
	}
	if len(actualSkills) != len(expectedSkills) {
		return false
	}

	expectedConnectors := make(map[capabilityResourceIdentity]CapabilityConnector, len(wanted.ConnectorNames))
	for _, name := range wanted.ConnectorNames {
		matches := 0
		for _, connector := range connectors {
			if connector.Name == name {
				expectedConnectors[capabilityIdentity(connector.Name, connector.Version)] = connector
				matches++
			}
		}
		if matches != 1 {
			return false
		}
	}
	actualConnectors := make(map[capabilityResourceIdentity]bool, len(actual.ConnectorIDs))
	for _, id := range actual.ConnectorIDs {
		connector, exists := connectorsByID[id]
		if !exists {
			return false
		}
		identity := capabilityIdentity(connector.Name, connector.Version)
		wantedConnector, exists := expectedConnectors[identity]
		if !exists || actualConnectors[identity] {
			return false
		}
		if connector.Created && !sameCapabilityMarketMCPID(connector.MarketMCPID, wantedConnector.MarketMCPID) {
			return false
		}
		actualConnectors[identity] = true
	}
	return len(actualConnectors) == len(expectedConnectors)
}

func validCapabilityConnectorOnlyResult(def CapabilityDefinition, out CapabilityInstallResult) bool {
	if len(def.Connectors) == 0 || len(out.Connectors) != len(def.Connectors) || len(def.Skills) != 0 ||
		def.ExpertTeam != nil || out.Type != "connectors" || out.ExpertID != "" ||
		out.ExpertTeamID != "" || out.LeaderExpertID != "" || out.ExpertTeam != nil ||
		len(out.Experts) != 0 || len(out.Skills) != 0 {
		return false
	}
	expected := make(map[capabilityResourceIdentity]CapabilityConnector, len(def.Connectors))
	for _, connector := range def.Connectors {
		identity := capabilityIdentity(connector.Name, connector.Version)
		if connector.Name == "" {
			return false
		}
		if _, exists := expected[identity]; exists {
			return false
		}
		expected[identity] = connector
	}
	for _, connector := range out.Connectors {
		identity := capabilityIdentity(connector.Name, connector.Version)
		wanted, exists := expected[identity]
		if !exists {
			return false
		}
		if connector.Created && !sameCapabilityMarketMCPID(connector.MarketMCPID, wanted.MarketMCPID) {
			return false
		}
		delete(expected, identity)
	}
	return len(expected) == 0
}

func sameCapabilityMarketMCPID(actual *string, wanted string) bool {
	if wanted == "" {
		return actual == nil
	}
	return actual != nil && *actual == wanted
}

func validCapabilityConnectorResults(out []CapabilityConnectorResult) (map[string]CapabilityConnectorResult, bool) {
	ids := make(map[string]CapabilityConnectorResult, len(out))
	for _, connector := range out {
		if !isCapabilityConnectorUUID(connector.ConnectorID) || connector.Name == "" {
			return nil, false
		}
		if _, exists := ids[connector.ConnectorID]; exists {
			return nil, false
		}
		ids[connector.ConnectorID] = connector
	}
	return ids, true
}

func isCapabilityConnectorUUID(value string) bool {
	if len(value) != 36 {
		return false
	}
	for i := range len(value) {
		switch i {
		case 8, 13, 18, 23:
			if value[i] != '-' {
				return false
			}
		default:
			c := value[i]
			if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
				return false
			}
		}
	}
	return true
}

func sameCapabilityNullableString(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}
