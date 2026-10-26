package plugin

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func samplePinnedSkill(id, version string) PinnedSkillFiles {
	return PinnedSkillFiles{PluginID: id, VersionID: version, Name: "Search", Description: "Fixture", Files: map[string]string{"SKILL.md": "---\nname: Search\ndescription: fixture\n---\nUse Search and ./refs/help.md unchanged.\n", "refs/help.md": "Search"}}
}

func TestPreparePinnedSkillsIdentityAndCopy(t *testing.T) {
	a, b, c := samplePinnedSkill("a", "v1"), samplePinnedSkill("b", "v1"), samplePinnedSkill("a", "v2")
	original := a.Files["SKILL.md"]
	got, err := PreparePinnedSkills([]PinnedSkillFiles{a, b, c, a})
	if err != nil || len(got) != 1 {
		t.Fatalf("prepare: count=%d err=%v", len(got), err)
	}
	repeat, err := PreparePinnedSkills([]PinnedSkillFiles{c, b, a})
	if err != nil || !reflect.DeepEqual(got, repeat) {
		t.Fatal("output must be order independent")
	}
	names := map[string]bool{}
	for _, item := range got {
		n := item.Skill.Name
		if names[n] || n != "Search" || n != item.Directory || item.Skill.Content != original {
			t.Fatal("identity/frontmatter mismatch")
		}
		names[n] = true
		if !strings.HasSuffix(item.Skill.Content, "Use Search and ./refs/help.md unchanged.\n") || item.Skill.Files[0].Source.Content != "Search" {
			t.Fatal("body or attachment modified")
		}
	}
	if a.Files["SKILL.md"] != original {
		t.Fatal("source changed")
	}
	changed := samplePinnedSkill("a", "v1")
	changed.Files["refs/help.md"] = "different"
	if _, err = PreparePinnedSkills([]PinnedSkillFiles{a, changed}); err == nil {
		t.Fatal("conflicting identity accepted")
	}
}

func TestPreparePinnedSkillsRejectsSameNameDifferentContent(t *testing.T) {
	a, b := samplePinnedSkill("a", "v1"), samplePinnedSkill("b", "v2")
	b.Files["refs/help.md"] = "new content"
	if _, err := PreparePinnedSkills([]PinnedSkillFiles{a, b}); err == nil {
		t.Fatal("same market name with different supporting content accepted")
	}
}

func TestPreparePinnedSkillsInvalid(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*PinnedSkillFiles)
	}{
		{"missing identity", func(s *PinnedSkillFiles) { s.VersionID = "" }},
		{"path traversal", func(s *PinnedSkillFiles) { s.Files["../bad"] = "bad" }},
		{"reserved path", func(s *PinnedSkillFiles) { s.Files[".git/config"] = "bad" }},
		{"binary", func(s *PinnedSkillFiles) { s.Files["refs/help.md"] = "\x00" }},
		{"large file", func(s *PinnedSkillFiles) { s.Files["refs/help.md"] = strings.Repeat("a", (1<<20)+1) }},
		{"invalid yaml", func(s *PinnedSkillFiles) { s.Files["SKILL.md"] = "---\nname: [\n---\nbody" }},
		{"duplicate key", func(s *PinnedSkillFiles) { s.Files["SKILL.md"] = "---\nname: x\nname: y\n---\nbody" }},
		{"anchor", func(s *PinnedSkillFiles) { s.Files["SKILL.md"] = "---\nname: &a x\ncopy: *a\n---\nbody" }},
		{"too many files", func(s *PinnedSkillFiles) {
			for i := 0; i < 51; i++ {
				s.Files[fmt.Sprintf("file%d", i)] = "ok"
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := samplePinnedSkill("a", "v")
			tc.mutate(&s)
			if _, err := PreparePinnedSkills([]PinnedSkillFiles{s}); err == nil {
				t.Fatal("invalid input accepted")
			}
		})
	}
}

func TestInstallFrontmatterBodyPreserved(t *testing.T) {
	for _, input := range []string{"body Search\r\n", "---\r\nname: old\r\n---\r\nbody Search\r\n"} {
		got, err := rewriteInstallFrontmatter(input, "new-name")
		if err != nil || !strings.HasSuffix(got, "body Search\r\n") || !strings.Contains(got, "name: new-name\n") {
			t.Fatalf("rewrite failed: %v", err)
		}
	}
}

func TestMergeCurrentConnectorMCP(t *testing.T) {
	a := CurrentConnectorMCP{"a", `{"mcpServers":{"search":{"command":"fixture"}}}`}
	b := CurrentConnectorMCP{"b", `{"mcpServers":{"docs":{"url":"https://example.invalid/mcp"}}}`}
	got, err := MergeCurrentConnectorMCP([]CurrentConnectorMCP{a, b, a})
	if err != nil {
		t.Fatal(err)
	}
	reverse, err := MergeCurrentConnectorMCP([]CurrentConnectorMCP{b, a})
	if err != nil || string(got) != string(reverse) {
		t.Fatal("unstable merge")
	}
	updated := a
	updated.Config = `{"mcpServers":{"search":{"command":"updated"}}}`
	next, err := MergeCurrentConnectorMCP([]CurrentConnectorMCP{updated, b})
	if err != nil || string(next) == string(got) {
		t.Fatal("current config ignored")
	}
	for _, inputs := range [][]CurrentConnectorMCP{
		{a, updated}, {a, {"other", a.Config}}, {{"", a.Config}},
		{{"bad", `{"mcpServers":{"x":{"type":"stdio","command":"fixture"}}}`}},
		{{"bad", `{"mcpServers":{"x":{"command":"fixture","secret":"do-not-log"}}}`}},
	} {
		if _, err := MergeCurrentConnectorMCP(inputs); err == nil {
			t.Fatal("invalid/conflicting config accepted")
		} else if strings.Contains(err.Error(), "do-not-log") {
			t.Fatal("error leaks input")
		}
	}
	var many []CurrentConnectorMCP
	for i := 0; i < 101; i++ {
		raw, _ := json.Marshal(map[string]any{"mcpServers": map[string]any{fmt.Sprintf("s%d", i): map[string]string{"command": "fixture"}}})
		many = append(many, CurrentConnectorMCP{fmt.Sprint(i), string(raw)})
	}
	if _, err := MergeCurrentConnectorMCP(many); err == nil {
		t.Fatal("server budget ignored")
	}
}

func TestPreparePinnedSkillsAggregateBudget(t *testing.T) {
	s := samplePinnedSkill("a", "v1")
	for i := 0; i < 17; i++ {
		s.Files[fmt.Sprintf("large%d", i)] = strings.Repeat("a", 1<<20)
	}
	if _, err := PreparePinnedSkills([]PinnedSkillFiles{s}); err == nil {
		t.Fatal("aggregate text budget ignored")
	}
}

func TestMergeCurrentConnectorMCPInputBudget(t *testing.T) {
	large := "{\"mcpServers\":{}}" + strings.Repeat(" ", 16<<20)
	if _, err := MergeCurrentConnectorMCP([]CurrentConnectorMCP{{"a", large}}); err == nil {
		t.Fatal("raw input budget ignored")
	}
}
