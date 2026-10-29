package migration

import (
	"encoding/json"
	"fmt"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	"reflect"
	"strings"
	"testing"
	"time"
)

func resource(id, name, space, body string) Resource {
	p := model.Plugin{ID: id, Name: name, Type: model.PluginTypeSkill, Visibility: model.PluginVisibilitySystem, Status: 1, ListingState: model.PluginListingStatePublished, UpdatedAt: time.Unix(100, 0), Manifest: json.RawMessage(fmt.Sprintf(`{"$schema":"cowork-plugin-manifest-2.0.json","plugin_name":%q,"plugin_type":"skill","name":%q,"description":"test","labels":[],"examples":[]}`, name, name)), Package: json.RawMessage(`{"$schema":"cowork-plugin-package-2.0.json","attachments":[]}`)}
	if space != "" {
		p.SpaceID = &space
		p.Visibility = model.PluginVisibilitySpace
	}
	files := []File{{Path: "SKILL.md", MIME: "text/markdown", Data: []byte(body)}}
	p.Package = testPackage(files)
	return Resource{Plugin: p, Files: files}
}

func TestLegacyPublicPlanNormalizesDestinationVisibility(t *testing.T) {
	space := "old-space"
	for _, sid := range []*string{nil, &space} {
		r := resource("public-source", "public-skill", "", "body")
		r.Plugin.Visibility, r.Plugin.SpaceID = model.PluginVisibilityPublic, sid
		r.Files = append(r.Files, File{Path: "image.bin", MIME: "application/octet-stream", Data: []byte{0, 255}})
		r.Plugin.Package = testPackage(r.Files)
		p, err := Build(Input{SnapshotID: "legacy-public", Resources: []Resource{r}}, Policy{})
		if err != nil || len(p.Nodes) != 1 {
			t.Fatalf("legacy public plan: %v %+v", err, p.Issues)
		}
		n := p.Nodes[0]
		if n.Plugin.Visibility != model.PluginVisibilitySystem || n.Source.Visibility != model.PluginVisibilityPublic {
			t.Fatal("target was not normalized independently from the original source")
		}
		for _, f := range n.Files {
			if f.Path == "image.bin" && !strings.HasPrefix(f.ObjectKey, "platform/plugins/"+n.Plugin.ID+"/attachments/") {
				t.Fatal("legacy public binary assigned to a Space namespace")
			}
		}
	}
}

