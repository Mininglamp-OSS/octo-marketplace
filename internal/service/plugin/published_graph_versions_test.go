package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	pluginrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/plugin"
)

type publishedGraphVersionStore struct {
	*pinnedFixtureStore
	published map[string]string
	reads     map[pluginrepo.InstallVersionKey]int
}

func (f *publishedGraphVersionStore) GetManagedVersion(_ context.Context, _ pluginrepo.Scope, id, version string) (*model.PluginVersion, error) {
	key := pluginrepo.InstallVersionKey{PluginID: id, VersionID: version}
	f.reads[key]++
	if v := f.versions[key]; v != nil {
		return v, nil
	}
	return nil, pluginrepo.ErrNotFound
}
func (f *publishedGraphVersionStore) GetVersionState(_ context.Context, _ pluginrepo.Scope, id string) (*model.PluginVersionState, error) {
	return &model.PluginVersionState{PublishedVersionID: pin(f.published[id])}, nil
}
func publishedGraphVersionsFixture(t *testing.T, first, second *string) (*Service, *publishedGraphVersionStore) {
	t.Helper()
	s, f, _ := fixturePinned()
	f.Store = &fakeStore{plugins: f.plugins, scopeAware: true}
	store := &publishedGraphVersionStore{pinnedFixtureStore: f, published: map[string]string{}, reads: map[pluginrepo.InstallVersionKey]int{}}
	for key := range f.versions {
		store.published[key.PluginID] = key.VersionID
	}
	original := f.versions[pluginrepo.InstallVersionKey{PluginID: "skill-a", VersionID: "skill-v1"}]
	v2 := *original
	v2.ID = "skill-v2"
	v2.Release = &model.PluginVersionRelease{PluginID: "skill-a", VersionID: "skill-v2"}
	v2.Manifest = json.RawMessage(`{"plugin_name":"Search version two","labels":["v2"]}`)
	v2.ManifestHash = "manifest-v2"
	v2.PluginHash = "package-v2"
	f.versions[pluginrepo.InstallVersionKey{PluginID: "skill-a", VersionID: "skill-v2"}] = &v2
	for i, id := range []string{"expert-a", "expert-b"} {
		member := f.versions[pluginrepo.InstallVersionKey{PluginID: id, VersionID: "expert-v1"}]
		rels, err := model.DecodeSnapshotRelations(member.Relations)
		if err != nil {
			t.Fatal(err)
		}
		for j := range rels {
			if rels[j].Type == "expert_skill" {
				rels[j].TargetPluginID = "skill-a"
				rels[j].TargetVersionID = []*string{first, second}[i]
			}
		}
		member.Relations, err = model.EncodeSnapshotRelations(rels)
		if err != nil {
			t.Fatal(err)
		}
	}
	s.repo = store
	return s, store
}
func TestPublishedGraphPreservesPinnedVersionIdentity(t *testing.T) {
	for _, tc := range []struct {
		name          string
		first, second *string
		missing       string
		wantVersions  int
	}{
		{"mixed", pin("skill-v1"), pin("skill-v2"), "", 2},
		{"mixed_reverse", pin("skill-v2"), pin("skill-v1"), "", 2},
		{"same_version", pin("skill-v1"), pin("skill-v1"), "", 1},
		{"missing_first", pin("skill-v1"), pin("skill-v2"), "skill-v1", 1},
		{"missing_second", pin("skill-v1"), pin("skill-v2"), "skill-v2", 1},
		{"legacy_current_and_pin", nil, pin("skill-v1"), "", 1},
		{"pin_and_legacy_current", pin("skill-v1"), nil, "", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f := publishedGraphVersionsFixture(t, tc.first, tc.second)
			if tc.missing != "" {
				delete(f.versions, pluginrepo.InstallVersionKey{PluginID: "skill-a", VersionID: tc.missing})
			}
			out, err := s.DetailGraph(context.Background(), testCaller, "team")
			if err != nil {
				t.Fatal(err)
			}
			versions := map[string]*model.Plugin{}
			connectors := 0
			for _, node := range out.Related {
				if node.Package != nil || node.AttachmentKeys != nil {
					t.Fatal("related nodes expose full package")
				}
				if node.ID == "connector" {
					connectors++
				}
				if node.ID != "skill-a" {
					continue
				}
				if node.CurrentVersionID == nil {
					t.Fatal("selected version missing")
				}
				version := *node.CurrentVersionID
				if versions[version] != nil {
					t.Fatalf("duplicate projected version %s", version)
				}
				versions[version] = node
				if version == "skill-v2" && (node.Name != "Search version two" || node.ManifestHash != "manifest-v2" || node.PluginHash != "package-v2") {
					t.Fatal("v2 projected v1 metadata")
				}
			}
			if len(versions) != tc.wantVersions || connectors != 1 {
				t.Fatalf("versions=%v connectors=%d", versions, connectors)
			}
			skillEdges := 0
			for _, rel := range out.Relations {
				if rel.TargetPluginID != "skill-a" {
					continue
				}
				skillEdges++
				wanted := "skill-v1"
				if rel.TargetVersionID != nil {
					wanted = *rel.TargetVersionID
				}
				if versions[wanted] == nil || wanted == tc.missing {
					t.Fatalf("edge has no matching node: %+v", rel)
				}
			}
			wantEdges := 2
			if tc.missing != "" {
				wantEdges = 1
			}
			if skillEdges != wantEdges {
				t.Fatalf("edges=%d want=%d", skillEdges, wantEdges)
			}
			if tc.first != nil && tc.second != nil {
				for _, version := range []string{*tc.first, *tc.second} {
					if f.reads[pluginrepo.InstallVersionKey{PluginID: "skill-a", VersionID: version}] != 1 {
						t.Fatalf("version %s was skipped or loaded twice", version)
					}
				}
			}
		})
	}
}

