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
