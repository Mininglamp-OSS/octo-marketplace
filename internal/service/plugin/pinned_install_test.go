package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/fleet"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	pluginrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/plugin"
)

type pinnedFixtureStore struct {
	Store
	plugins                   map[string]*model.Plugin
	versions                  map[pluginrepo.InstallVersionKey]*model.PluginVersion
	state                     model.PluginVersionState
	pluginCalls, versionCalls int
	attempts                  map[string]*pluginrepo.InstallAttempt
	finishErr                 error
}

func (f *pinnedFixtureStore) GetInstallPlugins(_ context.Context, sc pluginrepo.Scope, ids []string) (map[string]*model.Plugin, error) {
	f.pluginCalls++
	out := map[string]*model.Plugin{}
	for _, id := range ids {
		p := f.plugins[id]
		if p == nil || p.IsEmbedded || p.Status != 1 {
			continue
		}
		v := model.NormalizeLegacyVisibility(p.Visibility)
		if v == model.PluginVisibilitySystem || (p.SpaceID != nil && *p.SpaceID == sc.SpaceID && (v == model.PluginVisibilitySpace || (v == model.PluginVisibilityPrivate && p.OwnerUID == sc.CallerUID))) {
			out[id] = p
		}
	}
	return out, nil
}
func (f *pinnedFixtureStore) GetInstallVersions(_ context.Context, _ pluginrepo.Scope, keys []pluginrepo.InstallVersionKey) (map[pluginrepo.InstallVersionKey]*model.PluginVersion, error) {
	f.versionCalls++
	out := map[pluginrepo.InstallVersionKey]*model.PluginVersion{}
	for _, key := range keys {
		if v := f.versions[key]; v != nil && v.Release != nil {
			out[key] = v
		}
	}
	return out, nil
}
func (f *pinnedFixtureStore) GetVersionState(context.Context, pluginrepo.Scope, string) (*model.PluginVersionState, error) {
	return &f.state, nil
}
func (f *pinnedFixtureStore) GetManagedVersion(context.Context, pluginrepo.Scope, string, string) (*model.PluginVersion, error) {
	panic("resolver must batch versions")
}
func (f *pinnedFixtureStore) ListManagedVersions(context.Context, pluginrepo.Scope, string, int, int) ([]model.PluginVersion, int64, error) {
	panic("unused")
}
func (f *pinnedFixtureStore) PublishManaged(context.Context, pluginrepo.Scope, pluginrepo.PublishParams) (*model.Plugin, error) {
	panic("unused")
}
func (f *pinnedFixtureStore) AdoptPublishedBaseline(context.Context, pluginrepo.Scope, pluginrepo.AdoptParams) (*model.Plugin, error) {
	panic("unused")
}

