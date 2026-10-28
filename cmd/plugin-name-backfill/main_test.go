package main

import "testing"

func TestAnalyzeRowsReportsDraftPublishedCollision(t *testing.T) {
	space := "space-a"
	rows := []row{
		{ID: "one", Name: "Beta", PublishedName: "Alpha", Type: "skill", SpaceID: &space},
		{ID: "two", Name: "Beta", Type: "skill", SpaceID: &space},
	}
	out := analyzeRows(rows)
	if len(out.Invalid) != 0 || len(out.DuplicateGroups) != 1 {
		t.Fatalf("report=%+v", out)
	}
	g := out.DuplicateGroups[0]
	if g.Key != "beta" || len(g.IDs) != 2 || g.IDs[0] != "one" || g.IDs[1] != "two" {
		t.Fatalf("group=%+v", g)
	}
}

func TestAnalyzeRowsRejectsBlankDraftDespitePublishedName(t *testing.T) {
	space := "space-a"
	rows := []row{
		{ID: "blank", Name: "   ", PublishedName: "Alpha", Type: "skill", SpaceID: &space},
		{ID: "valid", Name: "Alpha", Type: "skill", SpaceID: &space},
	}
	out := analyzeRows(rows)
	if len(out.Invalid) != 1 || out.Invalid[0] != "blank" {
		t.Fatalf("report=%+v", out)
	}
}

func TestAnalyzeRowsIncludesExpertTeamsAndSkipsEmbedded(t *testing.T) {
	space := "space-a"
	out := analyzeRows([]row{
		{ID: "team-a", Name: "ＢＥＴＡ", PublishedName: "Alpha", Type: "expert_team", SpaceID: &space},
		{ID: "team-b", Name: " beta ", Type: "expert_team", SpaceID: &space},
		{ID: "embedded", Name: "ＢＥＴＡ", Type: "expert_team", SpaceID: &space, IsEmbedded: true},
		{ID: "skill", Name: "beta", Type: "skill", SpaceID: &space},
	})
	if len(out.DuplicateGroups) != 1 || len(out.DuplicateGroups[0].IDs) != 2 {
		t.Fatalf("team duplicates: %+v", out)
	}
}

func TestAnalyzeRowsSkipsInvalidSoftDeletedNames(t *testing.T) {
	deleted := "2026-10-28T00:00:00Z"
	for _, kind := range []string{"skill", "expert", "expert_team"} {
		rows := []row{{ID: "deleted-blank", Type: kind, Name: " ", PublishedName: " ", DeletedAt: &deleted},
			{ID: "deleted-invalid", Type: kind, Name: string(make([]byte, 200)), DeletedAt: &deleted},
			{ID: "deleted-duplicate", Type: kind, Name: "Alpha", DeletedAt: &deleted},
			{ID: "active", Type: kind, Name: "Alpha"}}
		out := analyzeRows(rows)
		if len(out.Invalid) != 0 || len(out.DuplicateGroups) != 0 || out.SkippedDeleted != 3 || rows[3].Key != "alpha" {
			t.Fatalf("%s: %+v", kind, out)
		}
		for _, r := range rows[:3] {
			if r.Key != "" || r.PublishedKey != "" {
				t.Fatal("deleted row was prepared for backfill")
			}
		}
	}
}