func TestMigrationFilesStayWithinDefaultReaderBudgets(t *testing.T) {
	data := make([]byte, (20<<20)+1)
	for _, tc := range []struct {
		size, count int
		want        string
	}{
		{20 << 20, 1, ""},
		{(20 << 20) + 1, 1, "file_size_budget_exceeded"},
		{20 << 20, 6, "expanded_bytes_budget_exceeded"},
	} {
		r := resource("reader-budget", "reader-budget", "", "body")
		for i := 0; i < tc.count; i++ {
			r.Files = append(r.Files, File{Path: fmt.Sprintf("asset-%d.bin", i), MIME: "application/octet-stream", Data: data[:tc.size]})
		}
		n := Node{Plugin: r.Plugin, Files: r.Files}
		if got := prepareFiles(&n, "reader-budget"); got != tc.want {
			t.Fatalf("size=%d count=%d: %s, want %s", tc.size, tc.count, got, tc.want)
		}
	}
}
func TestScopeVariantsStableAndImmutable(t *testing.T) {
	a := resource("a", "same", "", "system")
	b := resource("b", "same", "s", "local")
	c := resource("c", "same", "t", "local")
	d := resource("d", "same", "s", "discard")
	in := Input{SnapshotID: "snap", Resources: []Resource{d, c, b, a}}
	before, _ := json.Marshal(in)
	p, e := Build(in, Policy{})
	if e != nil || len(p.Nodes) != 3 {
		t.Fatalf("%+v %v", p, e)
	}
	q, e := Build(in, Policy{})
	if e != nil || !reflect.DeepEqual(p, q) {
		t.Fatal("unstable")
	}
	after, _ := json.Marshal(in)
	if string(before) != string(after) {
		t.Fatal("mutated")
	}
	for _, m := range p.Mappings {
		if m.SourceID == "d" && m.TargetID == "" {
			t.Fatal("no mapping")
		}
	}
}
func TestKeepVariantsAndAllFiles(t *testing.T) {
	a := resource("a", "fullstack-dev", "s", "one")
	b := resource("b", "fullstack-dev", "s", "two")
	for i := 0; i < 73; i++ {
		a.Files = append(a.Files, File{Path: fmt.Sprintf("refs/%d.bin", i), MIME: "application/octet-stream", Data: []byte{0, 255, byte(i)}})
	}
	a.Plugin.Package = testPackage(a.Files)
	p, e := Build(Input{SnapshotID: "s", Resources: []Resource{a, b}}, Policy{})
	if e != nil || len(p.Nodes) != 2 {
		t.Fatalf("%+v %v", p, e)
	}
	var large Node
	for _, n := range p.Nodes {
		if n.SourceID == "a" {
			large = n
		}
	}
	if len(large.Files) != 74 {
		t.Fatal("lost files")
	}
	var side map[string]string
	json.Unmarshal(large.Plugin.AttachmentKeys, &side)
	if len(side) != 73 {
		t.Fatal("lost binary")
	}
}
func TestDeletedPendingAndCycle(t *testing.T) {
	a := resource("a", "a", "", "a")
	a.Plugin.Type = model.PluginTypeExpert
	a.Relations = []model.PluginRelation{{TargetPluginID: "a", Type: "expert_skill", TargetPluginType: model.PluginTypeSkill, Status: 1}}
	b := resource("b", "b", "", "b")
	b.PendingReview = true
	c := resource("c", "c", "", "c")
	now := time.Now()
	c.Plugin.DeletedAt = &now
	p, e := Build(Input{SnapshotID: "s", Resources: []Resource{a, b, c}}, Policy{})
	if e == nil || len(p.Issues) == 0 || len(p.Mappings) != 3 {
		t.Fatalf("%+v %v", p, e)
	}
}
func TestFrontendChineseSelectionAndValidation(t *testing.T) {
	a := resource("a", "frontend-dev", "", "English")
	b := resource("b", "frontend-dev", "", "中文\nreferences")
	b.Plugin.UpdatedAt = time.Unix(200, 0)
	p, e := Build(Input{SnapshotID: "s", Resources: []Resource{a, b}}, Policy{FrontendChineseSourceIDs: []string{"b"}})
	if e != nil || len(p.Nodes) != 1 || p.Nodes[0].SourceID != "b" {
		t.Fatalf("nodes %d issues %v error %v", len(p.Nodes), p.Issues, e)
	}
	_, e = Build(Input{SnapshotID: "s", Resources: []Resource{a, b}}, Policy{FrontendChineseSourceIDs: []string{"a", "b"}})
	if e == nil {
		t.Fatal("accepted invalid candidate")
	}
}
func TestMCPPerServerPreservesConfigurationWithoutReportDisclosure(t *testing.T) {
	r := resource("expert", "expert", "", "body")
	r.Plugin.Type = model.PluginTypeExpert
	r.Plugin.Manifest = json.RawMessage(`{"$schema":"cowork-plugin-manifest-2.0.json","plugin_name":"expert","plugin_type":"expert","name":"expert","description":"test","labels":[],"examples":[]}`)
	r.Files = []File{{Path: "AGENTS.md", MIME: "text/markdown", Data: []byte("agent")}, {Path: "mcp.json", MIME: "application/json", Data: []byte(`{"mcpServers":{"a":{"command":"echo"},"b":{"command":"echo","env":{"TOKEN":"${TOKEN}"}}}}`)}}
	r.Plugin.Package = testPackage(r.Files)
	p, e := Build(Input{SnapshotID: "s", Resources: []Resource{r}}, Policy{})
	if e != nil || len(p.Nodes) != 3 {
		t.Fatalf("nodes %d issues %v err %v", len(p.Nodes), p.Issues, e)
	}
	if p.Nodes[2].Plugin.ID != "expert" || len(p.Nodes[2].Relations) != 2 {
		t.Fatal("topology")
	}
	r.Files[1].Data = []byte(`{"mcpServers":{"a":{"url":"https://mcp.example.test/mcp?key=production-value","headers":{"Authorization":"Bearer production-value"},"providerOptions":{"region":"cn"}},"b":{"type":"stdio","command":"echo","env":{"REGION":"cn","TOKEN":"production-value"}}},"rootOptions":{"enabled":true}}`)
	r.Plugin.Package = testPackage(r.Files)
	p, e = Build(Input{SnapshotID: "s", Resources: []Resource{r}}, Policy{})
	if e != nil || len(p.Nodes) != 3 {
		t.Fatalf("existing config blocked: %#v %v", p.Issues, e)
	}
	var want struct {
		Servers map[string]any `json:"mcpServers"`
	}
	json.Unmarshal(r.Files[1].Data, &want)
	got := map[string]any{}
	for _, n := range p.Nodes {
		if n.Plugin.Type == model.PluginTypeConnector {
			var full map[string]json.RawMessage
			json.Unmarshal(n.Files[0].Data, &full)
			if string(full["rootOptions"]) != `{"enabled":true}` {
				t.Fatal("root MCP extension lost")
			}
			var cfg struct {
				Servers map[string]any `json:"mcpServers"`
			}
			json.Unmarshal(n.Files[0].Data, &cfg)
			for name, config := range cfg.Servers {
				got[name] = config
			}
		}
	}
	if !reflect.DeepEqual(got, want.Servers) {
		t.Fatal("MCP address or configuration changed")
	}
	public, _ := json.Marshal(p)
	if strings.Contains(string(public), "production-value") {
		t.Fatal("public secret")
	}
	connector := r
	connector.Plugin.Type = model.PluginTypeConnector
	connector.Plugin.Manifest = json.RawMessage(`{"$schema":"cowork-plugin-manifest-2.0.json","plugin_name":"existing","plugin_type":"connector","name":"existing","description":"test","labels":[],"examples":[]}`)
	connector.Files = []File{r.Files[1]}
	connector.Plugin.Package = testPackage(connector.Files)
	var connectorPackage map[string]any
	json.Unmarshal(connector.Plugin.Package, &connectorPackage)
	connectorPackage["connector"] = map[string]string{"type": "mcp", "source": "migration"}
	connector.Plugin.Package, _ = json.Marshal(connectorPackage)
	if plan, e := Build(Input{SnapshotID: "s", Resources: []Resource{connector}}, Policy{}); e != nil {
		t.Fatal("existing connector configuration blocked", plan.Issues, e)
	}
}
func TestSharedSpaceOwnerAndPrivateBoundary(t *testing.T) {
	a := resource("a", "same", "s", "one")
	a.Plugin.OwnerUID = "u"
	b := resource("b", "same", "s", "two")
	b.Plugin.OwnerUID = "v"
	c := resource("c", "same", "s", "one")
	c.Plugin.OwnerUID = "u"
	c.Plugin.Visibility = model.PluginVisibilityPrivate
	d := resource("d", "same", "s", "one")
	d.Plugin.OwnerUID = "v"
	d.Plugin.Visibility = model.PluginVisibilityPrivate
	p, e := Build(Input{SnapshotID: "s", Resources: []Resource{a, b, c, d}}, Policy{})
	if e != nil || len(p.Nodes) != 3 {
		t.Fatalf("nodes %d issues %v error %v", len(p.Nodes), p.Issues, e)
	}
}
func TestIdentityIgnoresFileOrderAndStorageLocation(t *testing.T) {
	r := resource("a", "same", "", "one")
	r.Files = append(r.Files, File{Path: "b.md", MIME: "text/markdown", Data: []byte("b"), ObjectKey: "old/a"})
	p, e := Build(Input{SnapshotID: "s", Resources: []Resource{r}}, Policy{})
	if e != nil {
		t.Fatal(e)
	}
	r.Files[0], r.Files[1] = r.Files[1], r.Files[0]
	r.Files[0].ObjectKey = "old/b"
	q, e := Build(Input{SnapshotID: "s", Resources: []Resource{r}}, Policy{})
	if e != nil || p.ID != q.ID {
		t.Fatal("file order/location changed plan identity")
	}
}
func TestGraphTeamRolesAndDuplicatePins(t *testing.T) {
	r := resource("root", "root", "", "body")
	r.Plugin.Type = model.PluginTypeExpertTeam
	r.Files = []File{{Path: "AGENTS.md", MIME: "text/markdown", Data: []byte("team")}}
	r.Plugin.Manifest = json.RawMessage(`{"$schema":"cowork-plugin-manifest-2.0.json","plugin_name":"root","plugin_type":"expert_team","name":"root","description":"team","labels":[],"examples":[]}`)
	c := resource("child", "child", "", "body")
	c.Plugin.Type = model.PluginTypeExpert
	c.Files[0].Path = "AGENTS.md"
	c.Plugin.Manifest = json.RawMessage(`{"$schema":"cowork-plugin-manifest-2.0.json","plugin_name":"child","plugin_type":"expert","name":"child","description":"expert","labels":[],"examples":[]}`)
	r.Relations = []model.PluginRelation{{ID: "edge", SourcePluginID: "root", SourcePluginType: model.PluginTypeExpertTeam, TargetPluginID: "child", TargetPluginType: model.PluginTypeExpert, Type: "expert_team_expert", Status: 1, Data: json.RawMessage(`{"member_key":"a","role":"leader","is_leader":true}`)}}
	r.Plugin.Package = testPackage(r.Files)
	c.Plugin.Package = testPackage(c.Files)
	p, e := Build(Input{SnapshotID: "s", Resources: []Resource{r, c}}, Policy{})
	if e != nil || len(p.Nodes) != 2 || p.Nodes[0].SourceID != "child" || p.Nodes[1].Relations[0].TargetVersionID == nil {
		t.Fatalf("issues %v error %v", p.Issues, e)
	}
	r.Relations = append(r.Relations, r.Relations[0])
	r.Relations[1].Data = json.RawMessage(`{"member_key":"b","role":"worker","is_leader":false}`)
	_, e = Build(Input{SnapshotID: "s", Resources: []Resource{r, c}}, Policy{})
	if e == nil {
		t.Fatal("accepted colliding relation identity")
	}
}
func TestLifecycleIsolation(t *testing.T) {
	for _, state := range []model.PluginListingState{model.PluginListingStateDraft, model.PluginListingStateDelisted} {
		a := resource("a", "same", "s", "draft")
		a.Plugin.ListingState = state
		a.Plugin.OwnerUID = "u"
		b := resource("b", "same", "s", "published")
		b.Plugin.OwnerUID = "v"
		b.Plugin.ListingState = model.PluginListingStatePublished
		c := resource("c", "same", "s", "draft")
		c.Plugin.ListingState = state
		c.Plugin.OwnerUID = "v"
		p, e := Build(Input{SnapshotID: "s", Resources: []Resource{a, b, c}}, Policy{})
		if e != nil || len(p.Nodes) != 3 {
			t.Fatalf("state %s nodes %d issues %v", state, len(p.Nodes), p.Issues)
		}
		if accessible(b.Plugin, a.Plugin) {
			t.Fatal("published crossowner can access unlisted")
		}
	}
	a := resource("a", "same", "", "same")
	a.Plugin.Status = 0
	b := resource("b", "same", "s", "same")
	p, e := Build(Input{SnapshotID: "s", Resources: []Resource{a, b}}, Policy{})
	if e != nil || len(p.Nodes) != 2 {
		t.Fatalf("disabled reuse: %d %v", len(p.Nodes), p.Issues)
	}
}
func TestSnapshotContentIncludesRelationsAndSidecar(t *testing.T) {
	r := resource("a", "a", "", "body")
	id := "v"
	r.PublishedVersionID = &id
	v := model.PluginVersion{ID: id, PluginID: "a", PluginType: model.PluginTypeSkill, Manifest: r.Plugin.Manifest, Package: r.Plugin.Package, Relations: json.RawMessage(`[]`)}
	release := model.PluginVersionRelease{PluginID: "a", VersionID: id}
	for _, kind := range []string{"sidecar", "pin", "role", "release"} {
		rr := r
		vv := v
		rels := []model.PluginVersionRelease{release}
		switch kind {
		case "sidecar":
			vv.AttachmentKeys = json.RawMessage(`{"x":"old"}`)
		case "pin", "role":
			e := model.PluginRelation{TargetPluginID: "child", TargetPluginType: model.PluginTypeSkill, Type: "expert_skill", Status: 1}
			if kind == "pin" {
				x := "old-version"
				e.TargetVersionID = &x
			} else {
				e.Data = json.RawMessage(`{"role":"old"}`)
			}
			vv.Relations, _ = model.EncodeSnapshotRelations([]model.PluginRelation{e})
		case "release":
			rels = nil
		}
		p, e := Build(Input{SnapshotID: "s", Resources: []Resource{rr}, Versions: []model.PluginVersion{vv}, Releases: rels}, Policy{})
		if e == nil {
			t.Fatalf("accepted %s divergence %v", kind, p.Issues)
		}
	}
}
func TestSnapshotRelationComparisonUsesSemanticIdentity(t *testing.T) {
	r := resource("a", "a", "", "body")
	pin := "v1"
	r.Relations = []model.PluginRelation{{ID: "old-row", Type: "expert_team_expert", TargetPluginID: "member", TargetPluginType: model.PluginTypeExpert, TargetVersionID: &pin, Status: 1, Data: json.RawMessage(`{"role":"worker","member_key":"m","is_leader":false}`)}}
	old := append([]model.PluginRelation(nil), r.Relations...)
	old[0].ID = "different-row"
	old[0].UpdatedAt = time.Unix(123, 0)
	old[0].Data = json.RawMessage(`{"is_leader":false,"member_key":"m","role":"worker"}`)
	raw, _ := model.EncodeSnapshotRelations(old)
	v := model.PluginVersion{Manifest: r.Plugin.Manifest, Package: r.Plugin.Package, Relations: raw}
	if !snapshotMatches(r, v) {
		t.Fatal("row metadata or JSON key order treated as content")
	}
	newPin := "v2"
	old[0].TargetVersionID = &newPin
	v.Relations, _ = model.EncodeSnapshotRelations(old)
	if snapshotMatches(r, v) {
		t.Fatal("changed historical pin accepted")
	}
	old[0].TargetVersionID = &pin
	old[0].Data = json.RawMessage(`{"member_key":"m","role":"leader","is_leader":true}`)
	v.Relations, _ = model.EncodeSnapshotRelations(old)
	if snapshotMatches(r, v) {
		t.Fatal("changed team role accepted")
	}
}
func TestStoragePrefixAndSystemScope(t *testing.T) {
	r := resource("a", "binary", "space-id", "body")
	r.Files = append(r.Files, File{Path: "image.bin", MIME: "application/octet-stream", Data: []byte{0, 255}})
	p, e := Build(Input{SnapshotID: "s", Resources: []Resource{r}}, Policy{})
	if e != nil {
		t.Fatal(p.Issues)
	}
	if !strings.HasPrefix(p.Nodes[0].Files[1].ObjectKey, "plugins/space-id/attachments/versions/") {
		t.Fatal("unreadable storage prefix")
	}
	r.Plugin.SpaceID = nil
	r.Plugin.Visibility = model.PluginVisibilitySystem
	p, e = Build(Input{SnapshotID: "s", Resources: []Resource{r}}, Policy{})
	if e != nil {
		t.Fatal(p.Issues)
	}
	n := p.Nodes[0]
	if n.Plugin.SpaceID != nil || n.Plugin.Visibility != model.PluginVisibilitySystem {
		t.Fatal("platform ownership changed")
	}
	want := "platform/plugins/" + n.Plugin.ID + "/attachments/versions/" + digest(r.Files[1].Data)
	if n.Files[1].ObjectKey != want {
		t.Fatalf("platform object key = %q, want %q", n.Files[1].ObjectKey, want)
	}
	var side map[string]string
	if json.Unmarshal(n.Plugin.AttachmentKeys, &side) != nil || side["image.bin"] != want {
		t.Fatal("missing platform attachment sidecar")
	}
	if !strings.Contains(string(n.Plugin.Package), "sha256:"+digest(r.Files[1].Data)) {
		t.Fatal("missing platform content digest")
	}
}
func hasPlanIssue(p Plan, code string) bool {
	for _, i := range p.Issues {
		if i.Code == code {
			return true
		}
	}
	return false
}
func TestAggregateTextSpill(t *testing.T) {
	r := resource("a", "large", "s", "entry")
	for i := 0; i < 8; i++ {
		r.Files = append(r.Files, File{Path: fmt.Sprintf("references/%d.md", i), MIME: "text/markdown", Data: []byte(strings.Repeat("x", 200000))})
	}
	r.Plugin.Package = testPackage(r.Files)
	p, e := Build(Input{SnapshotID: "s", Resources: []Resource{r}}, Policy{})
	if e != nil {
		t.Fatal(p.Issues)
	}
	if len(p.Nodes[0].Plugin.Package) > 1<<20 {
		t.Fatal("aggregate inline package exceeds bound")
	}
	if p.Nodes[0].Files[0].ObjectKey != "" {
		t.Fatal("entry spilled")
	}
	r.Plugin.SpaceID = nil
	r.Plugin.Visibility = model.PluginVisibilitySystem
	p, e = Build(Input{SnapshotID: "s", Resources: []Resource{r}}, Policy{})
	if e != nil {
		t.Fatal(p.Issues)
	}
	n := p.Nodes[0]
	if len(n.Files) != len(r.Files) || len(n.Plugin.Package) > 1<<20 {
		t.Fatal("platform aggregate spill lost files or exceeded package budget")
	}
	var side map[string]string
	if json.Unmarshal(n.Plugin.AttachmentKeys, &side) != nil || len(side) == 0 {
		t.Fatal("platform aggregate spill missing sidecars")
	}
	for _, f := range n.Files {
		if f.ObjectKey != "" && (!strings.HasPrefix(f.ObjectKey, "platform/plugins/"+n.Plugin.ID+"/attachments/versions/") || side[f.Path] != f.ObjectKey || !strings.Contains(string(n.Artifacts), "sha256:"+digest(f.Data))) {
			t.Fatal("platform aggregate spill lost content digest or scope")
		}
	}
}

