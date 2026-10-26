package stockmigration

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

func pointer(v string) *string { return &v }
func fixture() Dataset {
	space := "space-a"
	resource := func(id string, kind model.PluginType, embedded bool, attachments ...map[string]any) Resource {
		p := model.Plugin{ID: id, Name: id, Type: kind, IsEmbedded: embedded, OwnerUID: "owner", SpaceID: &space, Visibility: "private", ListingState: "published", Status: 1, Tags: json.RawMessage(`[]`), CurrentVersion: pointer("1.2.3")}
		p.Manifest, _ = json.Marshal(map[string]any{"$schema": "cowork-plugin-manifest-2.0.json", "plugin_name": id, "plugin_type": kind, "name": id, "description": "fixture", "labels": []any{}, "examples": []any{}})
		p.Package, _ = json.Marshal(map[string]any{"$schema": "cowork-plugin-package-2.0.json", "attachments": attachments})
		return Resource{Plugin: p, Relations: []model.PluginRelation{}}
	}
	raw := func(path, content string) map[string]any {
		return map[string]any{"path": path, "content_type": "raw", "mime_type": "text/plain", "raw_content": content}
	}
	team := resource("team", "expert_team", false, raw("AGENTS.md", "team instructions"))
	team.Plugin.CurrentVersionID = pointer("old-team")
	expert := resource("member", "expert", true, raw("AGENTS.md", "member instructions"), raw("mcp.json", `{"mcpServers":{"search":{"command":"search"}}}`), raw("expert/context.json", `{"role":"researcher"}`))
	skill := resource("skill", "skill", true, raw("SKILL.md", "---\nname: skill\ndescription: fixture\n---\nbody"))
	team.Relations = []model.PluginRelation{{ID: "member-edge", SourcePluginID: "team", TargetPluginID: "member", Type: "expert_team_expert", Status: 1, SortOrder: 7, Data: json.RawMessage(`{"member_key":"m","role":"researcher","is_leader":true}`)}}
	expert.Relations = []model.PluginRelation{{ID: "skill-edge", SourcePluginID: "member", TargetPluginID: "skill", Type: "expert_skill", Status: 1, SortOrder: 3}}
	return Dataset{Resources: []Resource{team, expert, skill}, Versions: []model.PluginVersion{{ID: "old-team", PluginID: "team"}}}
}
func TestPlanExtractsOrderedPrivateResourcesAndFrozenPins(t *testing.T) {
	d := fixture()
	before, _ := json.Marshal(d)
	p := Build(d)
	after, _ := json.Marshal(d)
	if string(before) != string(after) {
		t.Fatal("preview mutated input")
	}
	if len(p.Exceptions) > 0 || len(p.Units) != 1 || len(p.Units[0].Nodes) != 4 {
		t.Fatalf("plan: %+v", p)
	}
	nodes := p.Units[0].Nodes
	for i, kind := range []model.PluginType{"skill", "connector", "expert", "expert_team"} {
		if nodes[i].Type != kind {
			t.Fatalf("order: %+v", nodes)
		}
		if nodes[i].Plugin.Visibility != "private" || nodes[i].Plugin.OwnerUID != "owner" || nodes[i].Plugin.IsEmbedded {
			t.Fatalf("audience: %+v", nodes[i])
		}
	}
	if nodes[1].VersionID != "" || nodes[3].Label != "1.2.4" || !nodes[3].Release {
		t.Fatal("connector/container release semantics")
	}
	if len(nodes[2].Relations) != 2 || nodes[2].Relations[0].TargetVersionID == nil || *nodes[2].Relations[0].TargetVersionID != nodes[0].VersionID {
		t.Fatal("expert skill pin")
	}
	edge := nodes[3].Relations[0]
	if edge.TargetVersionID == nil || *edge.TargetVersionID != nodes[2].VersionID || edge.SortOrder != 7 || !equalJSON(edge.Data, d.Resources[0].Relations[0].Data) {
		t.Fatal("member wiring/pin lost")
	}
	if hasAttachment(nodes[2].Plugin.Package, "mcp.json") || hasAttachment(nodes[2].Plugin.Package, "expert/context.json") {
		t.Fatal("expert retains private inline capabilities")
	}
	out, _ := json.Marshal(p)
	for _, secret := range []string{"search", "member instructions", "body", "researcher"} {
		if strings.Contains(string(out), secret) {
			t.Fatalf("report leaked %s", secret)
		}
	}
	if !reflect.DeepEqual(p, Build(d)) {
		t.Fatal("unstable preview")
	}
}
func TestPlanVisibilityLifecycleAndExceptions(t *testing.T) {
	for _, vis := range []model.PluginVisibility{"private", "space", "system"} {
		for _, listing := range []model.PluginListingState{"draft", "published", "delisted"} {
			d := fixture()
			d.Resources[0].Plugin.Visibility = vis
			d.Resources[0].Plugin.ListingState = listing
			p := Build(d)
			if len(p.Exceptions) > 0 {
				t.Fatalf("%s/%s: %+v", vis, listing, p.Exceptions)
			}
			for _, n := range p.Units[0].Nodes {
				if n.Plugin.Visibility != vis || n.Plugin.ListingState != listing {
					t.Fatal("scope/listing not inherited")
				}
			}
		}
	}
	for _, change := range []func(*Dataset){func(d *Dataset) { d.Versions = nil }, func(d *Dataset) { d.Resources[1].Plugin.OwnerUID = "foreign" }, func(d *Dataset) { d.Resources[0].PublishedVersionID = pointer("different-version") }, func(d *Dataset) {
		d.Resources[2].Plugin.Package = json.RawMessage(`{"attachments":[{"path":"SKILL.md","content_type":"storage"}]}`)
	}} {
		d := fixture()
		change(&d)
		if len(Build(d).Exceptions) == 0 {
			t.Fatal("ambiguous graph accepted")
		}
	}
}
func TestForwardLabelSkipsReleasedAndDoesNotReuseUnknownVersion(t *testing.T) {
	d := fixture()
	d.Releases = []model.PluginVersionRelease{{PluginID: "team", VersionID: "released", VersionLabel: "1.2.4"}}
	if label := Build(d).Units[0].Nodes[3].Label; label != "1.2.5" {
		t.Fatal(label)
	}
	d.Resources[0].Plugin.CurrentVersion = pointer("legacy-label")
	if label := Build(d).Units[0].Nodes[3].Label; label != "1.2.5" {
		t.Fatal(label)
	}
}

