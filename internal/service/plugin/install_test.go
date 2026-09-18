package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	expertsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/expert"
)

type fakeProvisioner struct {
	in        expertsvc.InstallInput
	agentSpec *expertsvc.ProvisionAgentSpec
	squad     *model.Squad
	err       error
}

func (f *fakeProvisioner) ProvisionAgentFromSpec(_ context.Context, in expertsvc.InstallInput, spec expertsvc.ProvisionAgentSpec) (string, error) {
	f.in, f.agentSpec = in, &spec
	return "agent-9", f.err
}
func (f *fakeProvisioner) ProvisionSquadFromSpec(_ context.Context, in expertsvc.InstallInput, m *model.Squad) (expertsvc.InstallSquadResult, error) {
	f.in, f.squad = in, m
	return expertsvc.InstallSquadResult{SquadID: "squad-9", LeaderAgentID: "agent-1"}, f.err
}

type fakeTracker struct {
	typ, id string
}

func (f *fakeTracker) TrackInstall(_ context.Context, resourceType, resourceID string) error {
	f.typ, f.id = resourceType, resourceID
	return nil
}
func (f *fakeTracker) TrackDownload(_ context.Context, resourceType, resourceID string) error {
	f.typ, f.id = resourceType, resourceID
	return nil
}

func packageWith(attachments ...string) json.RawMessage {
	return json.RawMessage(`{"$schema":"cowork-plugin-package-2.0.json","attachments":[` + joinComma(attachments) + `]}`)
}
func joinComma(items []string) string {
	out := ""
	for i, item := range items {
		if i > 0 {
			out += ","
		}
		out += item
	}
	return out
}
func rawAtt(path, content string) string {
	quotedContent, _ := json.Marshal(content)
	return `{"path":"` + path + `","content_type":"raw","mime_type":"text/plain","raw_content":` + string(quotedContent) + `}`
}

func installFixture() *fakeStore {
	space := "space-a"
	skillPkg := packageWith(rawAtt("skill/ref.json", `{"file_name":"pack.zip","file_size":9,"files":["a.md"],"object_key":"plugins/space-a/attachments/skill.md","zip_object_key":"plugins/space-a/attachments/pack.zip"}`))
	expertPkg := packageWith(rawAtt("AGENTS.md", "do the work"), rawAtt("mcp.json", `{"mcpServers":{}}`))
	teamPkg := packageWith(rawAtt("AGENTS.md", "# Team\n\n## 协作方式\n1. first\n2. second"))
	return &fakeStore{
		plugins: map[string]*model.Plugin{
			"expert-1": {ID: "expert-1", Name: "Alice", Type: model.PluginTypeExpert, OwnerUID: "user-1", SpaceID: &space, Manifest: json.RawMessage(`{"description":"expert summary"}`), Package: expertPkg},
			"team-1":   {ID: "team-1", Name: "Team", Type: model.PluginTypeExpertTeam, OwnerUID: "user-1", SpaceID: &space, Manifest: json.RawMessage(`{"description":"team summary"}`), Package: teamPkg},
			"skill-1":  {ID: "skill-1", Name: "Deploy", Type: model.PluginTypeSkill, OwnerUID: "user-1", SpaceID: &space, Manifest: json.RawMessage(`{}`), Package: skillPkg},
		},
		relations: map[string][]model.PluginRelation{
			"expert-1": {{ID: "r1", SourcePluginID: "expert-1", TargetPluginID: "skill-1", TargetPluginType: model.PluginTypeSkill, Type: "expert_skill", SortOrder: 0, Status: 1}},
			"team-1":   {{ID: "r2", SourcePluginID: "team-1", TargetPluginID: "expert-1", TargetPluginType: model.PluginTypeExpert, Type: "expert_team_expert", SortOrder: 0, Status: 1, Data: json.RawMessage(`{"member_key":"m1","role":"leader","is_leader":true}`)}},
		},
	}
}