func (f *pinnedFixtureStore) GetInstallAttempt(_ context.Context, _ pluginrepo.Scope, workspace, key string) (*pluginrepo.InstallAttempt, error) {
	return f.attempts[workspace+key], nil
}
func (f *pinnedFixtureStore) ClaimInstallAttempt(_ context.Context, _ pluginrepo.Scope, a pluginrepo.InstallAttempt) (*pluginrepo.InstallAttempt, error) {
	if f.attempts == nil {
		f.attempts = map[string]*pluginrepo.InstallAttempt{}
	}
	key := a.WorkspaceID + a.KeyDigest
	if existing := f.attempts[key]; existing != nil {
		return existing, nil
	}
	a.CreatedAt = time.Now().UTC()
	f.attempts[key] = &a
	return &a, nil
}
func (f *pinnedFixtureStore) CompleteInstallAttempt(_ context.Context, _ pluginrepo.Scope, a pluginrepo.InstallAttempt) error {
	a.Completed = true
	f.attempts[a.WorkspaceID+a.KeyDigest] = &a
	return nil
}
func pin(v string) *string { return &v }
func fixturePinned() (*Service, *pinnedFixtureStore, *fakeCapabilityInstaller) {
	f := &pinnedFixtureStore{Store: &fakeStore{}, plugins: map[string]*model.Plugin{}, versions: map[pluginrepo.InstallVersionKey]*model.PluginVersion{}, state: model.PluginVersionState{PublishedVersionID: pin("root-v1")}}
	add := func(id string, kind model.PluginType, name, vid string, pkg json.RawMessage, rels []model.PluginRelation) {
		sp := testCaller.SpaceID
		p := &model.Plugin{ID: id, Name: "DRAFT NAME", Type: kind, SpaceID: &sp, OwnerUID: testCaller.UID, Visibility: model.PluginVisibilityPrivate, ListingState: model.PluginListingStatePublished, Status: 1, Package: packageWith(rawAtt("AGENTS.md", "new unpublished instructions"))}
		f.plugins[id] = p
		if kind == model.PluginTypeConnector {
			p.Package = pkg
			return
		}
		raw, _ := model.EncodeSnapshotRelations(rels)
		manifest, _ := json.Marshal(map[string]string{"plugin_name": name, "description": "fixture"})
		schema := 1
		f.versions[pluginrepo.InstallVersionKey{PluginID: id, VersionID: vid}] = &model.PluginVersion{ID: vid, PluginID: id, Manifest: manifest, Package: pkg, Relations: raw, SnapshotSchemaVersion: &schema, Artifacts: json.RawMessage(`[]`), Release: &model.PluginVersionRelease{PluginID: id, VersionID: vid}}
	}
	rel := func(id, source, target, kind, vid, data string) model.PluginRelation {
		r := model.PluginRelation{Status: 1, ID: id, SourcePluginID: source, TargetPluginID: target, Type: kind, Data: json.RawMessage(data)}
		if vid != "" {
			r.TargetVersionID = pin(vid)
		}
		return r
	}
	add("team", model.PluginTypeExpertTeam, "Team", "root-v1", packageWith(rawAtt("AGENTS.md", "team instructions")), []model.PluginRelation{rel("r1", "team", "expert-a", "expert_team_expert", "expert-v1", `{"member_key":"a","is_leader":true,"role":"leader"}`), rel("r2", "team", "expert-b", "expert_team_expert", "expert-v1", `{"member_key":"b","role":"reviewer"}`)})
	for _, suffix := range []string{"a", "b"} {
		add("expert-"+suffix, model.PluginTypeExpert, "Expert "+suffix, "expert-v1", packageWith(rawAtt("AGENTS.md", "old expert "+suffix)), []model.PluginRelation{rel("s"+suffix, "expert-"+suffix, "skill-"+suffix, "expert_skill", "skill-v1", `{}`), rel("c"+suffix, "expert-"+suffix, "connector", "expert_connector", "", `{}`)})
		add("skill-"+suffix, model.PluginTypeSkill, "Search "+suffix, "skill-v1", packageWith(rawAtt("SKILL.md", "---\nname: Search\ndescription: fixture\n---\nBody "+suffix), rawAtt("references/guide.md", "guide "+suffix)), nil)
	}
	add("connector", model.PluginTypeConnector, "Connector", "", packageWith(rawAtt("mcp.json", `{"mcpServers":{"search":{"command":"search-v1"}}}`)), nil)
	installer := &fakeCapabilityInstaller{}
	return New(f).WithVersionManagement(true).WithPinnedInstallation(true).WithInstallationHMACKeys("test", map[string]string{"test": "synthetic-installation-test-key-32-bytes"}).WithCapabilityInstaller(installer), f, installer
}