func TestForwardLabelStaysWithinWriteBoundsAndLeavesASuccessor(t *testing.T) {
	for _, tc := range []struct{ current, want string }{
		{"v1", "1.0.0"},
		{"", "1.0.0"},
		{"0.0.0", "0.0.1"},
		{"1.2.999999999", "1.3.0"},
		{"1.999999999.999999999", "2.0.0"},
		{"999999999.999999999.999999997", "999999999.999999999.999999998"},
		{"999999999.999999999.999999998", ""},
		{"999999999.999999999.999999999", ""},
		{"2147483647.0.1", ""},
		{"1.0.0000000001", ""},
	} {
		t.Run(tc.current, func(t *testing.T) {
			p := model.Plugin{ID: "root", CurrentVersion: pointer(tc.current)}
			if got := forwardLabel(p, Dataset{}); got != tc.want {
				t.Fatalf("forwardLabel(%q)=%q, want %q", tc.current, got, tc.want)
			}
		})
	}
	d := Dataset{
		Releases: []model.PluginVersionRelease{{PluginID: "root", VersionLabel: "5.0.0"}},
		Versions: []model.PluginVersion{{PluginID: "root", DeclaredVersion: pointer("5.0.1")}},
	}
	if got := forwardLabel(model.Plugin{ID: "root", CurrentVersion: pointer("v1")}, d); got != "5.0.2" {
		t.Fatalf("historical lower bound ignored: %s", got)
	}
	d.Releases[0].VersionLabel = "2147483647.0.1"
	if got := forwardLabel(model.Plugin{ID: "root"}, d); got != "" {
		t.Fatalf("out-of-contract historical bound accepted: %s", got)
	}
}

func TestPlanQuarantinesUnresolvedVersionLabel(t *testing.T) {
	d := fixture()
	d.Resources[0].Plugin.CurrentVersion = pointer("999999999.999999999.999999999")
	p := Build(d)
	for _, e := range p.Exceptions {
		if e.ResourceID == "team" && e.Code == "unresolved_version_label" {
			return
		}
	}
	t.Fatalf("exhausted version namespace was not quarantined: %+v", p.Exceptions)
}

func equalJSON(a, b []byte) bool {
	var x, y any
	return json.Unmarshal(a, &x) == nil && json.Unmarshal(b, &y) == nil && reflect.DeepEqual(x, y)
}

