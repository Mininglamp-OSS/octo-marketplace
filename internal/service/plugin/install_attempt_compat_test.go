package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/fleet"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	pluginrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/plugin"
)

type historicalPinnedRequest struct {
	Definition historicalPinnedDefinition `json:"definition"`
	Bindings   fleet.CapabilityBindings   `json:"bindings"`
}

type historicalPinnedDefinition struct {
	SchemaVersion string                      `json:"schema_version"`
	Name          string                      `json:"name"`
	Skills        []historicalPinnedSkill     `json:"skills,omitempty"`
	Connectors    []fleet.CapabilityConnector `json:"connectors,omitempty"`
	Experts       []historicalPinnedExpert    `json:"experts"`
	ExpertTeam    *historicalPinnedExpertTeam `json:"expert_team,omitempty"`
}

type historicalPinnedSkill struct {
	Name        string                      `json:"name"`
	Description string                      `json:"description"`
	Content     string                      `json:"content"`
	Config      map[string]any              `json:"config"`
	Files       []fleet.CapabilitySkillFile `json:"files,omitempty"`
}

type historicalPinnedExpert struct {
	Name           string            `json:"name"`
	Description    string            `json:"description"`
	Instructions   string            `json:"instructions"`
	SkillNames     []string          `json:"skill_names,omitempty"`
	ConnectorNames []string          `json:"connector_names,omitempty"`
	CustomEnv      map[string]string `json:"custom_env,omitempty"`
	MCPConfig      json.RawMessage   `json:"mcp_config,omitempty"`
}

type historicalPinnedExpertTeam struct {
	Name         string                       `json:"name"`
	Members      []fleet.CapabilityTeamMember `json:"members"`
	Description  string                       `json:"description"`
	Instructions string                       `json:"instructions"`
}

// These mirror the request DTOs at 3f04192: Skills already carried version
// and source metadata, while Experts and ExpertTeam did not.
type baselinePinnedRequest struct {
	Definition baselinePinnedDefinition `json:"definition"`
	Bindings   fleet.CapabilityBindings `json:"bindings"`
}

type baselinePinnedDefinition struct {
	SchemaVersion string                      `json:"schema_version"`
	Name          string                      `json:"name"`
	Skills        []baselinePinnedSkill       `json:"skills,omitempty"`
	Connectors    []fleet.CapabilityConnector `json:"connectors,omitempty"`
	Experts       []historicalPinnedExpert    `json:"experts"`
	ExpertTeam    *historicalPinnedExpertTeam `json:"expert_team,omitempty"`
}

type baselinePinnedSkill struct {
	Name          string                      `json:"name"`
	Version       *string                     `json:"version"`
	MarketSkillID string                      `json:"market_skill_id,omitempty"`
	Description   string                      `json:"description"`
	Content       string                      `json:"content"`
	Config        map[string]any              `json:"config"`
	Files         []fleet.CapabilitySkillFile `json:"files,omitempty"`
}

type baselinePinnedResource struct {
	name, pluginID, versionID string
	wantAgentID, wantTeamID   string
}

func TestPinnedInstallReplaysSignedHistoricalReceiptWithOldSkillWireShape(t *testing.T) {
	s, f, _ := fixturePinned()
	p := installationParams()
	p.WorkspaceConnectors = false
	attempt, historicalWire := makeHistoricalPinnedAttempt(t, s, p, "expert-a", "expert-v1")
	f.attempts = map[string]*pluginrepo.InstallAttempt{p.WorkspaceID + attempt.KeyDigest: attempt}

	var captureMu sync.Mutex
	var received [][]byte
	var receivedKeys []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read retry body: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		captureMu.Lock()
		received = append(received, append([]byte(nil), body...))
		receivedKeys = append(receivedKeys, r.Header.Get("Idempotency-Key"))
		captureMu.Unlock()
		var in fleet.CapabilityInstallRequest
		if err := json.Unmarshal(body, &in); err != nil {
			t.Errorf("decode retry body: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(historicalInstallResponse(t, in))
	}))
	defer server.Close()
	s.WithCapabilityInstaller(fleet.New(server.URL))

	out, err := s.CreateInstallation(context.Background(), testCaller, "expert-a", p)
	if err != nil {
		t.Fatalf("signed historical receipt retry failed: %v", err)
	}
	captureMu.Lock()
	gotBodies := append([][]byte(nil), received...)
	gotKeys := append([]string(nil), receivedKeys...)
	captureMu.Unlock()
	wantAttemptID := digestInstall([]string{testCaller.SpaceID, testCaller.UID, p.WorkspaceID, attempt.KeyDigest})
	if len(gotBodies) != 1 || !bytes.Equal(gotBodies[0], historicalWire) || !reflect.DeepEqual(gotKeys, []string{wantAttemptID}) {
		t.Fatalf("retry did not send the historical body under the stable attempt key: calls=%d keys=%v", len(gotBodies), gotKeys)
	}
	if out == nil || out.AgentID != "historical-agent" || !out.Replayed {
		t.Fatalf("historical replay result was not returned: %#v", out)
	}
}
func TestPinnedInstallReplaysBaselineHistoricalReceiptWithVersionedSkill(t *testing.T) {
	runBaselineHistoricalPinnedAttempts(t, true)
}

