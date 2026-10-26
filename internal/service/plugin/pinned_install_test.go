package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

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
	return New(f).WithVersionManagement(true).WithPinnedInstallation(true).WithCapabilityInstaller(installer), f, installer
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
	if f.pluginCalls != 3 || f.versionCalls != 3 {
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
					legacy := &fakeProvisioner{}
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
					s.WithProvisioner(legacy)
					_, err := s.CreateInstallation(context.Background(), testCaller, id, installationParams())
					var validation *InstallationValidationError
					if !errors.As(err, &validation) || validation.Reason != "unsupported_inline_capabilities" || remote.calls != 0 {
						t.Fatalf("capability err=%v calls=%d", err, remote.calls)
					}
					p := installationParams()
					if _, err = s.Install(context.Background(), testCaller, id, InstallParams{WorkspaceID: p.WorkspaceID, RuntimeID: p.RuntimeID}); !errors.As(err, &validation) {
						t.Fatalf("legacy accepted inline snapshot: %v", err)
					}
					if legacy.agentSpec != nil || legacy.squad != nil {
						t.Fatal("inline rejection reached legacy provisioner")
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
			s.WithProvisioner(&fakeProvisioner{})
			p := installationParams()
			if _, err := s.Install(context.Background(), testCaller, "team", InstallParams{WorkspaceID: p.WorkspaceID, RuntimeID: p.RuntimeID}); !errors.Is(err, ErrNotFound) {
				t.Fatal(err)
			}
		})
	}
}

func TestPinnedLegacySkillLookupRejectsMissingNames(t *testing.T) {
	if _, err := pinnedLegacySkillRefs(map[string]model.SkillRef{}, []string{"missing"}); !errors.Is(err, ErrIntegrity) {
		t.Fatal(err)
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
func TestPinnedInstallUsesCurrentConnectorAndPreservesDelistedPins(t *testing.T) {
	s, f, remote := fixturePinned()
	f.plugins["skill-a"].ListingState = model.PluginListingStateDelisted
	f.plugins["expert-a"].ListingState = model.PluginListingStateDelisted
	f.plugins["connector"].Package = packageWith(rawAtt("mcp.json", `{"mcpServers":{"search":{"command":"search-v2"}}}`))
	if _, err := s.CreateInstallation(context.Background(), testCaller, "team", installationParams()); err != nil {
		t.Fatalf("%#v", err)
	}
	if !strings.Contains(string(remote.in.Definition.Experts[0].MCPConfig), "search-v2") {
		t.Fatal("connector current config not used")
	}
}
func TestPinnedInstallNeedsVersioning(t *testing.T) {
	s, _, remote := fixturePinned()
	s.WithVersionManagement(false)
	_, err := s.CreateInstallation(context.Background(), testCaller, "team", installationParams())
	if !errors.Is(err, ErrVersionBaselineUnavailable) || remote.calls != 0 {
		t.Fatalf("err=%v calls=%d", err, remote.calls)
	}
}

func TestPinnedInstallRetriesKeepInitialRootAndDetectInputOrConnectorDrift(t *testing.T) {
	s, f, remote := fixturePinned()
	p := installationParams()
	if _, err := s.CreateInstallation(context.Background(), testCaller, "team", p); err != nil {
		t.Fatalf("%#v", err)
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
	if _, err = s.CreateInstallation(context.Background(), testCaller, "team", p); !errors.Is(err, ErrConflict) {
		t.Fatalf("connector drift err=%v", err)
	}
	if remote.calls != 2 {
		t.Fatal("changed payload submitted under same key")
	}
}
func TestPinnedUncertainRetryUsesSameKeyAndExpiredReceiptFailsClosed(t *testing.T) {
	s, f, remote := fixturePinned()
	p := installationParams()
	remote.err = errors.New("uncertain remote result")
	if _, err := s.CreateInstallation(context.Background(), testCaller, "team", p); err == nil {
		t.Fatal("uncertain remote result lost")
	}
	remote.err = nil
	f.state.PublishedVersionID = pin("root-v2")
	if _, err := s.CreateInstallation(context.Background(), testCaller, "team", p); err != nil {
		t.Fatalf("%#v", err)
	}
	if remote.calls != 2 || remote.key != p.IdempotencyKey {
		t.Fatal("same-key retry not forwarded")
	}
	for _, a := range f.attempts {
		a.CreatedAt = s.now().Add(-25 * time.Hour)
	}
	if _, err := s.CreateInstallation(context.Background(), testCaller, "team", p); !errors.Is(err, ErrConflict) {
		t.Fatalf("expired uncertain key permitted: %v", err)
	}
	if remote.calls != 2 {
		t.Fatal("expired key reached Fleet")
	}
}
func TestLegacyAndCapabilityUseSameFrozenBindings(t *testing.T) {
	s, _, remote := fixturePinned()
	p := installationParams()
	p.ResourceName = ""
	if _, err := s.CreateInstallation(context.Background(), testCaller, "team", p); err != nil {
		t.Fatalf("%#v", err)
	}
	legacy := &fakeProvisioner{}
	s.WithProvisioner(legacy)
	if _, err := s.Install(context.Background(), testCaller, "team", InstallParams{WorkspaceID: p.WorkspaceID, RuntimeID: p.RuntimeID}); err != nil {
		t.Fatal(err)
	}
	if legacy.squad == nil || len(legacy.squad.Members) != 2 {
		t.Fatalf("legacy %+v", legacy)
	}
	for i, e := range remote.in.Definition.Experts {
		member := legacy.squad.Members[i]
		if member.Instruction != e.Instructions || len(member.Skills) != len(e.SkillNames) || member.Skills[0].Name != e.SkillNames[0] {
			t.Fatal("entrypoints differ")
		}
	}
}