func TestPinnedInstallationPreservesMarketNamesAndFrozenFiles(t *testing.T) {
	s, f, remote := fixturePinned()
	p := installationParams()
	p.ResourceName = ""
	if _, err := s.CreateInstallation(context.Background(), testCaller, "team", p); err != nil {
		t.Fatalf("%#v", err)
	}
	def := remote.in.Definition
	if remote.calls != 1 || len(def.Skills) != 2 || len(def.Experts) != 2 {
		t.Fatalf("unexpected definition %+v", def)
	}
	if def.Skills[0].Name != "Search a" || def.Skills[1].Name != "Search b" {
		t.Fatal("market skill names changed")
	}
	for _, e := range def.Experts {
		if !strings.HasPrefix(e.Instructions, "old expert") || !strings.Contains(string(e.MCPConfig), "search-v1") || len(e.SkillNames) != 1 {
			t.Fatalf("mutable or embedded content leaked %+v", e)
		}
	}
	if f.pluginCalls != 4 || f.versionCalls != 3 {
		t.Fatalf("not batched: plugin=%d version=%d", f.pluginCalls, f.versionCalls)
	}
	if !strings.Contains(string(f.versions[pluginrepo.InstallVersionKey{PluginID: "skill-a", VersionID: "skill-v1"}].Package), "name: Search") {
		t.Fatal("source was mutated")
	}
}

func TestPinnedInstallRejectsInlineSnapshotsBeforeEitherInstaller(t *testing.T) {
	for _, target := range []string{"expert", "member", "team"} {
		for _, connector := range []bool{false, true} {
			for _, path := range []string{"mcp.json", "expert/context.json", "skills/legacy/SKILL.md", "members/legacy/AGENTS.md"} {
				t.Run(target+"/"+path+"/connector="+strconv.FormatBool(connector), func(t *testing.T) {
					s, f, remote := fixturePinned()
					id, pluginID, versionID := "team", "expert-a", "expert-v1"
					if target == "expert" {
						id = "expert-a"
						f.state.PublishedVersionID = pin(versionID)
					} else if target == "team" {
						pluginID, versionID = "team", "root-v1"
					}
					v := f.versions[pluginrepo.InstallVersionKey{PluginID: pluginID, VersionID: versionID}]
					v.Package = packageWith(rawAtt("AGENTS.md", "legacy instructions"), rawAtt(path, `{"mcpServers":{"legacy":{"command":"tool"}}}`))
					if !connector {
						for _, member := range []string{"expert-a", "expert-b"} {
							v := f.versions[pluginrepo.InstallVersionKey{PluginID: member, VersionID: "expert-v1"}]
							rels, err := model.DecodeSnapshotRelations(v.Relations)
							if err != nil {
								t.Fatal(err)
							}
							v.Relations, _ = model.EncodeSnapshotRelations(relationsOfType(rels, "expert_skill"))
						}
					}
					_, err := s.CreateInstallation(context.Background(), testCaller, id, installationParams())
					var validation *InstallationValidationError
					if !errors.As(err, &validation) || validation.Reason != "unsupported_inline_capabilities" || remote.calls != 0 {
						t.Fatalf("capability err=%v calls=%d", err, remote.calls)
					}
					p := installationParams()
					if _, err = s.Install(context.Background(), testCaller, id, InstallParams{WorkspaceID: p.WorkspaceID, RuntimeID: p.RuntimeID}); !errors.As(err, &validation) {
						t.Fatalf("legacy accepted inline snapshot: %v", err)
					}
				})
			}
		}
	}
}

func TestPinnedMissingRootIsNotFoundForBothEntrypoints(t *testing.T) {
	for _, mode := range []string{"missing", "cross-space", "other-owner", "embedded", "deleted", "draft"} {
		t.Run(mode, func(t *testing.T) {
			s, f, remote := fixturePinned()
			root := f.plugins["team"]
			switch mode {
			case "missing":
				delete(f.plugins, "team")
			case "cross-space":
				root.SpaceID = pin("other-space")
			case "other-owner":
				root.OwnerUID = "other"
			case "embedded":
				root.IsEmbedded = true
			case "deleted":
				now := time.Now()
				root.DeletedAt = &now
			case "draft":
				root.ListingState = model.PluginListingStateDraft
			}
			if _, err := s.CreateInstallation(context.Background(), testCaller, "team", installationParams()); !errors.Is(err, ErrNotFound) || remote.calls != 0 {
				t.Fatalf("err=%v calls=%d", err, remote.calls)
			}
			p := installationParams()
			if _, err := s.Install(context.Background(), testCaller, "team", InstallParams{WorkspaceID: p.WorkspaceID, RuntimeID: p.RuntimeID}); !errors.Is(err, ErrNotFound) {
				t.Fatal(err)
			}
		})
	}
}

