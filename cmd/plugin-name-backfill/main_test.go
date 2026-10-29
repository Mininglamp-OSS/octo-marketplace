package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

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

func TestRunBackfillReportsOnlyCommittedApply(t *testing.T) {
	for _, mode := range []string{"invalid", "duplicate", "preview", "commit", "commit-failed"} {
		t.Run(mode, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			rows := sqlmock.NewRows([]string{"plugin_id", "name", "type", "embedded", "space", "deleted", "published_name", "visibility", "listing", "status"})
			switch mode {
			case "invalid":
				rows.AddRow("one", " ", "skill", false, "space-a", nil, "", "private", "draft", 1)
			case "duplicate":
				rows.AddRow("one", "Alpha", "skill", false, "space-a", nil, "", "private", "draft", 1).AddRow("two", "Ａlpha", "skill", false, "space-a", nil, "", "private", "draft", 1)
			}
			mock.ExpectQuery("SELECT p.plugin_id,p.plugin_name").WillReturnRows(rows)
			if mode == "commit" || mode == "commit-failed" {
				mock.ExpectBegin()
				mock.ExpectQuery("ORDER BY p.plugin_id FOR UPDATE").WillReturnRows(sqlmock.NewRows([]string{"plugin_id", "name", "type", "embedded", "space", "deleted", "published_name", "visibility", "listing", "status"}))
				commit := mock.ExpectCommit()
				if mode == "commit-failed" {
					commit.WillReturnError(errors.New("commit failed"))
				}
			}
			var output bytes.Buffer
			err = runBackfill(context.Background(), db, mode != "preview", &output)
			failed := mode == "invalid" || mode == "duplicate" || mode == "commit-failed"
			if (err != nil) != failed {
				t.Fatalf("error=%v output=%s", err, output.String())
			}
			if mode == "commit-failed" {
				if output.Len() != 0 {
					t.Fatalf("reported an uncommitted apply: %s", output.String())
				}
			} else {
				var got report
				if err := json.Unmarshal(output.Bytes(), &got); err != nil {
					t.Fatal(err)
				}
				if got.Applied != (mode == "commit") {
					t.Fatalf("report=%+v", got)
				}
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestAnalyzeRowsReservesReleasedNameAlongsideRenamedDraft(t *testing.T) {
	space := "space-a"
	rows := []row{
		{ID: "renamed", Name: "Draft", PublishedName: "Official", Type: "skill", SpaceID: &space},
		{ID: "new", Name: "ＯＦＦＩＣＩＡＬ", Type: "skill", SpaceID: &space},
	}
	out := analyzeRows(rows)
	if len(out.Invalid) != 0 || len(out.DuplicateGroups) != 1 || out.DuplicateGroups[0].Key != "official" || rows[0].Key != "draft" || rows[0].PublishedKey != "official" {
		t.Fatalf("released reservation missing: %+v rows=%+v", out, rows)
	}
}

func TestAnalyzeRowsGlobalPublishedReservations(t *testing.T) {
	a, b := "space-a", "space-b"
	for _, kind := range []string{"skill", "expert", "expert_team"} {
		for _, tc := range []struct {
			name, visibility, listing, otherVisibility, otherListing, otherPublished string
			conflict                                                                 bool
		}{
			{"public versus public", "public", "published", "public", "published", "Official", true},
			{"system versus system", "system", "published", "system", "published", "ＯＦＦＩＣＩＡＬ", true},
			{"existing private draft", "public", "published", "private", "draft", "", false},
			{"existing private release", "system", "published", "private", "published", "Official", false},
			{"existing unreleased public draft", "public", "published", "public", "draft", "", false},
			{"public draft name matches but release differs", "public", "published", "public", "published", "Different", false},
			{"foreign private market row", "private", "published", "public", "published", "Official", false},
			{"global draft market row", "system", "draft", "public", "published", "Official", false},
			{"delisted global", "public", "delisted", "public", "published", "Official", false},
		} {
			t.Run(kind+"/"+tc.name, func(t *testing.T) {
				rows := []row{
					{ID: "market", Name: "Draft", PublishedName: "Official", Type: kind, SpaceID: &a, Visibility: tc.visibility, ListingState: tc.listing, Status: 1},
					{ID: "tenant", Name: "ＯＦＦＩＣＩＡＬ", PublishedName: tc.otherPublished, Type: kind, SpaceID: &b, Visibility: tc.otherVisibility, ListingState: tc.otherListing, Status: 1},
				}
				out := analyzeRows(rows)
				if len(out.Invalid) != 0 || (len(out.DuplicateGroups) > 0) != tc.conflict {
					t.Fatalf("report=%+v", out)
				}
				if tc.conflict && (out.DuplicateGroups[0].Scope != "global-market" || out.DuplicateGroups[0].Key != "official") {
					t.Fatalf("wrong global group: %+v", out)
				}
			})
		}
	}
}

func TestRunBackfillRevalidatesLockedInventory(t *testing.T) {
	for _, change := range []string{"embedded", "visibility", "new-global", "unchanged"} {
		t.Run(change, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			columns := []string{"plugin_id", "name", "type", "embedded", "space", "deleted", "published_name", "visibility", "listing", "status"}
			initialVisibility, listing := "private", "draft"
			if change == "new-global" {
				initialVisibility, listing = "public", "published"
			}
			mock.ExpectQuery("SELECT p.plugin_id,p.plugin_name").WillReturnRows(sqlmock.NewRows(columns).AddRow("one", "Alpha", "skill", false, "space-a", nil, "", initialVisibility, listing, 1))
			mock.ExpectBegin()
			visibility := initialVisibility
			if change == "visibility" {
				visibility = "public"
			}
			locked := sqlmock.NewRows(columns).AddRow("one", "Alpha", "skill", change == "embedded", "space-a", nil, "", visibility, listing, 1)
			if change == "new-global" {
				locked.AddRow("market", "Draft", "skill", false, nil, nil, "Alpha", "system", "published", 1)
			}
			mock.ExpectQuery("ORDER BY p.plugin_id FOR UPDATE").WillReturnRows(locked)
			if change == "unchanged" {
				mock.ExpectExec("UPDATE plugins SET name_key").WithArgs("alpha", nil, "space-a", "one").WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectCommit()
			} else {
				mock.ExpectRollback()
			}
			var output bytes.Buffer
			err = runBackfill(context.Background(), db, true, &output)
			if (err == nil) != (change == "unchanged") {
				t.Fatalf("change=%s err=%v report=%s", change, err, output.String())
			}
			if change == "unchanged" || change == "new-global" {
				var out report
				if err := json.Unmarshal(output.Bytes(), &out); err != nil {
					t.Fatal(err)
				}
				if out.Applied != (change == "unchanged") {
					t.Fatalf("incorrect applied status: %+v", out)
				}
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