func TestPinnedInstallRecoversUnknownBaselineAttemptWithVersionedSkill(t *testing.T) {
	runBaselineHistoricalPinnedAttempts(t, false)
}

func runBaselineHistoricalPinnedAttempts(t *testing.T, completed bool) {
	t.Helper()
	resources := []baselinePinnedResource{
		{name: "expert", pluginID: "expert-a", versionID: "expert-v1", wantAgentID: "historical-agent"},
		{name: "team", pluginID: "team", versionID: "root-v1", wantTeamID: "historical-team"},
	}
	for _, workspaceConnectors := range []bool{false, true} {
		mode := "default"
		if workspaceConnectors {
			mode = "workspace-connectors"
		}
		for _, resource := range resources {
			t.Run(resource.name+"/"+mode, func(t *testing.T) {
				s, f, _ := fixturePinned()
				p := installationParams()
				p.WorkspaceConnectors = workspaceConnectors
				if workspaceConnectors {
					setPinnedMarketConnector(f, "Shared Connector", pinnedConnectorExtensionFixture())
				}
				attempt, historicalWire := makeBaselineHistoricalPinnedAttempt(t, s, p, resource.pluginID, resource.versionID)
				attempt.AgentID, attempt.TeamID = resource.wantAgentID, resource.wantTeamID
				attempt.Completed = completed
				if completed {
					attempt.State = "SUCCEEDED"
				} else {
					attempt.AgentID, attempt.TeamID = "", ""
					attempt.State = "UNKNOWN"
					attempt.HadUnknown = true
				}
				f.attempts = map[string]*pluginrepo.InstallAttempt{p.WorkspaceID + attempt.KeyDigest: attempt}

				var captureMu sync.Mutex
				var received [][]byte
				var receivedKeys []string
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					body, err := io.ReadAll(r.Body)
					if err != nil {
						t.Errorf("read retry body: %v", err)
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					captureMu.Lock()
					received = append(received, append([]byte(nil), body...))
					receivedKeys = append(receivedKeys, r.Header.Get("Idempotency-Key"))
					captureMu.Unlock()
					var in fleet.CapabilityInstallRequest
					if err := json.Unmarshal(body, &in); err != nil {
						t.Errorf("decode retry body: %v", err)
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					w.Header().Set("Content-Type", "application/json")
					w.Header().Set("Idempotency-Replayed", "true")
					_, _ = w.Write(baselineHistoricalInstallResponse(t, in))
				}))
				defer server.Close()
				s.WithCapabilityInstaller(fleet.New(server.URL))

				out, err := s.CreateInstallation(context.Background(), testCaller, resource.pluginID, p)
				if err != nil {
					t.Fatalf("historical attempt retry failed: %v", err)
				}
				captureMu.Lock()
				gotBodies := append([][]byte(nil), received...)
				gotKeys := append([]string(nil), receivedKeys...)
				captureMu.Unlock()
				wantAttemptID := digestInstall([]string{testCaller.SpaceID, testCaller.UID, p.WorkspaceID, attempt.KeyDigest})
				if len(gotBodies) != 1 || !bytes.Equal(gotBodies[0], historicalWire) || !reflect.DeepEqual(gotKeys, []string{wantAttemptID}) {
					t.Fatalf("retry did not preserve the authenticated baseline body and key: calls=%d keys=%v", len(gotBodies), gotKeys)
				}
				if out == nil || out.AgentID != resource.wantAgentID || out.SquadID != resource.wantTeamID || (completed && !out.Replayed) {
					t.Fatalf("historical IDs were not returned: got=%#v want agent=%q team=%q completed=%t", out, resource.wantAgentID, resource.wantTeamID, completed)
				}
				if !completed {
					recovered := f.attempts[p.WorkspaceID+attempt.KeyDigest]
					if recovered == nil || !recovered.Completed || recovered.AgentID != resource.wantAgentID || recovered.TeamID != resource.wantTeamID {
						t.Fatalf("unknown historical result was not persisted with its original IDs: %#v", recovered)
					}
				}
			})
		}
	}
}

func TestPinnedWorkspaceConnectorModeConflictsWithHistoricalDefaultModeKey(t *testing.T) {
	s, f, _ := fixturePinned()
	rootKey := pluginrepo.InstallVersionKey{PluginID: "expert-a", VersionID: "expert-v1"}
	version := f.versions[rootKey]
	relations, err := model.DecodeSnapshotRelations(version.Relations)
	if err != nil {
		t.Fatal(err)
	}
	version.Relations, err = model.EncodeSnapshotRelations(relationsOfType(relations, "expert_skill"))
	if err != nil {
		t.Fatal(err)
	}
	p := installationParams()
	attempt, _ := makeHistoricalPinnedAttempt(t, s, p, "expert-a", "expert-v1")
	f.attempts = map[string]*pluginrepo.InstallAttempt{p.WorkspaceID + attempt.KeyDigest: attempt}

	var callsMu sync.Mutex
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		callsMu.Lock()
		calls++
		callsMu.Unlock()
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	s.WithCapabilityInstaller(fleet.New(server.URL))
	p.WorkspaceConnectors = true

	if _, err = s.CreateInstallation(context.Background(), testCaller, "expert-a", p); !errors.Is(err, ErrConflict) {
		t.Fatalf("new connector mode did not conflict with the historical default-mode key: %v", err)
	}
	callsMu.Lock()
	callCount := calls
	callsMu.Unlock()
	if callCount != 0 {
		t.Fatalf("conflicting historical key reached Fleet %d times", callCount)
	}
}