func TestPublishedGraphBoundsVersionedTraversal(t *testing.T) {
	for _, kind := range []string{"nodes", "edges"} {
		t.Run(kind, func(t *testing.T) {
			s, f := publishedGraphVersionsFixture(t, pin("skill-v1"), pin("skill-v2"))
			count := pluginrepo.MaxGraphNodes() + 1
			if kind == "edges" {
				count = pluginrepo.MaxGraphEdges() + 1
			}
			var rels []model.PluginRelation
			original := f.versions[pluginrepo.InstallVersionKey{PluginID: "skill-a", VersionID: "skill-v1"}]
			for i := 0; i < count; i++ {
				version := fmt.Sprintf("bounded-version-%d", i)
				v := *original
				v.ID = version
				f.versions[pluginrepo.InstallVersionKey{PluginID: "skill-a", VersionID: version}] = &v
				rels = append(rels, model.PluginRelation{ID: fmt.Sprintf("bounded-edge-%d", i), SourcePluginID: "expert-a", TargetPluginID: "skill-a", TargetVersionID: &v.ID, Type: "expert_skill", Status: 1})
			}
			raw, err := model.EncodeSnapshotRelations(rels)
			if err != nil {
				t.Fatal(err)
			}
			f.versions[pluginrepo.InstallVersionKey{PluginID: "expert-a", VersionID: "expert-v1"}].Relations = raw
			if _, err = s.DetailGraph(context.Background(), testCaller, "team"); !errors.Is(err, ErrGraphTooLarge) {
				t.Fatalf("unbounded %s: %v", kind, err)
			}
		})
	}
}

func maximumPublishedGraphFixture(t *testing.T) (*Service, *publishedGraphVersionStore) {
	t.Helper()
	s, f := publishedGraphVersionsFixture(t, pin("skill-v1"), pin("skill-v2"))
	templateExpert := *f.plugins["expert-a"]
	templateSkill := *f.plugins["skill-a"]
	expertVersion := *f.versions[pluginrepo.InstallVersionKey{PluginID: "expert-a", VersionID: "expert-v1"}]
	skillVersion := *f.versions[pluginrepo.InstallVersionKey{PluginID: "skill-a", VersionID: "skill-v1"}]
	var members []model.PluginRelation
	for member := 0; member < containerMaxMembers; member++ {
		id := fmt.Sprintf("cap-expert-%d", member)
		vid := id + "-v1"
		node := templateExpert
		node.ID = id
		f.plugins[id] = &node
		var skills []model.PluginRelation
		for skill := 0; skill < containerMaxSkills; skill++ {
			target := fmt.Sprintf("cap-skill-%d-%d", member, skill)
			version := fmt.Sprintf("%s-member-%d", target, member)
			p := templateSkill
			p.ID = target
			f.plugins[target] = &p
			f.published[target] = version
			v := skillVersion
			v.PluginID = target
			v.ID = version
			v.Release = &model.PluginVersionRelease{PluginID: target, VersionID: version}
			f.versions[pluginrepo.InstallVersionKey{PluginID: target, VersionID: version}] = &v
			skills = append(skills, model.PluginRelation{ID: fmt.Sprintf("%s-skill-%d", id, skill), SourcePluginID: id, TargetPluginID: target, TargetVersionID: pin(version), TargetPluginType: model.PluginTypeSkill, Type: "expert_skill", Status: 1})
		}
		v := expertVersion
		v.PluginID = id
		v.ID = vid
		v.Manifest, _ = json.Marshal(map[string]string{"plugin_name": id})
		v.Release = &model.PluginVersionRelease{PluginID: id, VersionID: vid}
		var err error
		v.Relations, err = model.EncodeSnapshotRelations(skills)
		if err != nil {
			t.Fatal(err)
		}
		f.versions[pluginrepo.InstallVersionKey{PluginID: id, VersionID: vid}] = &v
		wiring, _ := json.Marshal(map[string]any{"member_key": id, "is_leader": member == 0})
		members = append(members, model.PluginRelation{Data: wiring, ID: id + "-member", SourcePluginID: "team", TargetPluginID: id, TargetVersionID: pin(vid), TargetPluginType: model.PluginTypeExpert, Type: "expert_team_expert", Status: 1})
	}
	var err error
	root := f.versions[pluginrepo.InstallVersionKey{PluginID: "team", VersionID: "root-v1"}]
	root.Relations, err = model.EncodeSnapshotRelations(members)
	if err != nil {
		t.Fatal(err)
	}
	return s, f
}