func TestInstallExpertBuildsSpecFromAttachmentsAndRelations(t *testing.T) {
	f := installFixture()
	prov := &fakeProvisioner{}
	tracker := &fakeTracker{}
	svc := fixedService(f).WithProvisioner(prov).WithMetrics(tracker)
	outcome, err := svc.Install(context.Background(), testCaller, "expert-1", InstallParams{
		WorkspaceID: " ws-1 ",
		RuntimeID:   "rt-1",
		CustomEnv:   map[string]string{"OCTOBUDDY_PROVIDER_ID": "provider-1"},
		Token:       "octo-token",
	})
	if err != nil {
		t.Fatal(err)
	}
	if outcome.AgentID != "agent-9" || outcome.SquadID != "" {
		t.Fatalf("outcome = %#v", outcome)
	}
	if prov.in.WorkspaceID != "ws-1" || prov.in.RuntimeID != "rt-1" || prov.in.SpaceID != "space-a" || prov.in.Token != "octo-token" {
		t.Fatalf("install input = %#v", prov.in)
	}
	if prov.in.CustomEnv["OCTOBUDDY_PROVIDER_ID"] != "provider-1" {
		t.Fatalf("custom env = %#v", prov.in.CustomEnv)
	}
	spec := prov.agentSpec
	if spec.Name != "Alice" || spec.Summary != "expert summary" || spec.Instruction != "do the work" || spec.MCPConfig != `{"mcpServers":{}}` {
		t.Fatalf("spec = %#v", spec)
	}
	if len(spec.Skills) != 1 || spec.Skills[0].Name != "Deploy" || spec.Skills[0].ObjectKey != "plugins/space-a/attachments/skill.md" || spec.Skills[0].ZipObjectKey != "plugins/space-a/attachments/pack.zip" {
		t.Fatalf("skills = %#v", spec.Skills)
	}
	if tracker.typ != "plugin" || tracker.id != "expert-1" {
		t.Fatalf("tracked = %s %s", tracker.typ, tracker.id)
	}
}

func TestInstallTeamBuildsSquadModelFromAgentsDocAndMembers(t *testing.T) {
	f := installFixture()
	prov := &fakeProvisioner{}
	svc := fixedService(f).WithProvisioner(prov)
	outcome, err := svc.Install(context.Background(), testCaller, "team-1", InstallParams{
		WorkspaceID: "ws-1",
		RuntimeID:   "rt-1",
		CustomEnv:   map[string]string{"OCTOBUDDY_PROVIDER_ID": "provider-1"},
		Token:       "octo-token",
	})
	if err != nil {
		t.Fatal(err)
	}
	if outcome.SquadID != "squad-9" || outcome.AgentID != "" {
		t.Fatalf("outcome = %#v", outcome)
	}
	if prov.in.CustomEnv["OCTOBUDDY_PROVIDER_ID"] != "provider-1" {
		t.Fatalf("custom env = %#v", prov.in.CustomEnv)
	}
	squad := prov.squad
	// Contract layout: the AGENTS.md prose is the squad's dispatch document;
	// leadership comes from member relation data, not a config attachment.
	if squad.Name != "Team" || squad.Summary != "team summary" || squad.Leader != "" {
		t.Fatalf("squad = %#v", squad)
	}
	if squad.Instructions != "# Team\n\n## 协作方式\n1. first\n2. second" {
		t.Fatalf("instructions = %q", squad.Instructions)
	}
	if len(squad.Strategies) != 0 {
		t.Fatalf("strategies = %#v", squad.Strategies)
	}
	if len(squad.Members) != 1 {
		t.Fatalf("members = %#v", squad.Members)
	}
	member := squad.Members[0]
	if member.Name != "Alice" || member.Role != "leader" || !member.IsLeader || member.MemberKey != "m1" || member.Instruction != "do the work" {
		t.Fatalf("member = %#v", member)
	}
	if len(member.Skills) != 1 || member.Skills[0].ZipObjectKey != "plugins/space-a/attachments/pack.zip" {
		t.Fatalf("member skills = %#v", member.Skills)
	}
}