func testPackage(files []File) json.RawMessage {
	atts := []map[string]any{}
	for _, f := range files {
		if strings.ContainsRune(string(f.Data), 0) || f.MIME == "application/octet-stream" {
			atts = append(atts, map[string]any{"path": f.Path, "mime_type": f.MIME, "content_type": "storage", "content_size": len(f.Data), "content_hash": "sha256:" + digest(f.Data)})
		} else {
			atts = append(atts, map[string]any{"path": f.Path, "mime_type": f.MIME, "content_type": "raw", "raw_content": string(f.Data)})
		}
	}
	raw, _ := json.Marshal(map[string]any{"$schema": "cowork-plugin-package-2.0.json", "attachments": atts})
	return raw
}

func TestStorageScopeRejectsMalformedOwnership(t *testing.T) {
	for _, tc := range []struct {
		name, id, space string
		visibility      model.PluginVisibility
	}{
		{"system traversal", "../id", "", model.PluginVisibilitySystem},
		{"system empty id", "", "", model.PluginVisibilitySystem},
		{"space traversal", "id", "../space", model.PluginVisibilitySpace},
		{"space missing", "id", "", model.PluginVisibilitySpace},
		{"private missing", "id", "", model.PluginVisibilityPrivate},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := resource(tc.id, "binary", tc.space, "body")
			r.Plugin.Visibility = tc.visibility
			r.Plugin.OwnerUID = "owner"
			r.Files = append(r.Files, File{Path: "image.bin", MIME: "application/octet-stream", Data: []byte{0, 255}})
			n := Node{Plugin: r.Plugin, Files: r.Files}
			if code := prepareFiles(&n, "snapshot"); code != "storage_scope_invalid" {
				t.Fatalf("invalid scope returned %q", code)
			}
		})
	}
}