func TestPlanQuarantinesSecretAndUninstallableSkill(t *testing.T) {
	for _, tt := range []struct{ name, body string }{
		{"secret", `{"$schema":"cowork-plugin-package-2.0.json","attachments":[{"path":"AGENTS.md","content_type":"raw","mime_type":"text/plain","raw_content":"work"},{"path":"mcp.json","content_type":"raw","mime_type":"application/json","raw_content":"{\"mcpServers\":{\"search\":{\"command\":\"search\",\"env\":{\"API_KEY\":\"do-not-report-secret\"}}}}"}]}`},
		{"malformed-frontmatter", `{"$schema":"cowork-plugin-package-2.0.json","attachments":[{"path":"SKILL.md","content_type":"raw","mime_type":"text/plain","raw_content":"---\nname: [broken\n---\nbody"}]}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			d := fixture()
			index := 1
			if tt.name != "secret" {
				index = 2
			}
			d.Resources[index].Plugin.Package = json.RawMessage(tt.body)
			p := Build(d)
			if len(p.Exceptions) == 0 {
				t.Fatal("unsafe input accepted")
			}
			out, _ := json.Marshal(p)
			if strings.Contains(string(out), "do-not-report-secret") {
				t.Fatal("secret leaked")
			}
		})
	}
}
func TestPlanRejectsSameNameExpertsAndOrphanEmbedded(t *testing.T) {
	d := fixture()
	second := d.Resources[1]
	second.Plugin.ID = "member-second"
	second.Relations = nil
	d.Resources = append(d.Resources, second)
	d.Resources[0].Relations = append(d.Resources[0].Relations, model.PluginRelation{ID: "second", SourcePluginID: "team", TargetPluginID: "member-second", Type: "expert_team_expert", Status: 1, Data: json.RawMessage(`{"member_key":"second","is_leader":false}`)})
	p := Build(d)
	found := false
	for _, e := range p.Exceptions {
		found = found || e.Code == "duplicate_expert_install_name"
	}
	if !found {
		t.Fatal("same name experts passed")
	}
	d = fixture()
	d.Resources[0].Relations = nil
	if len(Build(d).Exceptions) == 0 {
		t.Fatal("unowned embedded resources vanished from inventory")
	}
}

func TestPlanIndependentSkillIsMigratedBeforeItsUnpinnedConsumer(t *testing.T) {
	d := fixture()
	d.Resources[2].Plugin.IsEmbedded = false
	d.Resources[2].Plugin.CurrentVersionID = pointer("skill-old")
	d.Versions = append(d.Versions, model.PluginVersion{ID: "skill-old", PluginID: "skill"})
	p := Build(d)
	if len(p.Exceptions) > 0 || len(p.Units) != 2 || p.Units[0].RootID != "skill" || p.Units[1].RootID != "team" || len(p.Units[1].Dependencies) != 1 {
		t.Fatalf("order/dependency: exceptions%+v units%+v", p.Exceptions, p.Units)
	}
	if p.Units[0].Nodes[0].TargetID != "skill" {
		t.Fatal("standalone skill identity replaced")
	}
}
func TestForwardLabelReservesUnreleasedHistoricalLabels(t *testing.T) {
	d := fixture()
	d.Versions = append(d.Versions, model.PluginVersion{ID: "saved", PluginID: "team", DeclaredVersion: pointer("1.2.4")})
	if label := Build(d).Units[0].Nodes[3].Label; label != "1.2.5" {
		t.Fatal(label)
	}
}
func TestHydratedFilesAreDigestBoundAndOnlyReplaceNewPackage(t *testing.T) {
	d := fixture()
	original := string(d.Resources[2].Plugin.Package)
	resolver := func(_ context.Context, _ *model.Plugin) (map[string]string, error) {
		return map[string]string{"SKILL.md": "---\nname: skill\ndescription: fixture\n---\nverified bytes"}, nil
	}
	hydrated := Hydrate(context.Background(), d, resolver)
	p := Build(hydrated)
	if len(p.Exceptions) > 0 {
		t.Fatal(p.Exceptions)
	}
	if string(d.Resources[2].Plugin.Package) != original {
		t.Fatal("source package overwritten")
	}
	if !strings.Contains(string(p.Units[0].Nodes[0].Plugin.Package), "verified bytes") {
		t.Fatal("resolved bytes unused")
	}
	hydrated.ResolvedSkillFiles["skill"]["SKILL.md"] += "changed"
	if p.Digest == Build(hydrated).Digest {
		t.Fatal("file bytes not bound to preview")
	}
}

func TestMCPQuarantinesUnambiguousCredentialCarriers(t *testing.T) {
	for _, raw := range []string{`{"type":"http","url":"https://user:do-not-report-secret@example.invalid/mcp"}`, `{"url":"https://example.invalid/mcp?api_key=do-not-report-secret"}`, `{"command":"search","args":["--token=do-not-report-secret"]}`, `{"command":"search","args":["--client-secret","do-not-report-secret"]}`} {
		reason := checkMCP(map[string]json.RawMessage{"search": json.RawMessage(raw)})
		if reason != "secret_value_requires_policy" {
			t.Fatalf("expected fixed secret exception, got %q", reason)
		}
	}
	if reason := checkMCP(map[string]json.RawMessage{"search": json.RawMessage(`{"command":"search","args":["--token","${SEARCH_TOKEN}"],"env":{"API_KEY":"${API_KEY}"}}`)}); reason != "" {
		t.Fatalf("placeholder rejected: %s", reason)
	}
}