func TestPinnedInstallRejectsBeforeFleet(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*pinnedFixtureStore)
	}{
		{"missing pin", func(f *pinnedFixtureStore) {
			v := f.versions[pluginrepo.InstallVersionKey{PluginID: "team", VersionID: "root-v1"}]
			rels, _ := model.DecodeSnapshotRelations(v.Relations)
			rels[0].TargetVersionID = nil
			v.Relations, _ = model.EncodeSnapshotRelations(rels)
		}},
		{"unknown snapshot schema", func(f *pinnedFixtureStore) {
			schema := 999
			f.versions[pluginrepo.InstallVersionKey{PluginID: "skill-a", VersionID: "skill-v1"}].SnapshotSchemaVersion = &schema
		}},
		{"unreleased pin", func(f *pinnedFixtureStore) {
			f.versions[pluginrepo.InstallVersionKey{PluginID: "skill-a", VersionID: "skill-v1"}].Release = nil
		}},
		{"wrong resource", func(f *pinnedFixtureStore) {
			f.versions[pluginrepo.InstallVersionKey{PluginID: "skill-a", VersionID: "skill-v1"}].PluginID = "skill-b"
		}},
		{"foreign space", func(f *pinnedFixtureStore) { other := "other-space"; f.plugins["skill-a"].SpaceID = &other }},
		{"private audience", func(f *pinnedFixtureStore) { f.plugins["expert-a"].Visibility = model.PluginVisibilitySpace }},
		{"delisted root", func(f *pinnedFixtureStore) { f.plugins["team"].ListingState = model.PluginListingStateDelisted }},
		{"draft connector", func(f *pinnedFixtureStore) { f.plugins["connector"].ListingState = model.PluginListingStateDraft }},
		{"connector version", func(f *pinnedFixtureStore) {
			v := f.versions[pluginrepo.InstallVersionKey{PluginID: "expert-a", VersionID: "expert-v1"}]
			rels, _ := model.DecodeSnapshotRelations(v.Relations)
			rels[1].TargetVersionID = pin("made-up")
			v.Relations, _ = model.EncodeSnapshotRelations(rels)
		}},
		{"duplicate leader", func(f *pinnedFixtureStore) {
			v := f.versions[pluginrepo.InstallVersionKey{PluginID: "team", VersionID: "root-v1"}]
			rels, _ := model.DecodeSnapshotRelations(v.Relations)
			rels[1].Data = json.RawMessage(`{"member_key":"b","is_leader":true}`)
			v.Relations, _ = model.EncodeSnapshotRelations(rels)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f, remote := fixturePinned()
			tc.mutate(f)
			if _, err := s.CreateInstallation(context.Background(), testCaller, "team", installationParams()); err == nil {
				t.Fatal("invalid graph installed")
			}
			if remote.calls != 0 {
				t.Fatal("preparation caused Fleet mutation")
			}
		})
	}
}
func TestPinnedInstallRejectsDelistedDependencies(t *testing.T) {
	s, f, remote := fixturePinned()
	f.plugins["skill-a"].ListingState = model.PluginListingStateDelisted
	if _, err := s.CreateInstallation(context.Background(), testCaller, "team", installationParams()); !errors.Is(err, ErrDependencyHidden) || remote.calls != 0 {
		t.Fatalf("delisted dependency installed: %v", err)
	}
}