func TestPlatformStorageUsesFinalResourceIdentity(t *testing.T) {
	a := resource("old-a", "skill-a", "", "a")
	b := resource("old-b", "skill-b", "", "b")
	for _, r := range []*Resource{&a, &b} {
		r.Files = append(r.Files, File{Path: "image.bin", MIME: "application/octet-stream", Data: []byte{0, 255}})
	}
	p, err := Build(Input{SnapshotID: "s", Resources: []Resource{a, b}}, Policy{})
	if err != nil || len(p.Nodes) != 2 {
		t.Fatalf("plan: %v %+v", err, p.Issues)
	}
	keys := map[string]bool{}
	for _, n := range p.Nodes {
		if n.Plugin.ID == n.SourceID {
			t.Fatal("expected cloned system skill")
		}
		key := n.Files[1].ObjectKey
		if !strings.HasPrefix(key, "platform/plugins/"+n.Plugin.ID+"/attachments/versions/") || keys[key] {
			t.Fatal("platform object did not isolate final resource identity")
		}
		keys[key] = true
	}
}

// A converted catalog must retain its display metadata and expose previously
// embedded Experts as independent resources, while Teams point at their release.
func TestMigrationRetainsCatalogMetadataAndExtractsEmbeddedExperts(t *testing.T) {
	skill := resource("skill-source", "sales-playbook", "", "original Skill body")
	skill.Plugin.Tags = json.RawMessage(`["销售","客户研究"]`)
	category := "sales-category"
	skill.Plugin.CategoryID = &category
	skill.Plugin.Icon = "icons/sales.png"
	skill.IconFile = &File{Path: skill.Plugin.Icon, ObjectKey: skill.Plugin.Icon, MIME: "image/png", Data: []byte("icon")}
	skill.Evidence.Objects = []SourceObject{{Key: skill.Plugin.Icon, Hash: digest([]byte("icon")), Size: 4}}
	skill.Plugin.Publisher = "官方发布"
	skill.Plugin.CreatorName = "catalog-owner"
	skill.Plugin.Manifest = json.RawMessage(`{"$schema":"cowork-plugin-manifest-2.0.json","plugin_name":"sales-playbook","plugin_type":"skill","name":"销售技能","description":"保留这段技能卡片介绍","labels":["销售"],"examples":[]}`)
	expert := resource("embedded-expert", "销售专家", "", "unused")
	expert.Plugin.Type = model.PluginTypeExpert
	expert.Plugin.IsEmbedded = true
	expert.Plugin.Manifest = json.RawMessage(`{"$schema":"cowork-plugin-manifest-2.0.json","plugin_name":"销售专家","plugin_type":"expert","name":"销售专家","description":"保留专家介绍","labels":[],"examples":[]}`)
	expert.Files = []File{{Path: "AGENTS.md", MIME: "text/markdown", Data: []byte("original expert instructions")}}
	expert.Plugin.Package = testPackage(expert.Files)
	expert.Relations = []model.PluginRelation{{ID: "skill-edge", SourcePluginID: expert.Plugin.ID, SourcePluginType: model.PluginTypeExpert, TargetPluginID: skill.Plugin.ID, TargetPluginType: model.PluginTypeSkill, Type: "expert_skill", Status: 1}}
	team := resource("team-source", "销售作战专家团", "", "unused")
	team.Plugin.Type = model.PluginTypeExpertTeam
	team.Plugin.Manifest = json.RawMessage(`{"$schema":"cowork-plugin-manifest-2.0.json","plugin_name":"销售作战专家团","plugin_type":"expert_team","name":"销售作战专家团","description":"保留专家团介绍","labels":[],"examples":[]}`)
	team.Files = []File{{Path: "AGENTS.md", MIME: "text/markdown", Data: []byte("original team instructions")}}
	team.Plugin.Package = testPackage(team.Files)
	team.Relations = []model.PluginRelation{{ID: "member-edge", SourcePluginID: team.Plugin.ID, SourcePluginType: model.PluginTypeExpertTeam, TargetPluginID: expert.Plugin.ID, TargetPluginType: model.PluginTypeExpert, Type: "expert_team_expert", Status: 1, Data: json.RawMessage(`{"member_key":"sales-leader","role":"leader","is_leader":true}`)}}
	connector := resource("connector-source", "平台搜索", "", "unused")
	connector.Plugin.Type = model.PluginTypeConnector
	connector.Plugin.Manifest = json.RawMessage(`{"$schema":"cowork-plugin-manifest-2.0.json","plugin_name":"平台搜索","plugin_type":"connector","name":"平台搜索","description":"保留 MCP 介绍","labels":[],"examples":[]}`)
	connector.Files = []File{{Path: "mcp.json", MIME: "application/json", Data: []byte(`{"mcpServers":{"search":{"command":"search","args":["--source","original"]}}}`)}}
	var connectorPackage map[string]json.RawMessage
	_ = json.Unmarshal(testPackage(connector.Files), &connectorPackage)
	connectorPackage["connector"] = json.RawMessage(`{"type":"mcp","source":"fixture"}`)
	connector.Plugin.Package, _ = json.Marshal(connectorPackage)
	sources := []Resource{team, expert, skill, connector}
	plan, err := Build(Input{SnapshotID: "catalog-input", Resources: sources}, Policy{})
	if err != nil {
		t.Fatalf("build: %v issues=%v", err, plan.Issues)
	}
	if len(plan.Nodes) != 4 {
		t.Fatalf("lost catalog resources: %d", len(plan.Nodes))
	}
	nodes := map[string]Node{}
	for _, node := range plan.Nodes {
		nodes[node.SourceID] = node
	}
	for _, source := range sources {
		node, ok := nodes[source.Plugin.ID]
		if !ok {
			t.Fatalf("missing %s", source.Plugin.ID)
		}
		if node.Plugin.IsEmbedded || node.Plugin.Name != source.Plugin.Name || node.Plugin.Visibility != source.Plugin.Visibility || node.Plugin.SpaceID != nil || !node.Release {
			t.Fatalf("catalog ownership or display changed: %+v", node.Plugin)
		}
		var before, after map[string]any
		if json.Unmarshal(source.Plugin.Manifest, &before) != nil || json.Unmarshal(node.Plugin.Manifest, &after) != nil {
			t.Fatal("invalid manifest")
		}
		for _, key := range []string{"plugin_name", "name", "description", "labels", "examples"} {
			if !reflect.DeepEqual(before[key], after[key]) {
				t.Fatalf("%s lost display field %s: %v -> %v", source.Plugin.ID, key, before[key], after[key])
			}
		}
		if !reflect.DeepEqual(source.Files[0].Data, node.Files[0].Data) {
			t.Fatal("entry document bytes changed")
		}
	}
	convertedSkill, convertedExpert, convertedTeam := nodes[skill.Plugin.ID], nodes[expert.Plugin.ID], nodes[team.Plugin.ID]
	if convertedSkill.Plugin.CategoryID == nil || *convertedSkill.Plugin.CategoryID != category || string(convertedSkill.Plugin.Tags) != string(skill.Plugin.Tags) || convertedSkill.Plugin.Icon != skill.Plugin.Icon || convertedSkill.Plugin.Publisher != skill.Plugin.Publisher || convertedSkill.Plugin.CreatorName != skill.Plugin.CreatorName {
		t.Fatal("lost Skill card metadata")
	}
	if convertedExpert.Plugin.ID == expert.Plugin.ID {
		t.Fatal("embedded expert did not receive independent identity")
	}
	for _, edge := range []struct{ parent, child Node }{{convertedTeam, convertedExpert}, {convertedExpert, convertedSkill}} {
		if len(edge.parent.Relations) != 1 {
			t.Fatal("lost dependency")
		}
		r := edge.parent.Relations[0]
		if r.TargetPluginID != edge.child.Plugin.ID || r.TargetVersionID == nil || *r.TargetVersionID != edge.child.VersionID {
			t.Fatal("dependency does not select extracted published child")
		}
	}
	if !expert.Plugin.IsEmbedded {
		t.Fatal("planner mutated the original input")
	}
}