func TestLegacyInstallRecoversUnknownDefaultedAttemptInFrozenMode(t *testing.T) {
	for _, noMCP := range []bool{false, true} {
		name := "with_mcp"
		if noMCP {
			name = "without_mcp"
		}
		t.Run(name, func(t *testing.T) {
			s, _, capture, p, attempt := unknownDefaultModeAttempt(t, noMCP)
			beforePlan := append([]byte(nil), attempt.FrozenPlan...)
			beforeIntent, beforeInput, beforeRequest := attempt.IntentDigest, attempt.InputFingerprint, attempt.RequestFingerprint
			beforeRetry := *attempt.RetryUntil

			out, err := s.Install(context.Background(), testCaller, "team", legacyInstallParams(p))
			if err != nil || out == nil || out.AgentID != "" || out.SquadID != "historical-team" {
				t.Fatalf("legacy same-key retry failed: out=%#v err=%v", out, err)
			}
			bodies, keys := capture.snapshot()
			wantAttemptID := digestInstall([]string{testCaller.SpaceID, testCaller.UID, p.WorkspaceID, attempt.KeyDigest})
			if len(bodies) != 2 || !bytes.Equal(bodies[0], bodies[1]) || !reflect.DeepEqual(keys, []string{wantAttemptID, wantAttemptID}) {
				t.Fatalf("recovery changed frozen wire or key: calls=%d keys=%v", len(bodies), keys)
			}
			if !bytes.Equal(attempt.FrozenPlan, beforePlan) || attempt.IntentDigest != beforeIntent ||
				attempt.InputFingerprint != beforeInput || attempt.RequestFingerprint != beforeRequest ||
				attempt.RetryUntil == nil || !attempt.RetryUntil.Equal(beforeRetry) {
				t.Fatal("retry rewrote the frozen attempt, fingerprints, or original deadline")
			}
			if !attempt.Completed || attempt.State != "SUCCEEDED" {
				t.Fatalf("unknown attempt did not complete: state=%q completed=%t", attempt.State, attempt.Completed)
			}
			if noMCP {
				var plan pinnedInstallationPlan
				if err = json.Unmarshal(beforePlan, &plan); err != nil {
					t.Fatal(err)
				}
				if len(plan.Definition.Connectors) != 0 {
					t.Fatal("no-MCP fixture unexpectedly froze Workspace connectors")
				}
				for _, expert := range plan.Definition.Experts {
					if len(expert.ConnectorNames) != 0 || len(expert.MCPConfig) != 0 {
						t.Fatal("no-MCP fixture unexpectedly froze expert connectors")
					}
				}
			}
		})
	}
}

func TestLegacyInstallRecoveryRequiresExactDefaultedIntent(t *testing.T) {
	s, _, capture, p, _ := unknownDefaultModeAttempt(t, true)
	explicit := p
	explicit.WorkspaceConnectors = true
	if _, err := s.CreateInstallation(context.Background(), testCaller, "team", explicit); !errors.Is(err, ErrConflict) {
		t.Fatalf("explicit Workspace connectors selection recovered a default-mode attempt: %v", err)
	}

	changedRuntime := legacyInstallParams(p)
	changedRuntime.RuntimeID = "runtime:changed"
	if _, err := s.Install(context.Background(), testCaller, "team", changedRuntime); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed runtime recovered a frozen attempt: %v", err)
	}
	changedEnv := legacyInstallParams(p)
	changedEnv.CustomEnv = map[string]string{"OCTOBUDDY_PROVIDER_ID": "provider-changed"}
	if _, err := s.Install(context.Background(), testCaller, "team", changedEnv); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed custom environment recovered a frozen attempt: %v", err)
	}
	otherSpace := testCaller
	otherSpace.SpaceID = "other-space"
	if _, err := s.Install(context.Background(), otherSpace, "team", legacyInstallParams(p)); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed caller scope recovered a frozen attempt: %v", err)
	}
	bodies, _ := capture.snapshot()
	if len(bodies) != 1 {
		t.Fatalf("conflicting retries reached Fleet: calls=%d", len(bodies))
	}

	if _, err := s.Install(context.Background(), testCaller, "team", legacyInstallParams(p)); err != nil {
		t.Fatalf("matching defaulted retry was rejected after conflicts: %v", err)
	}
	bodies, keys := capture.snapshot()
	if len(bodies) != 2 || !bytes.Equal(bodies[0], bodies[1]) || keys[0] != keys[1] {
		t.Fatalf("matching retry changed the original request: calls=%d keys=%v", len(bodies), keys)
	}
}