// A references-enabled install is a published P graph: without a pinned resolver
// the service fails closed instead of following a live head.
func TestPinnedInstallNeedsVersioning(t *testing.T) {
	s, _, remote := fixturePinned()
	s.WithVersionManagement(false)
	_, err := s.CreateInstallation(context.Background(), testCaller, "team", installationParams())
	if !errors.Is(err, ErrVersionBaselineUnavailable) || remote.calls != 0 {
		t.Fatalf("err=%v calls=%d", err, remote.calls)
	}
}

func TestPinnedInstallRetriesKeepInitialPlanDespiteUpstreamChanges(t *testing.T) {
	s, f, remote := fixturePinned()
	p := installationParams()
	if _, err := s.CreateInstallation(context.Background(), testCaller, "team", p); err != nil {
		t.Fatalf("%#v", err)
	}
	if remote.key == "" || remote.key == p.IdempotencyKey {
		t.Fatalf("first dispatch did not use a stable attempt id: key=%q", remote.key)
	}
	f.state.PublishedVersionID = pin("new-root-not-in-fixture")
	out, err := s.CreateInstallation(context.Background(), testCaller, "team", p)
	if err != nil || !out.Replayed || remote.calls != 2 {
		t.Fatalf("retry drifted: %+v %v %d", out, err, remote.calls)
	}
	changed := p
	changed.ResourceName = "Changed"
	if _, err = s.CreateInstallation(context.Background(), testCaller, "team", changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("input change err=%v", err)
	}
	f.plugins["connector"].Package = packageWith(rawAtt("mcp.json", `{"mcpServers":{"search":{"command":"new-command"}}}`))
	if _, err = s.CreateInstallation(context.Background(), testCaller, "team", p); err != nil {
		t.Fatalf("upstream update blocked frozen replay: %v", err)
	}
	if remote.calls != 3 || strings.Contains(string(remote.in.Definition.Experts[0].MCPConfig), "new-command") {
		t.Fatal("frozen connector content changed")
	}

}
func TestPinnedUncertainRetryUsesSameKeyAndExpiredReceiptFailsClosed(t *testing.T) {
	s, f, remote := fixturePinned()
	p := installationParams()
	remote.err = errors.New("uncertain remote result")
	if _, err := s.CreateInstallation(context.Background(), testCaller, "team", p); err == nil {
		t.Fatal("uncertain remote result lost")
	}
	if remote.calls != 1 || remote.key == "" || remote.key == p.IdempotencyKey {
		t.Fatalf("uncertain dispatch did not use a stable attempt id: calls=%d key=%q", remote.calls, remote.key)
	}
	for _, a := range f.attempts {
		cutoff := s.now().Add(-time.Minute)
		a.RetryUntil = &cutoff
	}
	if _, err := s.CreateInstallation(context.Background(), testCaller, "team", p); !errors.Is(err, ErrAttemptExpired) {
		t.Fatalf("expired uncertain key permitted: %v", err)
	}
	if remote.calls != 1 {
		t.Fatal("expired key reached Fleet")
	}
	for _, a := range f.attempts {
		if a.State != "EXPIRED" {
			t.Fatalf("expired state not persisted: %s", a.State)
		}
	}
}
func (f *pinnedFixtureStore) AcquireInstallDispatch(_ context.Context, _ pluginrepo.Scope, a pluginrepo.InstallAttempt, token string, now time.Time, lease, retry time.Duration) (bool, error) {
	p := f.attempts[a.WorkspaceID+a.KeyDigest]
	if p.Completed || (p.LeaseUntil != nil && now.Before(*p.LeaseUntil)) || (p.RetryUntil != nil && !now.Before(*p.RetryUntil)) {
		return false, nil
	}
	if p.State == "DISPATCHING" {
		p.HadUnknown = true
	}
	p.State = "DISPATCHING"
	p.LeaseToken = token
	until := now.Add(lease)
	p.LeaseUntil = &until
	if p.RetryUntil == nil {
		cutoff := now.Add(retry)
		p.RetryUntil = &cutoff
	}
	return true, nil
}
func (f *pinnedFixtureStore) SetInstallDispatchUnknown(_ context.Context, _ pluginrepo.Scope, a pluginrepo.InstallAttempt, token string) error {
	p := f.attempts[a.WorkspaceID+a.KeyDigest]
	if p.LeaseToken == token && !p.Completed {
		p.State = "UNKNOWN"
		p.HadUnknown = true
		p.LeaseToken = ""
		p.LeaseUntil = nil
	}
	return nil
}
func (f *pinnedFixtureStore) ExpireInstallAttempt(_ context.Context, _ pluginrepo.Scope, a pluginrepo.InstallAttempt) error {
	p := f.attempts[a.WorkspaceID+a.KeyDigest]
	if p != nil && p.State != "SUCCEEDED" {
		p.State = "EXPIRED"
	}
	return nil
}
func (f *pinnedFixtureStore) FinishInstallDispatch(_ context.Context, _ pluginrepo.Scope, a pluginrepo.InstallAttempt, token string) (bool, error) {
	if f.finishErr != nil {
		return false, f.finishErr
	}
	p := f.attempts[a.WorkspaceID+a.KeyDigest]
	if p.LeaseToken != token || p.Completed {
		return false, nil
	}
	p.Completed = true
	p.State = "SUCCEEDED"
	p.AgentID = a.AgentID
	p.TeamID = a.TeamID
	p.LeaseToken = ""
	p.LeaseUntil = nil
	return true, nil
}