func TestDelistedPublicationSurvivesConversion(t *testing.T) {
	for _, kind := range []model.PluginType{model.PluginTypeSkill, model.PluginTypeExpert} {
		t.Run(string(kind), func(t *testing.T) {
			r := resource("source", "source", "", "body")
			r.Plugin.ListingState = model.PluginListingStateDelisted
			if kind == model.PluginTypeExpert {
				r.Plugin.Type = kind
				r.Plugin.Manifest = json.RawMessage(`{"$schema":"cowork-plugin-manifest-2.0.json","plugin_name":"source","plugin_type":"expert","name":"source","description":"test","labels":[],"examples":[]}`)
				r.Files = []File{{Path: "AGENTS.md", MIME: "text/markdown", Data: []byte("body")}}
				r.Plugin.Package = testPackage(r.Files)
			}
			r.PublishedVersionID = stringPtrApply("old")
			in := Input{SnapshotID: "snap", Resources: []Resource{r}, Versions: []model.PluginVersion{{ID: "old", PluginID: "source", Manifest: r.Plugin.Manifest, Package: r.Plugin.Package, Relations: json.RawMessage(`[]`)}}, Releases: []model.PluginVersionRelease{{PluginID: "source", VersionID: "old", VersionLabel: "1.0.0", ReleasedBy: "owner", ReleasedAt: r.Plugin.UpdatedAt}}}
			p, e := Build(in, Policy{})
			if e != nil {
				t.Fatalf("build: %v %#v", e, p.Issues)
			}
			n := p.Nodes[0]
			if !n.Release || n.Plugin.ListingState != model.PluginListingStateDelisted {
				t.Fatalf("publication dropped or relisted: released=%v listing=%s", n.Release, n.Plugin.ListingState)
			}
			if n.SourcePublishedVersionID == nil || *n.SourcePublishedVersionID != "old" || n.SourceRelease == nil || n.SourceRelease.ReleasedBy != "owner" || !n.SourceRelease.ReleasedAt.Equal(r.Plugin.UpdatedAt) {
				t.Fatal("source publication evidence was not captured")
			}
			if n.VersionID == "old" || n.VersionID == "" {
				t.Fatalf("target must own a new version: %#v", n)
			}
		})
	}
}

func TestSharedIconCannotBindConflictingByteSnapshots(t *testing.T) {
	a := resource("a", "first", "", "body")
	b := resource("b", "second", "", "body")
	for i, r := range []*Resource{&a, &b} {
		data := []byte{byte(i)}
		r.Plugin.Icon = "icons/upload/logo.png"
		r.IconFile = &File{Path: r.Plugin.Icon, ObjectKey: r.Plugin.Icon, Data: data}
		r.Evidence.Objects = []SourceObject{{Key: r.Plugin.Icon, Hash: digest(data), Size: 1}}
	}
	if p, err := Build(Input{SnapshotID: "icons", Resources: []Resource{a, b}}, Policy{}); err != ErrIssues {
		t.Fatalf("conflicting shared icon accepted: %v %+v", err, p.Issues)
	}
}
