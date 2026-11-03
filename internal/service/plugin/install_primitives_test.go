package plugin

import (
	"encoding/json"
	"errors"
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

func TestPreparePinnedSkillsKeepsVersionIdentity(t *testing.T) {
	for _, tc := range []struct {
		name        string
		left, right *string
		wantShared  bool
	}{
		{name: "different_versions", left: pin("1.2.3"), right: pin("2.0.0")},
		{name: "unversioned_and_versioned", right: pin("1.2.3")},
		{name: "versioned_and_unversioned", left: pin("1.2.3")},
		{name: "equivalent_sources_share_first_identity", left: pin("1.2.3"), right: pin("1.2.3"), wantShared: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			left, right := samplePinnedSkill("a", "v1"), samplePinnedSkill("b", "v1")
			left.DeclaredVersion, right.DeclaredVersion = tc.left, tc.right
			got, err := PreparePinnedSkills([]PinnedSkillFiles{right, left})
			if !tc.wantShared {
				var invalid *InstallationValidationError
				if !errors.As(err, &invalid) || invalid.Field != "definition.skills" {
					t.Fatalf("different version identities were merged: skills=%+v err=%v", got, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 1 || got[0].Skill.Name != "Search" || got[0].Skill.MarketSkillID != "a" ||
				got[0].Skill.Version == nil || *got[0].Skill.Version != "1.2.3" {
				t.Fatalf("equivalent source identity was not retained: %+v", got)
			}
		})
	}
}

func TestPreparePinnedSkillsPreservesDescriptionAt2048Runes(t *testing.T) {
	description := strings.Repeat("名", 2048)
	input := samplePinnedSkill("a", "v1")
	input.Description = description

	got, err := PreparePinnedSkills([]PinnedSkillFiles{input})
	if err != nil {
		t.Fatalf("prepare 2048-rune description: %v", err)
	}
	if len(got) != 1 || got[0].Skill.Description != description {
		t.Fatalf("prepared description was not preserved in full: count=%d description runes=%d",
			len(got), len(got[0].Skill.Description))
	}
}

func TestPreparePinnedSkillsRejectsDescriptionOver2048Runes(t *testing.T) {
	input := samplePinnedSkill("a", "v1")
	input.Description = strings.Repeat("名", 2049)

	_, err := PreparePinnedSkills([]PinnedSkillFiles{input})
	var invalid *InstallationValidationError
	if !errors.As(err, &invalid) || invalid.Field != "definition.skills.description" || invalid.Reason != "too_long" {
		t.Fatalf("err=%v, want definition.skills.description too_long", err)
	}
}

func TestPreparePinnedSkillsRejectsNULDescription(t *testing.T) {
	input := samplePinnedSkill("a", "v1")
	input.Description = "search\x00skill"

	_, err := PreparePinnedSkills([]PinnedSkillFiles{input})
	var invalid *InstallationValidationError
	if !errors.As(err, &invalid) || invalid.Field != "definition.skills.description" || invalid.Reason != "text_only" {
		t.Fatalf("err=%v, want definition.skills.description text_only", err)
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
		{"invalid yaml", func(s *PinnedSkillFiles) { s.Files["SKILL.md"] = "---\nname: [\n---\nbody" }},
		{"duplicate key", func(s *PinnedSkillFiles) { s.Files["SKILL.md"] = "---\nname: x\nname: y\n---\nbody" }},
		{"anchor", func(s *PinnedSkillFiles) { s.Files["SKILL.md"] = "---\nname: &a x\ncopy: *a\n---\nbody" }},
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

// Pinned preparation must carry the same content capacity as the live builder:
// supporting-file count and per-text size are unbounded; only the aggregate
// decimal-20 MB budget applies.
func TestPreparePinnedSkillsKeepsFilesAboveLegacyCaps(t *testing.T) {
	input := samplePinnedSkill("a", "v1")
	large := strings.Repeat("b", (1<<20)+512)
	input.Files["refs/big.md"] = large
	for i := range 120 {
		input.Files[fmt.Sprintf("refs/%03d.md", i)] = "ok"
	}
	got, err := PreparePinnedSkills([]PinnedSkillFiles{input})
	if err != nil {
		t.Fatalf("pinned preparation kept the retired caps: %v", err)
	}
	if len(got) != 1 || len(got[0].Skill.Files) != 122 {
		t.Fatalf("prepared files=%d", len(got[0].Skill.Files))
	}
	found := false
	for _, file := range got[0].Skill.Files {
		if file.Path == "refs/big.md" {
			found = file.Source.Content == large
		}
	}
	if !found {
		t.Fatal("large pinned file was dropped or truncated")
	}
}

func TestPreparePinnedSkillsAggregateBudget(t *testing.T) {
	s := samplePinnedSkill("a", "v1")
	for i := range 20 {
		s.Files[fmt.Sprintf("large%d", i)] = strings.Repeat("a", 1<<20)
	}
	if _, err := PreparePinnedSkills([]PinnedSkillFiles{s}); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("aggregate text budget ignored: %v", err)
	}
}
