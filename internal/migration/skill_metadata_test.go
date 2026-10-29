package migration

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func skillWithDescription(body, description string) Resource {
	r := resource("legacy-skill", "humanizer", "", body)
	var manifest map[string]any
	_ = json.Unmarshal(r.Plugin.Manifest, &manifest)
	manifest["description"] = description
	r.Plugin.Manifest, _ = json.Marshal(manifest)
	return r
}
func descriptionOf(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var manifest map[string]any
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	value, _ := manifest["description"].(string)
	return value
}
func TestMigrationFillsEmptyDescriptionFromOriginalSkillFrontmatter(t *testing.T) {
	body := "---\r\nname: humanizer\r\ndescription: >-\r\n  支持内容创作专家相关的 humanizer 任务，\r\n  按正文和参考资料执行。\r\n---\r\n\r\n# Humanizer\r\n完整原始操作说明。\r\n"
	r := skillWithDescription(body, "")
	p, err := Build(Input{SnapshotID: "prod-input", Resources: []Resource{r}}, Policy{})
	if err != nil {
		t.Fatalf("build: %v issues=%v", err, p.Issues)
	}
	n := p.Nodes[0]
	want := "支持内容创作专家相关的 humanizer 任务， 按正文和参考资料执行。"
	if got := descriptionOf(t, n.Plugin.Manifest); got != want {
		t.Fatalf("description=%q want %q", got, want)
	}
	if !bytes.Equal(n.Files[0].Data, []byte(body)) {
		t.Fatal("original Skill bytes changed")
	}
	if descriptionOf(t, n.Source.Manifest) != "" || descriptionOf(t, r.Plugin.Manifest) != "" {
		t.Fatal("source evidence was rewritten")
	}
	if !n.Release || n.VersionID == "" {
		t.Fatal("recovered display metadata lost release")
	}
}
func TestMigrationPreservesCuratedDescriptionOverSkillFrontmatter(t *testing.T) {
	r := skillWithDescription("---\nname: humanizer\ndescription: 文件内说明\n---\n完整正文", "人工维护的市场说明")
	p, err := Build(Input{SnapshotID: "prod-input", Resources: []Resource{r}}, Policy{})
	if err != nil {
		t.Fatal(err)
	}
	if descriptionOf(t, p.Nodes[0].Plugin.Manifest) != "人工维护的市场说明" {
		t.Fatal("curated description overwritten")
	}
}
func TestMigrationBlocksEmptyDescriptionsWithoutUsableOriginalMetadata(t *testing.T) {
	for _, body := range []string{
		"# humanizer\n",
		"---\nname: humanizer\n---\n正文没有描述",
		"---\ndescription: \"\"\n---\n正文",
		"---\ndescription: 123\n---\n正文",
		"---\ndescription: one\ndescription: two\n---\n正文",
		"---\ndescription: [\n---\n正文",
		"---\ndescription: text\n未关闭文件头",
		"---\ndescription: " + strings.Repeat("x", 65537) + "\n---\n正文",
	} {
		r := skillWithDescription(body, "")
		p, err := Build(Input{SnapshotID: "prod-input", Resources: []Resource{r}}, Policy{})
		if err == nil {
			t.Fatal("published incomplete Skill metadata")
		}
		found := false
		for _, issue := range p.Issues {
			if issue.ResourceID == r.Plugin.ID && issue.Code == "skill_description_unavailable" {
				found = true
			}
		}
		if !found {
			t.Fatalf("missing actionable metadata blocker: %v", p.Issues)
		}
	}
}