func TestLegacyInstallModeRecoveryRechecksFrozenAttemptGuards(t *testing.T) {
	for _, scenario := range []string{"tampered_eligibility", "revoked_dependency", "expired_deadline"} {
		t.Run(scenario, func(t *testing.T) {
			s, f, capture, p, attempt := unknownDefaultModeAttempt(t, false)
			switch scenario {
			case "tampered_eligibility":
				var plan pinnedInstallationPlan
				if err := json.Unmarshal(attempt.FrozenPlan, &plan); err != nil {
					t.Fatal(err)
				}
				plan.EligibilityHMAC = "tampered"
				attempt.FrozenPlan, _ = json.Marshal(plan)
			case "revoked_dependency":
				f.plugins["connector"].ListingState = model.PluginListingStateDelisted
			case "expired_deadline":
				expired := time.Now().Add(-time.Second)
				attempt.RetryUntil = &expired
			}

			_, err := s.Install(context.Background(), testCaller, "team", legacyInstallParams(p))
			want := ErrIntegrity
			if scenario == "revoked_dependency" {
				want = ErrDependencyHidden
			} else if scenario == "expired_deadline" {
				want = ErrAttemptExpired
			}
			if !errors.Is(err, want) {
				t.Fatalf("guard %s was bypassed: got %v, want %v", scenario, err, want)
			}
			bodies, _ := capture.snapshot()
			if len(bodies) != 1 {
				t.Fatalf("guarded recovery reached Fleet: calls=%d", len(bodies))
			}
			if scenario == "expired_deadline" && attempt.State != "EXPIRED" {
				t.Fatalf("expired attempt state=%q, want EXPIRED", attempt.State)
			}
		})
	}
}

func TestLegacyInstallModeRecoveryUsesConcurrentClaimWinner(t *testing.T) {
	s, f, capture, p, winner := unknownDefaultModeAttempt(t, false)
	key := p.WorkspaceID + winner.KeyDigest
	delete(f.attempts, key)
	raceStore := &installAttemptClaimRaceStore{
		pinnedFixtureStore: f,
		winner:             winner,
		hideFirstGet:       true,
	}
	s.repo = raceStore

	out, err := s.Install(context.Background(), testCaller, "team", legacyInstallParams(p))
	if err != nil || out == nil || out.SquadID != "historical-team" {
		t.Fatalf("retry against concurrent old-mode winner failed: out=%#v err=%v", out, err)
	}
	bodies, keys := capture.snapshot()
	wantAttemptID := digestInstall([]string{testCaller.SpaceID, testCaller.UID, p.WorkspaceID, winner.KeyDigest})
	if raceStore.claimCalls != 1 || len(bodies) != 2 || !bytes.Equal(bodies[0], bodies[1]) ||
		!reflect.DeepEqual(keys, []string{wantAttemptID, wantAttemptID}) {
		t.Fatalf("claim winner was not reused under the original wire/key: claims=%d calls=%d keys=%v", raceStore.claimCalls, len(bodies), keys)
	}
	if f.attempts[key] != winner || !winner.Completed {
		t.Fatal("concurrent winner was replaced instead of completed")
	}
}

type installAttemptWireCapture struct {
	mu     sync.Mutex
	bodies [][]byte
	keys   []string
}

func (c *installAttemptWireCapture) snapshot() ([][]byte, []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	bodies := make([][]byte, len(c.bodies))
	for i := range c.bodies {
		bodies[i] = append([]byte(nil), c.bodies[i]...)
	}
	return bodies, append([]string(nil), c.keys...)
}

type installAttemptClaimRaceStore struct {
	*pinnedFixtureStore
	winner       *pluginrepo.InstallAttempt
	hideFirstGet bool
	claimCalls   int
}

func (f *installAttemptClaimRaceStore) GetInstallAttempt(ctx context.Context, sc pluginrepo.Scope, workspace, key string) (*pluginrepo.InstallAttempt, error) {
	if f.hideFirstGet {
		f.hideFirstGet = false
		return nil, nil
	}
	return f.pinnedFixtureStore.GetInstallAttempt(ctx, sc, workspace, key)
}

func (f *installAttemptClaimRaceStore) ClaimInstallAttempt(ctx context.Context, sc pluginrepo.Scope, candidate pluginrepo.InstallAttempt) (*pluginrepo.InstallAttempt, error) {
	f.claimCalls++
	if f.winner != nil {
		winner := f.winner
		f.pinnedFixtureStore.attempts[candidate.WorkspaceID+candidate.KeyDigest] = winner
		f.winner = nil
		return winner, nil
	}
	return f.pinnedFixtureStore.ClaimInstallAttempt(ctx, sc, candidate)
}