// TestInstallDropsForgedCrossSpaceSkillRef proves a caller-forged skill/ref.json
// pointing outside the plugin's own Space is not handed to the provisioner: the
// object/zip keys are dropped, so no cross-Space or arbitrary bucket object is
// fetched during install.
func TestInstallDropsForgedCrossSpaceSkillRef(t *testing.T) {
	space := "space-a"
	forged := packageWith(rawAtt("skill/ref.json", `{"file_name":"x.zip","object_key":"plugins/space-b/attachments/loot.md","zip_object_key":"experts/other/skill.zip"}`))
	f := &fakeStore{
		plugins: map[string]*model.Plugin{
			"expert-1": {ID: "expert-1", Name: "Alice", Type: model.PluginTypeExpert, OwnerUID: "user-1", SpaceID: &space, Manifest: json.RawMessage(`{"description":"x"}`), Package: packageWith(rawAtt("AGENTS.md", "w"))},
			"skill-1":  {ID: "skill-1", Name: "Deploy", Type: model.PluginTypeSkill, OwnerUID: "user-1", SpaceID: &space, Manifest: json.RawMessage(`{}`), Package: forged},
		},
		relations: map[string][]model.PluginRelation{
			"expert-1": {{ID: "r1", SourcePluginID: "expert-1", TargetPluginID: "skill-1", TargetPluginType: model.PluginTypeSkill, Type: "expert_skill", Status: 1}},
		},
	}
	prov := &fakeProvisioner{}
	svc := fixedService(f).WithProvisioner(prov).WithMetrics(&fakeTracker{})
	if _, err := svc.Install(context.Background(), testCaller, "expert-1", InstallParams{WorkspaceID: "ws", RuntimeID: "rt", Token: "t"}); err != nil {
		t.Fatal(err)
	}
	if len(prov.agentSpec.Skills) != 1 {
		t.Fatalf("skills = %#v", prov.agentSpec.Skills)
	}
	if prov.agentSpec.Skills[0].ObjectKey != "" || prov.agentSpec.Skills[0].ZipObjectKey != "" {
		t.Fatalf("forged cross-Space keys must be dropped, got %#v", prov.agentSpec.Skills[0])
	}
}

// TestInstallRefusesWhenDeclaredDependencyHidden is the P1-1 regression: when the
// installing caller cannot see a declared relation target (a shared parent that
// depends on a private child), the visibility-filtered read yields a short
// relation list. Install must refuse loudly with ErrDependencyHidden rather than
// provisioning an expert with missing skills (or a squad missing members) and
// returning success. The declared count exceeds the visible relations here, which
// is exactly the drop CountDeclaredRelations detects.
func TestInstallRefusesWhenDeclaredDependencyHidden(t *testing.T) {
	space := "space-a"
	f := &fakeStore{
		plugins: map[string]*model.Plugin{
			"expert-1": {ID: "expert-1", Name: "Alice", Type: model.PluginTypeExpert, OwnerUID: "user-1", SpaceID: &space, Manifest: json.RawMessage(`{"description":"x"}`), Package: packageWith(rawAtt("AGENTS.md", "w"))},
		},
		// The caller sees zero relations (the private skill target was filtered),
		// but the plugin declares one — the hidden-dependency condition.
		relations:      map[string][]model.PluginRelation{"expert-1": {}},
		declaredCounts: map[string]int{"expert-1": 1},
	}
	svc := fixedService(f).WithProvisioner(&fakeProvisioner{}).WithMetrics(&fakeTracker{})
	if _, err := svc.Install(context.Background(), testCaller, "expert-1", InstallParams{WorkspaceID: "ws", RuntimeID: "rt", Token: "t"}); !errors.Is(err, ErrDependencyHidden) {
		t.Fatalf("install with a hidden declared dependency err = %v, want ErrDependencyHidden", err)
	}
}

func TestInstallRejectsNonInstallableTypesAndMissingProvisioner(t *testing.T) {
	f := installFixture()
	svc := fixedService(f).WithProvisioner(&fakeProvisioner{})
	if _, err := svc.Install(context.Background(), testCaller, "skill-1", InstallParams{WorkspaceID: "ws", RuntimeID: "rt"}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("skill install err = %v", err)
	}
	if _, err := svc.Install(context.Background(), testCaller, "expert-1", InstallParams{WorkspaceID: "", RuntimeID: "rt"}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("missing workspace err = %v", err)
	}
	bare := fixedService(installFixture())
	if _, err := bare.Install(context.Background(), testCaller, "expert-1", InstallParams{WorkspaceID: "ws", RuntimeID: "rt"}); !errors.Is(err, expertsvc.ErrFleetNotConfigured) {
		t.Fatalf("no provisioner err = %v", err)
	}
}