func (f *pinnedFixtureStore) FailInstallDispatch(_ context.Context, _ pluginrepo.Scope, a pluginrepo.InstallAttempt, token string) (bool, error) {
	p := f.attempts[a.WorkspaceID+a.KeyDigest]
	if p.LeaseToken != token || p.HadUnknown || p.Completed {
		return false, nil
	}
	p.State = "FAILED"
	p.LeaseToken = ""
	p.LeaseUntil = nil
	return true, nil
}

func TestUnknownInstallationSurvivesLaterForbidden(t *testing.T) {
	s, f, remote := fixturePinned()
	p := installationParams()
	remote.err = errors.New("timeout")
	_, err := s.CreateInstallation(context.Background(), testCaller, "team", p)
	var attemptErr *InstallationAttemptError
	if !errors.As(err, &attemptErr) || !attemptErr.Unknown {
		t.Fatal("timeout did not expose uncertainty")
	}
	remote.err = &fleet.CapabilityAPIError{Status: 403}
	_, err = s.CreateInstallation(context.Background(), testCaller, "team", p)
	if !errors.As(err, &attemptErr) || !attemptErr.Unknown {
		t.Fatal("later 403 erased earlier possible commit")
	}
	for _, a := range f.attempts {
		if a.State != "UNKNOWN" || !a.HadUnknown {
			t.Fatal("uncertainty lost")
		}
		for _, secret := range []string{p.Token, p.IdempotencyKey, p.CustomEnv["OCTOBUDDY_PROVIDER_ID"]} {
			if secret != "" && strings.Contains(string(a.FrozenPlan), secret) {
				t.Fatal("runtime input persisted in frozen plan")
			}
		}
	}
	remote.err = nil
	s.WithInstallationHMACKeys("rotated", map[string]string{"test": "synthetic-installation-test-key-32-bytes", "rotated": "synthetic-rotated-installation-key-32-bytes"})
	if _, err = s.CreateInstallation(context.Background(), testCaller, "team", p); err != nil {
		t.Fatalf("old-key frozen retry failed after rotation: %v", err)
	}
}