func unknownDefaultModeAttempt(t *testing.T, noMCP bool) (*Service, *pinnedFixtureStore, *installAttemptWireCapture, InstallationParams, *pluginrepo.InstallAttempt) {
	t.Helper()
	s, f, _ := fixturePinned()
	p := installationParams()
	p.ResourceName = ""
	if noMCP {
		for _, id := range []string{"expert-a", "expert-b"} {
			key := pluginrepo.InstallVersionKey{PluginID: id, VersionID: "expert-v1"}
			version := f.versions[key]
			relations, err := model.DecodeSnapshotRelations(version.Relations)
			if err != nil {
				t.Fatal(err)
			}
			version.Relations, err = model.EncodeSnapshotRelations(relationsOfType(relations, "expert_skill"))
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	capture := &installAttemptWireCapture{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read install body: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		capture.mu.Lock()
		capture.bodies = append(capture.bodies, append([]byte(nil), body...))
		capture.keys = append(capture.keys, r.Header.Get("Idempotency-Key"))
		call := len(capture.bodies)
		capture.mu.Unlock()
		if call == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		var in fleet.CapabilityInstallRequest
		if err = json.Unmarshal(body, &in); err != nil {
			t.Errorf("decode install body: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Idempotency-Replayed", "true")
		_, _ = w.Write(baselineHistoricalInstallResponse(t, in))
	}))
	t.Cleanup(server.Close)
	s.WithCapabilityInstaller(fleet.New(server.URL))

	_, err := s.CreateInstallation(context.Background(), testCaller, "team", p)
	var attemptErr *InstallationAttemptError
	if !errors.As(err, &attemptErr) || !attemptErr.Unknown {
		t.Fatalf("initial Fleet result was not unknown: %v", err)
	}
	attempt := f.attempts[p.WorkspaceID+digestInstall(p.IdempotencyKey)]
	if attempt == nil || attempt.RetryUntil == nil || !attempt.HadUnknown {
		t.Fatalf("unknown pinned attempt was not persisted: %#v", attempt)
	}
	bodies, keys := capture.snapshot()
	if len(bodies) != 1 || len(keys) != 1 {
		t.Fatalf("initial dispatch count=%d keys=%v", len(bodies), keys)
	}
	return s, f, capture, p, attempt
}

func legacyInstallParams(p InstallationParams) InstallParams {
	return InstallParams{
		WorkspaceID: p.WorkspaceID, RuntimeID: p.RuntimeID, CustomEnv: p.CustomEnv,
		IdempotencyKey: p.IdempotencyKey, Token: p.Token,
	}
}

func makeHistoricalPinnedAttempt(t *testing.T, s *Service, p InstallationParams, pluginID, rootVersionID string) (*pluginrepo.InstallAttempt, []byte) {
	t.Helper()
	plan, err := s.resolvePinnedInstallation(context.Background(), testCaller, pluginID, rootVersionID, false)
	if err != nil {
		t.Fatalf("resolve historical request fixture: %v", err)
	}
	for i := range plan.Definition.Skills {
		plan.Definition.Skills[i].Version = nil
		plan.Definition.Skills[i].MarketSkillID = ""
	}
	request, err := pinnedPlanRequest(plan, p)
	if err != nil {
		t.Fatalf("build historical request fixture: %v", err)
	}
	oldDefinition := historicalPinnedDefinition{
		SchemaVersion: request.Definition.SchemaVersion,
		Name:          request.Definition.Name,
		Connectors:    request.Definition.Connectors,
	}
	for _, expert := range request.Definition.Experts {
		oldDefinition.Experts = append(oldDefinition.Experts, historicalPinnedExpert{
			Name: expert.Name, Description: expert.Description, Instructions: expert.Instructions,
			SkillNames: expert.SkillNames, ConnectorNames: expert.ConnectorNames,
			CustomEnv: expert.CustomEnv, MCPConfig: expert.MCPConfig,
		})
	}
	if team := request.Definition.ExpertTeam; team != nil {
		oldDefinition.ExpertTeam = &historicalPinnedExpertTeam{
			Name: team.Name, Members: team.Members,
			Description: team.Description, Instructions: team.Instructions,
		}
	}
	oldRequest := historicalPinnedRequest{Definition: oldDefinition, Bindings: request.Bindings}
	for _, skill := range request.Definition.Skills {
		oldRequest.Definition.Skills = append(oldRequest.Definition.Skills, historicalPinnedSkill{
			Name: skill.Name, Description: skill.Description, Content: skill.Content,
			Config: skill.Config, Files: skill.Files,
		})
	}
	wire, err := json.Marshal(oldRequest)
	if err != nil {
		t.Fatalf("marshal historical request: %v", err)
	}
	var decoded struct {
		Definition struct {
			Skills     []map[string]json.RawMessage `json:"skills"`
			Experts    []map[string]json.RawMessage `json:"experts"`
			ExpertTeam map[string]json.RawMessage   `json:"expert_team"`
		} `json:"definition"`
	}
	if err := json.Unmarshal(wire, &decoded); err != nil || len(decoded.Definition.Skills) != len(request.Definition.Skills) {
		t.Fatalf("historical payload is not a real install request: err=%v skills=%d", err, len(decoded.Definition.Skills))
	}
	for _, skill := range decoded.Definition.Skills {
		if _, exists := skill["version"]; exists {
			t.Fatal("historical fixture contains the later Skill version field")
		}
		if _, exists := skill["market_skill_id"]; exists {
			t.Fatal("historical fixture contains the later Skill source field")
		}
	}
	for _, expert := range decoded.Definition.Experts {
		if _, exists := expert["version"]; exists {
			t.Fatal("historical fixture contains the later Expert version field")
		}
		if _, exists := expert["market_expert_id"]; exists {
			t.Fatal("historical fixture contains the later Expert source field")
		}
	}
	if _, exists := decoded.Definition.ExpertTeam["version"]; exists {
		t.Fatal("historical fixture contains the later ExpertTeam version field")
	}
	if _, exists := decoded.Definition.ExpertTeam["market_expert_team_id"]; exists {
		t.Fatal("historical fixture contains the later ExpertTeam source field")
	}
	intent := digestInstall(struct {
		Root, Space, Workspace, Runtime, Name string
		WorkspaceConnectors                   bool `json:"workspace_connectors,omitempty"`
	}{pluginID, testCaller.SpaceID, p.WorkspaceID, p.RuntimeID, p.ResourceName, p.WorkspaceConnectors})
	inputFingerprint, err := s.installationHMAC(s.installHMACKeyID, "input", struct {
		Intent string
		Env    map[string]string
	}{intent, p.CustomEnv})
	if err != nil {
		t.Fatalf("sign historical input: %v", err)
	}
	requestFingerprint, err := s.installationHMAC(s.installHMACKeyID, "request", json.RawMessage(wire))
	if err != nil {
		t.Fatalf("sign historical request: %v", err)
	}
	key := digestInstall(p.IdempotencyKey)
	retryUntil := time.Now().UTC().Add(time.Hour)
	attempt := &pluginrepo.InstallAttempt{
		WorkspaceID: p.WorkspaceID, KeyDigest: key, PluginID: pluginID, RootVersionID: rootVersionID,
		AdapterVersion: SkillInstallAdapterVersion, InputFingerprint: inputFingerprint,
		RequestFingerprint: requestFingerprint, HMACKeyID: s.installHMACKeyID, IntentDigest: intent,
		AgentID: "historical-agent", Completed: true, RetryUntil: &retryUntil, CreatedAt: time.Now().UTC(),
	}
	plan.EligibilityHMAC, err = s.installationEligibilityHMAC(testCaller, *attempt, plan)
	if err != nil {
		t.Fatalf("sign historical eligibility: %v", err)
	}
	attempt.FrozenPlan, err = json.Marshal(plan)
	if err != nil {
		t.Fatalf("marshal historical frozen plan: %v", err)
	}
	return attempt, wire
}
func makeBaselineHistoricalPinnedAttempt(t *testing.T, s *Service, p InstallationParams, pluginID, rootVersionID string) (*pluginrepo.InstallAttempt, []byte) {
	t.Helper()
	store := s.repo.(*pinnedFixtureStore)
	for _, skillID := range []string{"skill-a", "skill-b"} {
		versionKey := pluginrepo.InstallVersionKey{PluginID: skillID, VersionID: "skill-v1"}
		store.versions[versionKey].DeclaredVersion = pin("1.4.2")
	}
	plan, err := s.resolvePinnedInstallation(context.Background(), testCaller, pluginID, rootVersionID, p.WorkspaceConnectors)
	if err != nil {
		t.Fatalf("resolve baseline request fixture: %v", err)
	}
	request, err := pinnedPlanRequest(plan, p)
	if err != nil {
		t.Fatalf("build baseline request fixture: %v", err)
	}
	baselineDefinition := baselinePinnedDefinition{
		SchemaVersion: request.Definition.SchemaVersion,
		Name:          request.Definition.Name,
		Connectors:    request.Definition.Connectors,
	}
	for _, expert := range request.Definition.Experts {
		baselineDefinition.Experts = append(baselineDefinition.Experts, historicalPinnedExpert{
			Name: expert.Name, Description: expert.Description, Instructions: expert.Instructions,
			SkillNames: expert.SkillNames, ConnectorNames: expert.ConnectorNames,
			CustomEnv: expert.CustomEnv, MCPConfig: expert.MCPConfig,
		})
	}
	if team := request.Definition.ExpertTeam; team != nil {
		baselineDefinition.ExpertTeam = &historicalPinnedExpertTeam{
			Name: team.Name, Members: team.Members,
			Description: team.Description, Instructions: team.Instructions,
		}
	}
	for _, skill := range request.Definition.Skills {
		baselineDefinition.Skills = append(baselineDefinition.Skills, baselinePinnedSkill{
			Name: skill.Name, Version: skill.Version, MarketSkillID: skill.MarketSkillID,
			Description: skill.Description, Content: skill.Content, Config: skill.Config, Files: skill.Files,
		})
	}
	wire, err := json.Marshal(baselinePinnedRequest{Definition: baselineDefinition, Bindings: request.Bindings})
	if err != nil {
		t.Fatalf("marshal baseline request: %v", err)
	}
	var decoded struct {
		Definition struct {
			Skills     []map[string]json.RawMessage `json:"skills"`
			Experts    []map[string]json.RawMessage `json:"experts"`
			ExpertTeam map[string]json.RawMessage   `json:"expert_team"`
		} `json:"definition"`
	}
	if err := json.Unmarshal(wire, &decoded); err != nil || len(decoded.Definition.Skills) == 0 {
		t.Fatalf("baseline fixture is not an install request: err=%v skills=%d", err, len(decoded.Definition.Skills))
	}
	for _, skill := range decoded.Definition.Skills {
		var version, marketSkillID string
		if json.Unmarshal(skill["version"], &version) != nil || version != "1.4.2" ||
			json.Unmarshal(skill["market_skill_id"], &marketSkillID) != nil || marketSkillID == "" {
			t.Fatalf("baseline fixture lost Skill version/source metadata: %v", skill)
		}
	}
	for _, expert := range decoded.Definition.Experts {
		if _, exists := expert["version"]; exists {
			t.Fatal("baseline fixture contains the later Expert version field")
		}
		if _, exists := expert["market_expert_id"]; exists {
			t.Fatal("baseline fixture contains the later Expert source field")
		}
	}
	if _, exists := decoded.Definition.ExpertTeam["version"]; exists {
		t.Fatal("baseline fixture contains the later ExpertTeam version field")
	}
	if _, exists := decoded.Definition.ExpertTeam["market_expert_team_id"]; exists {
		t.Fatal("baseline fixture contains the later ExpertTeam source field")
	}
	intent := digestInstall(struct {
		Root, Space, Workspace, Runtime, Name string
		WorkspaceConnectors                   bool `json:"workspace_connectors,omitempty"`
	}{pluginID, testCaller.SpaceID, p.WorkspaceID, p.RuntimeID, p.ResourceName, p.WorkspaceConnectors})
	inputFingerprint, err := s.installationHMAC(s.installHMACKeyID, "input", struct {
		Intent string
		Env    map[string]string
	}{intent, p.CustomEnv})
	if err != nil {
		t.Fatalf("sign baseline input: %v", err)
	}
	requestFingerprint, err := s.installationHMAC(s.installHMACKeyID, "request", json.RawMessage(wire))
	if err != nil {
		t.Fatalf("sign baseline request: %v", err)
	}
	key := digestInstall(p.IdempotencyKey)
	retryUntil := time.Now().UTC().Add(time.Hour)
	attempt := &pluginrepo.InstallAttempt{
		WorkspaceID: p.WorkspaceID, KeyDigest: key, PluginID: pluginID, RootVersionID: rootVersionID,
		AdapterVersion: SkillInstallAdapterVersion, InputFingerprint: inputFingerprint, RequestFingerprint: requestFingerprint,
		HMACKeyID: s.installHMACKeyID, IntentDigest: intent,
		AgentID: "historical-agent", TeamID: "historical-team", Completed: true, RetryUntil: &retryUntil, CreatedAt: time.Now().UTC(),
	}
	plan.EligibilityHMAC, err = s.installationEligibilityHMAC(testCaller, *attempt, plan)
	if err != nil {
		t.Fatalf("sign baseline eligibility: %v", err)
	}
	attempt.FrozenPlan, err = json.Marshal(plan)
	if err != nil {
		t.Fatalf("marshal baseline frozen plan: %v", err)
	}
	return attempt, wire
}

func baselineHistoricalInstallResponse(t *testing.T, in fleet.CapabilityInstallRequest) []byte {
	t.Helper()
	var skills []fleet.CapabilitySkillResult
	skillIDs := make(map[string]string, len(in.Definition.Skills))
	for i, skill := range in.Definition.Skills {
		id := "historical-skill-" + string(rune('a'+i))
		skills = append(skills, fleet.CapabilitySkillResult{
			SkillID: id, Name: skill.Name, Version: skill.Version, MarketSkillID: skill.MarketSkillID, Created: true,
		})
		skillIDs[skill.Name] = id
	}
	var connectors []fleet.CapabilityConnectorResult
	connectorIDs := make(map[string]string, len(in.Definition.Connectors))
	for i, connector := range in.Definition.Connectors {
		id := fmt.Sprintf("11111111-1111-4111-8111-%012x", i+1)
		var marketMCPID *string
		if connector.MarketMCPID != "" {
			marketMCPID = &in.Definition.Connectors[i].MarketMCPID
		}
		connectors = append(connectors, fleet.CapabilityConnectorResult{
			ConnectorID: id, Name: connector.Name, Version: connector.Version,
			MarketMCPID: marketMCPID, Created: true,
		})
		connectorIDs[connector.Name] = id
	}
	result := fleet.CapabilityInstallResult{Type: "expert", ExpertID: "historical-agent", Skills: skills, Connectors: connectors}
	if in.Definition.ExpertTeam != nil {
		result.Type, result.ExpertID, result.ExpertTeamID = "expert_team", "", "historical-team"
	}
	experts := make([]fleet.CapabilityExpertResult, 0, len(in.Definition.Experts))
	leaderID := ""
	for i, expert := range in.Definition.Experts {
		id := "historical-agent"
		if in.Definition.ExpertTeam != nil {
			id += "-" + string(rune('a'+i))
		}
		var boundSkills []string
		for _, name := range expert.SkillNames {
			boundSkills = append(boundSkills, skillIDs[name])
		}
		var boundConnectors []string
		for _, name := range expert.ConnectorNames {
			boundConnectors = append(boundConnectors, connectorIDs[name])
		}
		isLeader := false
		if in.Definition.ExpertTeam != nil {
			for _, member := range in.Definition.ExpertTeam.Members {
				if member.ExpertName == expert.Name && member.IsLeader {
					isLeader = true
					leaderID = id
				}
			}
		}
		experts = append(experts, fleet.CapabilityExpertResult{
			ExpertID: id, Name: expert.Name, SkillIDs: boundSkills, ConnectorIDs: boundConnectors,
			IsLeader: isLeader, Created: true,
		})
	}
	result.Experts = experts
	if in.Definition.ExpertTeam != nil {
		result.LeaderExpertID = leaderID
	}
	payload, err := json.Marshal(map[string]any{"data": result})
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func historicalInstallResponse(t *testing.T, in fleet.CapabilityInstallRequest) []byte {
	t.Helper()
	var skills []fleet.CapabilitySkillResult
	skillIDs := make(map[string]string, len(in.Definition.Skills))
	for i, skill := range in.Definition.Skills {
		id := "historical-skill-" + string(rune('a'+i))
		skills = append(skills, fleet.CapabilitySkillResult{SkillID: id, Name: skill.Name})
		skillIDs[skill.Name] = id
	}
	experts := make([]fleet.CapabilityExpertResult, 0, len(in.Definition.Experts))
	for _, expert := range in.Definition.Experts {
		var boundSkills []string
		for _, name := range expert.SkillNames {
			boundSkills = append(boundSkills, skillIDs[name])
		}
		experts = append(experts, fleet.CapabilityExpertResult{ExpertID: "historical-agent", Name: expert.Name, SkillIDs: boundSkills})
	}
	result := fleet.CapabilityInstallResult{Type: "expert", ExpertID: "historical-agent", Experts: experts, Skills: skills}
	payload, err := json.Marshal(map[string]any{"data": result})
	if err != nil {
		t.Fatal(err)
	}
	return payload
}
func TestPinnedPlanRequestCanonicalizesOpaqueJSONAfterFrozenPlanJSONReorder(t *testing.T) {
	const (
		mcpBefore       = `{"extensions":{"z":2,"large":9007199254740993,"a":1},"mcpServers":{"server":{"z":3,"large":9007199254740995,"a":4}}}`
		mcpAfter        = `{"mcpServers":{"server":{"a":4,"large":9007199254740995,"z":3}},"extensions":{"a":1,"large":9007199254740993,"z":2}}`
		connectorBefore = `{"mcpServers":{"server":{"z":6,"large":9007199254740997,"a":5}},"extensions":{"z":8,"large":9007199254740999,"a":7}}`
		connectorAfter  = `{"extensions":{"a":7,"large":9007199254740999,"z":8},"mcpServers":{"server":{"a":5,"large":9007199254740997,"z":6}}}`
	)
	p := InstallationParams{RuntimeID: "550e8400-e29b-41d4-a716-446655440000", WorkspaceConnectors: true}
	makePlan := func(mcpConfig, connectorConfig string) pinnedInstallationPlan {
		return pinnedInstallationPlan{Definition: fleet.CapabilityDefinition{
			SchemaVersion: "1.0",
			Name:          "marketplace:root",
			Connectors: []fleet.CapabilityConnector{{
				Name: "Shared", MarketMCPID: "connector-1", Config: json.RawMessage(connectorConfig),
			}},
			Experts: []fleet.CapabilityExpert{{
				Name: "Expert", Instructions: "instructions", ConnectorNames: []string{"Shared"},
				MCPConfig: json.RawMessage(mcpConfig),
			}},
		}}
	}
	marshal := func(plan pinnedInstallationPlan) []byte {
		t.Helper()
		request, err := pinnedPlanRequest(&plan, p)
		if err != nil {
			t.Fatalf("build pinned request: %v", err)
		}
		wire, err := fleet.MarshalCapabilityInstallRequest(*request)
		if err != nil {
			t.Fatalf("marshal pinned request: %v", err)
		}
		return wire
	}
	before := marshal(makePlan(mcpBefore, connectorBefore))
	after := marshal(makePlan(mcpAfter, connectorAfter))
	if !bytes.Equal(before, after) {
		t.Fatalf("equivalent frozen JSON objects changed the Fleet request bytes:\nbefore: %s\nafter:  %s", before, after)
	}
	for _, number := range []string{"9007199254740993", "9007199254740995", "9007199254740997", "9007199254740999"} {
		if !bytes.Contains(before, []byte(number)) {
			t.Errorf("canonical request lost exact extension integer %s: %s", number, before)
		}
	}
}