func TestPublishedGraphMaximumLegalVersionedTeam(t *testing.T) {
	s, _ := maximumPublishedGraphFixture(t)
	out, err := s.DetailGraph(context.Background(), testCaller, "team")
	if err != nil {
		t.Fatal(err)
	}
	expected := containerMaxMembers * (1 + containerMaxSkills)
	if len(out.Related) != expected || len(out.Relations) != expected {
		t.Fatalf("nodes=%d edges=%d want=%d", len(out.Related), len(out.Relations), expected)
	}
}

// Both relation spellings select one release and must share one node budget.
func TestPublishedGraphAliasesAtNodeLimit(t *testing.T) {
	for _, unpinnedFirst := range []bool{false, true} {
		t.Run(fmt.Sprintf("unpinned_first_%t", unpinnedFirst), func(t *testing.T) {
			s, f := maximumPublishedGraphFixture(t)
			target := "cap-skill-0-0"
			version := f.published[target]
			if unpinnedFirst {
				first := f.versions[pluginrepo.InstallVersionKey{PluginID: "cap-expert-0", VersionID: "cap-expert-0-v1"}]
				rels, err := model.DecodeSnapshotRelations(first.Relations)
				if err != nil {
					t.Fatal(err)
				}
				rels[0].TargetVersionID = nil
				first.Relations, err = model.EncodeSnapshotRelations(rels)
				if err != nil {
					t.Fatal(err)
				}
			}
			second := f.versions[pluginrepo.InstallVersionKey{PluginID: "cap-expert-1", VersionID: "cap-expert-1-v1"}]
			rels, err := model.DecodeSnapshotRelations(second.Relations)
			if err != nil {
				t.Fatal(err)
			}
			alias := model.PluginRelation{ID: "alias", SourcePluginID: "cap-expert-1", TargetPluginID: target, TargetPluginType: model.PluginTypeSkill, Type: "expert_skill", Status: 1}
			if unpinnedFirst {
				alias.TargetVersionID = pin(version)
			}
			rels = append(rels, alias)
			second.Relations, err = model.EncodeSnapshotRelations(rels)
			if err != nil {
				t.Fatal(err)
			}
			out, err := s.DetailGraph(context.Background(), testCaller, "team")
			if err != nil {
				t.Fatal(err)
			}
			if len(out.Related) != pluginrepo.MaxGraphNodes() || len(out.Relations) != pluginrepo.MaxGraphNodes()+1 {
				t.Fatalf("nodes=%d edges=%d", len(out.Related), len(out.Relations))
			}
			matches := 0
			for _, node := range out.Related {
				if node.ID == target && node.CurrentVersionID != nil && *node.CurrentVersionID == version {
					matches++
				}
			}
			if matches != 1 {
				t.Fatalf("alias projected %d times", matches)
			}
		})
	}
}

// Unavailable selections consume traversal work, never the returned-node budget.
func TestPublishedGraphBoundsUnavailableSelections(t *testing.T) {
	for _, count := range []int{pluginrepo.MaxGraphEdges() - 2, pluginrepo.MaxGraphEdges() - 1} {
		t.Run(fmt.Sprintf("selections_%d", count), func(t *testing.T) {
			s, f := publishedGraphVersionsFixture(t, pin("skill-v1"), pin("skill-v2"))
			root := f.versions[pluginrepo.InstallVersionKey{PluginID: "team", VersionID: "root-v1"}]
			rels, err := model.DecodeSnapshotRelations(root.Relations)
			if err != nil {
				t.Fatal(err)
			}
			// Keep the two member edges; each unavailable pin is attempted only once.
			rels = rels[:2]
			for i := 0; i < count; i++ {
				rels = append(rels, model.PluginRelation{ID: fmt.Sprintf("missing-%d", i), SourcePluginID: "team", TargetPluginID: "skill-a", TargetVersionID: pin(fmt.Sprintf("missing-%d", i)), Type: "expert_skill", Status: 1})
			}
			root.Relations, err = model.EncodeSnapshotRelations(rels)
			if err != nil {
				t.Fatal(err)
			}
			// No outgoing edges beyond the requested root selection budget.
			for _, id := range []string{"expert-a", "expert-b"} {
				f.versions[pluginrepo.InstallVersionKey{PluginID: id, VersionID: "expert-v1"}].Relations = json.RawMessage(`[]`)
			}
			out, err := s.DetailGraph(context.Background(), testCaller, "team")
			if count+2 > pluginrepo.MaxGraphEdges() {
				if !errors.Is(err, ErrGraphTooLarge) {
					t.Fatalf("over-limit error=%v", err)
				}
				if len(f.reads) != 1 {
					t.Fatalf("over-limit graph read %d snapshots", len(f.reads))
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(out.Related) != 2 || len(out.Relations) != 2 {
				t.Fatalf("nodes=%d edges=%d", len(out.Related), len(out.Relations))
			}
			for key, reads := range f.reads {
				if reads != 1 {
					t.Fatalf("%+v read %d times", key, reads)
				}
			}
		})
	}
}