func TestInstallSanitizesUnknownPlugin(t *testing.T) {
	svc := fixedService(installFixture()).WithProvisioner(&fakeProvisioner{})
	if _, err := svc.Install(context.Background(), testCaller, "other-space", InstallParams{WorkspaceID: "ws", RuntimeID: "rt"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestSkillRefPrefersStoragePackageWhenLegacyPointerAbsent(t *testing.T) {
	space := "space-a"
	pkg := json.RawMessage(`{"attachments":[{"path":"skill/package.zip","content_type":"storage"}]}`)
	keys := json.RawMessage(`{"skill/package.zip":"plugins/space-a/attachments/x.zip"}`)
	svc := New(&fakeStore{}, &importStorage{objects: map[string][]byte{}})
	ref, err := svc.skillRefFromPlugin(context.Background(), &model.Plugin{Name: "New Skill", SpaceID: &space, Package: pkg, AttachmentKeys: keys}, freshBudget())
	if err != nil {
		t.Fatal(err)
	}
	if ref.Name != "New Skill" || ref.ZipObjectKey != "plugins/space-a/attachments/x.zip" {
		t.Fatalf("ref = %#v", ref)
	}

	// Q5: a package.zip key outside this plugin's own Space prefix is dropped
	// rather than handed to the provisioner, matching skill/ref.json scoping.
	forgedKeys := json.RawMessage(`{"skill/package.zip":"plugins/space-b/attachments/x.zip"}`)
	ref, err = svc.skillRefFromPlugin(context.Background(), &model.Plugin{Name: "New Skill", SpaceID: &space, Package: pkg, AttachmentKeys: forgedKeys}, freshBudget())
	if err != nil {
		t.Fatal(err)
	}
	if ref.ZipObjectKey != "" {
		t.Fatalf("cross-Space package.zip key trusted: %#v", ref)
	}
}

func TestSkillRefResolvesTreeContentInline(t *testing.T) {
	space := "space-a"
	binKey := "plugins/space-a/attachments/asset.bin"
	txtKey := "plugins/space-a/attachments/big.txt"
	pkg := json.RawMessage(`{"attachments":[` +
		`{"path":"SKILL.md","content_type":"raw","raw_content":"# doc"},` +
		`{"path":"scripts/run.sh","content_type":"raw","raw_content":"echo hi"},` +
		`{"path":"references/big.txt","content_type":"storage"},` +
		`{"path":"assets/asset.bin","content_type":"storage"}` +
		`]}`)
	keys := json.RawMessage(`{"references/big.txt":"` + txtKey + `","assets/asset.bin":"` + binKey + `"}`)
	blobs := &importStorage{objects: map[string][]byte{
		txtKey: []byte("spilled text"),
		binKey: {0x00, 0xff, 0xfe},
	}}
	svc := New(&fakeStore{}, blobs)
	ref, err := svc.skillRefFromPlugin(context.Background(), &model.Plugin{Name: "Tree Skill", SpaceID: &space, Package: pkg, AttachmentKeys: keys}, freshBudget())
	if err != nil {
		t.Fatal(err)
	}

	if ref.Markdown != "# doc" || ref.ObjectKey != "" || ref.ZipObjectKey != "" {
		t.Fatalf("tree ref should resolve inline: %#v", ref)
	}
	got := map[string]string{}
	for _, f := range ref.SupportingFiles {
		got[f.Path] = f.Content
	}
	// Inline text + storage text are included; the binary is skipped.
	if got["scripts/run.sh"] != "echo hi" || got["references/big.txt"] != "spilled text" {
		t.Fatalf("supporting files = %#v", ref.SupportingFiles)
	}
	if _, ok := got["assets/asset.bin"]; ok {
		t.Fatalf("binary should be skipped: %#v", ref.SupportingFiles)
	}
}

// TestSkillRefFallsBackToInlineStorageURI locks the migration-window safety net:
// a row not yet migrated to the sidecar (AttachmentKeys nil) still resolves its
// storage attachment from the inline storage_uri the pre-2.0 package carries, so
// download/install do not silently drop files before the backfill runs.
func TestSkillRefFallsBackToInlineStorageURI(t *testing.T) {
	space := "space-a"
	txtKey := "plugins/space-a/attachments/big.txt"
	pkg := json.RawMessage(`{"attachments":[` +
		`{"path":"SKILL.md","content_type":"raw","raw_content":"# doc"},` +
		`{"path":"references/big.txt","content_type":"storage","storage_uri":"` + txtKey + `"}` +
		`]}`)
	blobs := &importStorage{objects: map[string][]byte{txtKey: []byte("spilled text")}}
	svc := New(&fakeStore{}, blobs)
	// AttachmentKeys deliberately nil — the un-migrated state.
	ref, err := svc.skillRefFromPlugin(context.Background(), &model.Plugin{Name: "Legacy Skill", SpaceID: &space, Package: pkg}, freshBudget())
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, f := range ref.SupportingFiles {
		got[f.Path] = f.Content
	}
	if got["references/big.txt"] != "spilled text" {
		t.Fatalf("inline storage_uri fallback dropped the file: %#v", ref.SupportingFiles)
	}
}

// TestSkillRefCapsSupportingFilesBeforeFetching is the Q4 install-side bound: a
// plugin_json packed with far more attachments than the per-skill budget yields
// at most maxInstallSupportingFiles, and the cap is applied before fetching so
// the GetObject fan-out stays bounded too.
func TestSkillRefCapsSupportingFilesBeforeFetching(t *testing.T) {
	space := "space-a"
	var b strings.Builder
	b.WriteString(`{"attachments":[{"path":"SKILL.md","content_type":"raw","raw_content":"# doc"}`)
	for i := 0; i < maxInstallSupportingFiles+40; i++ {
		fmt.Fprintf(&b, `,{"path":"f%d.txt","content_type":"raw","raw_content":"x"}`, i)
	}
	b.WriteString(`]}`)
	svc := New(&fakeStore{}, &importStorage{objects: map[string][]byte{}})
	ref, err := svc.skillRefFromPlugin(context.Background(), &model.Plugin{Name: "Big Skill", SpaceID: &space, Package: json.RawMessage(b.String())}, freshBudget())
	if err != nil {
		t.Fatal(err)
	}
	if len(ref.SupportingFiles) != maxInstallSupportingFiles {
		t.Fatalf("supporting files = %d, want capped at %d", len(ref.SupportingFiles), maxInstallSupportingFiles)
	}
}

// TestSkillRefChargesMarkdownAgainstBudget is the P1-3 regression: SKILL.md (the
// largest inline payload per skill) is charged against the shared install byte
// budget, so a skill whose document exhausts the budget contributes no further
// supporting files.
func TestSkillRefChargesMarkdownAgainstBudget(t *testing.T) {
	space := "space-a"
	budget := &installBudget{bytes: 10, targets: maxInstallRelationTargets} // exactly the SKILL.md length below
	pkg := json.RawMessage(`{"attachments":[{"path":"SKILL.md","content_type":"raw","raw_content":"0123456789"},{"path":"f.txt","content_type":"raw","raw_content":"x"}]}`)
	svc := New(&fakeStore{}, &importStorage{objects: map[string][]byte{}})
	ref, err := svc.skillRefFromPlugin(context.Background(), &model.Plugin{Name: "S", SpaceID: &space, Package: pkg}, budget)
	if err != nil {
		t.Fatal(err)
	}
	if ref.Markdown != "0123456789" {
		t.Fatalf("markdown = %q", ref.Markdown)
	}
	if budget.bytes > 0 {
		t.Fatalf("SKILL.md not charged against budget; remaining=%d", budget.bytes)
	}
	if len(ref.SupportingFiles) != 0 {
		t.Fatalf("supporting files should be dropped once SKILL.md exhausted the budget: %#v", ref.SupportingFiles)
	}
}

// TestSkillRefFromPluginFailsLoudWhenSkillMDOverBudget is the P1-2 regression:
// a tree-shaped skill whose SKILL.md exists but does not fit the remaining byte
// budget (0 < budget.bytes < len(md)) must fail loudly with ErrTooLarge, not
// return a document-less ref the provisioner silently drops. The caller-level
// budget.bytes<=0 gate does not cover this window because the budget is still
// positive when this skill is reached.
func TestSkillRefFromPluginFailsLoudWhenSkillMDOverBudget(t *testing.T) {
	space := "space-a"
	budget := &installBudget{bytes: 5, targets: maxInstallRelationTargets} // < len(SKILL.md) below, but > 0
	pkg := json.RawMessage(`{"attachments":[{"path":"SKILL.md","content_type":"raw","raw_content":"0123456789"}]}`)
	svc := New(&fakeStore{}, &importStorage{objects: map[string][]byte{}})
	if _, err := svc.skillRefFromPlugin(context.Background(), &model.Plugin{Name: "S", SpaceID: &space, Package: pkg}, budget); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("over-budget SKILL.md err = %v, want ErrTooLarge (loud, not a dropped skill)", err)
	}
}

// freshBudget returns a large per-call install budget for direct
// skillRefFromPlugin tests (the aggregate cap is exercised via the install flow).
func freshBudget() *installBudget {
	return &installBudget{bytes: int64(100) << 20, targets: maxInstallRelationTargets}
}

// TestSkillRefsFromRelationsFailsLoudOnExhaustedTargets is the r10 fix: once the
// install-wide target budget is spent, the fan-out must return ErrTooLarge
// rather than silently dropping targets (a partial/leaderless install reported
// as success).
func TestSkillRefsFromRelationsFailsLoudOnExhaustedTargets(t *testing.T) {
	space := "space-a"
	skillPkg := packageWith(rawAtt("SKILL.md", "# doc"))
	f := &fakeStore{plugins: map[string]*model.Plugin{
		"skill-a": {ID: "skill-a", Name: "A", Type: model.PluginTypeSkill, OwnerUID: "user-1", SpaceID: &space, Manifest: json.RawMessage(`{}`), Package: skillPkg},
		"skill-b": {ID: "skill-b", Name: "B", Type: model.PluginTypeSkill, OwnerUID: "user-1", SpaceID: &space, Manifest: json.RawMessage(`{}`), Package: skillPkg},
	}}
	rels := []model.PluginRelation{
		{TargetPluginID: "skill-a", Type: "expert_skill", Status: 1},
		{TargetPluginID: "skill-b", Type: "expert_skill", Status: 1},
	}
	budget := &installBudget{bytes: int64(100) << 20, targets: 1} // room for one target only
	_, err := fixedService(f).skillRefsFromRelations(context.Background(), testCaller, rels, budget)
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v, want ErrTooLarge (loud truncation)", err)
	}
}

// TestSquadFromPluginStopsWhenMemberDocsExhaustByteBudget is the P1-2
// regression: a team whose members carry instruction bytes but NO expert_skill
// relations must still charge those documents against the shared install byte
// budget and fail loudly once it is exhausted. Before the fix the member loop
// gated on budget.targets only, so a member-only team charged budget.bytes into
// deep-negative territory (the only byte gate lived in the skill loop, which
// never runs for a skill-less member) and returned success while retaining every
// member document past the aggregate ceiling.
func TestSquadFromPluginStopsWhenMemberDocsExhaustByteBudget(t *testing.T) {
	space := "space-a"
	big := strings.Repeat("x", 300) // per-member AGENTS.md bytes
	plugins := map[string]*model.Plugin{
		"team-1": {ID: "team-1", Name: "Team", Type: model.PluginTypeExpertTeam, OwnerUID: "user-1", SpaceID: &space, Manifest: json.RawMessage(`{"description":"t"}`), Package: packageWith(rawAtt("AGENTS.md", "# Team"))},
	}
	rels := make([]model.PluginRelation, 0, 5)
	for i := 0; i < 5; i++ {
		id := fmt.Sprintf("m%d", i)
		// Each member carries an instruction but no expert_skill relation of its own.
		plugins[id] = &model.Plugin{ID: id, Name: id, Type: model.PluginTypeExpert, OwnerUID: "user-1", SpaceID: &space, Manifest: json.RawMessage(`{}`), Package: packageWith(rawAtt("AGENTS.md", big))}
		rels = append(rels, model.PluginRelation{ID: "r" + id, SourcePluginID: "team-1", TargetPluginID: id, TargetPluginType: model.PluginTypeExpert, Type: "expert_team_expert", SortOrder: i, Status: 1, Data: json.RawMessage(`{"member_key":"` + id + `","role":"member"}`)})
	}
	f := &fakeStore{plugins: plugins, relations: map[string][]model.PluginRelation{"team-1": rels}}
	detail := &Detail{Plugin: plugins["team-1"], Relations: rels}
	// Budget below the sum of member instructions (5 × 300 = 1500), so the
	// fan-out must trip the byte gate before all members are materialized.
	budget := &installBudget{bytes: 700, targets: maxInstallRelationTargets}
	if _, err := fixedService(f).squadFromPlugin(context.Background(), testCaller, detail, budget); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("member-only team over byte budget err = %v, want ErrTooLarge", err)
	}
}