func TestSuccessfulRemoteCompletionFailureKeepsRetryDeadline(t *testing.T) {
	s, f, _ := fixturePinned()
	f.finishErr = errors.New("completion write failed")
	_, err := s.CreateInstallation(context.Background(), testCaller, "team", installationParams())
	var attemptErr *InstallationAttemptError
	if !errors.As(err, &attemptErr) || !attemptErr.Unknown {
		t.Fatalf("completion failure was not uncertain: %v", err)
	}
	if attemptErr.RetryUntil == nil || !time.Now().Before(*attemptErr.RetryUntil) {
		t.Fatalf("durable retry deadline lost: %+v", attemptErr)
	}
}

// A persisted plan is untrusted on recovery, even when the original attempt
// was valid. Reject malformed structure before dispatch without changing state.
func TestPinnedRetryRejectsMalformedDefinition(t *testing.T) {
	for _, name := range []string{"", "Custom name"} {
		for _, corruption := range []string{"empty_experts", "missing_team", "duplicate_expert", "missing_member", "unknown_member", "duplicate_member", "no_leader", "unknown_skill", "wrong_schema", "wrong_root_kind"} {
			t.Run(name+"/"+corruption, func(t *testing.T) {
				s, f, remote := fixturePinned()
				p := installationParams()
				p.ResourceName = name
				remote.err = errors.New("timeout")
				_, _ = s.CreateInstallation(context.Background(), testCaller, "team", p)
				a := f.attempts[p.WorkspaceID+digestInstall(p.IdempotencyKey)]
				if a == nil || remote.calls != 1 {
					t.Fatal("initial attempt missing")
				}
				var plan pinnedInstallationPlan
				if err := json.Unmarshal(a.FrozenPlan, &plan); err != nil {
					t.Fatal(err)
				}
				switch corruption {
				case "empty_experts":
					plan.Definition.Experts = nil
					plan.Definition.ExpertTeam = nil
				case "missing_team":
					plan.Definition.ExpertTeam = nil
				case "duplicate_expert":
					plan.Definition.Experts[1].Name = plan.Definition.Experts[0].Name
				case "missing_member":
					plan.Definition.ExpertTeam.Members = nil
				case "unknown_member":
					plan.Definition.ExpertTeam.Members[0].ExpertName = "missing"
				case "duplicate_member":
					plan.Definition.ExpertTeam.Members[1].ExpertName = plan.Definition.ExpertTeam.Members[0].ExpertName
				case "no_leader":
					plan.Definition.ExpertTeam.Members[0].IsLeader = false
				case "unknown_skill":
					plan.Definition.Experts[0].SkillNames = []string{"missing"}
				case "wrong_schema":
					plan.Definition.SchemaVersion = "invalid"
				case "wrong_root_kind":
					f.plugins["team"].Type = model.PluginTypeExpert
				}
				var err error
				a.FrozenPlan, err = json.Marshal(plan)
				if err != nil {
					t.Fatal(err)
				}
				before, _ := json.Marshal(a)
				_, err = s.CreateInstallation(context.Background(), testCaller, "team", p)
				after, _ := json.Marshal(a)
				if !errors.Is(err, ErrIntegrity) || remote.calls != 1 || string(before) != string(after) {
					t.Fatalf("error=%v calls=%d state unchanged=%t", err, remote.calls, string(before) == string(after))
				}
			})
		}
	}
}

func TestPinnedPlanRequestRejectsCloneFailure(t *testing.T) {
	s, _, _ := fixturePinned()
	plan, err := s.resolvePinnedInstallation(context.Background(), testCaller, "team", "", false)
	if err != nil {
		t.Fatal(err)
	}
	plan.Definition.Experts[0].MCPConfig = json.RawMessage(`{"broken":`)
	if _, err = pinnedPlanRequest(plan, installationParams()); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("clone error=%v", err)
	}
	if _, err = pinnedPlanRequest(nil, installationParams()); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("nil plan error=%v", err)
	}
}
