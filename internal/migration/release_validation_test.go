package migration

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

func TestPersistedReleaseRequiresLabelAndReleaseRow(t *testing.T) {
	for _, tc := range []struct {
		name               string
		declared, released any
		wantError          bool
	}{
		{"valid", "1.0.0", "1.0.0", false},
		{"bad business label", "1", "1", true},
		{"saved but unreleased", "1.0.0", nil, true},
		{"mismatched release", "1.0.0", "2.0.0", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, m, _ := sqlmock.New()
			defer db.Close()
			n := Node{Plugin: model.Plugin{ID: "skill"}, VersionID: "snapshot", VersionLabel: "1.0.0", Release: true}
			m.ExpectQuery("SELECT v.declared_version").WithArgs("snapshot", "skill").WillReturnRows(sqlmock.NewRows([]string{"declared", "release", "current", "current_id", "published_id"}).AddRow(tc.declared, tc.released, "1.0.0", "snapshot", "snapshot"))
			if err := verifyMigrationRelease(context.Background(), db, n); (err != nil) != tc.wantError {
				t.Fatalf("error: %v", err)
			}
			if err := m.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestMigrationReleaseContract(t *testing.T) {
	child := Node{Plugin: model.Plugin{ID: "skill"}, VersionID: "snapshot", VersionLabel: "1.0.0", Release: true}
	parent := Node{Plugin: model.Plugin{ID: "expert"}, VersionID: "parent", VersionLabel: "2.0.0", Release: true, Relations: []model.PluginRelation{{Type: "expert_skill", TargetPluginID: "skill", TargetVersionID: stringPtrApply("snapshot")}}}
	for _, tc := range []struct {
		name      string
		change    func(*Node, *Node)
		wantIssue bool
	}{
		{"valid released pin", func(_, _ *Node) {}, false},
		{"bare declared version", func(c, _ *Node) { c.VersionLabel = "1" }, true},
		{"missing release", func(c, _ *Node) { c.Release = false }, true},
		{"wrong snapshot", func(_, p *Node) { p.Relations[0].TargetVersionID = stringPtrApply("other") }, true},
		{"missing pin", func(_, p *Node) { p.Relations[0].TargetVersionID = nil }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, p := child, parent
			p.Relations = append([]model.PluginRelation(nil), parent.Relations...)
			tc.change(&c, &p)
			issues := validateMigrationReleases(Plan{Nodes: []Node{c, p}})
			if (len(issues) > 0) != tc.wantIssue {
				t.Fatalf("issues: %#v", issues)
			}
		})
	}
}

func TestLegacySnapshotSequenceGetsValidNewBusinessLabel(t *testing.T) {
	r := resource("skill", "skill", "space", "body")
	r.Plugin.CurrentVersion = stringPtrApply("1")
	label := nextLabel(r.Plugin, Input{Versions: []model.PluginVersion{{PluginID: "skill", Version: "1"}}})
	if label != "1.0.0" {
		t.Fatalf("business label: %q", label)
	}
}

func TestApplyRejectsSourcePublicationDriftBeforeWrites(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	release := model.PluginVersionRelease{PluginID: "source", VersionID: "old", VersionLabel: "1.0.0", ReleasedBy: "owner", ReleasedAt: now}
	for _, tc := range []struct {
		name         string
		expected     *string
		captured     *model.PluginVersionRelease
		actual       any
		label, actor string
		at           time.Time
	}{
		{"nil became published", nil, nil, "old", "1.0.0", "owner", now},
		{"published became nil", stringPtrApply("old"), &release, nil, "", "", now},
		{"pointer changed", stringPtrApply("old"), &release, "other", "1.0.0", "owner", now},
		{"release label changed", stringPtrApply("old"), &release, "old", "2.0.0", "owner", now},
		{"release actor changed", stringPtrApply("old"), &release, "old", "1.0.0", "other", now},
		{"release time changed", stringPtrApply("old"), &release, "old", "1.0.0", "owner", now.Add(time.Second)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, m, _ := sqlmock.New()
			defer db.Close()
			expectedRow := map[string][]byte{"plugin_id": []byte("source"), "published_version_id": nil}
			if tc.expected != nil {
				expectedRow["published_version_id"] = []byte(*tc.expected)
			}
			rowHash, err := sourceRowDigest(expectedRow)
			if err != nil {
				t.Fatal(err)
			}
			n := Node{SourceRowHash: rowHash, SourceID: "source", SourcePublishedVersionID: tc.expected, SourceRelease: tc.captured, Source: model.Plugin{}, Plugin: model.Plugin{ID: "target"}}
			m.ExpectBegin()
			m.ExpectQuery("SELECT node_hash FROM marketplace_conversion_receipt").WithArgs("run", "target").WillReturnError(sql.ErrNoRows)
			m.ExpectQuery("SELECT \\* FROM plugin_category_placements ORDER BY placement_id FOR UPDATE").WillReturnRows(sqlmock.NewRows([]string{"placement_id"}))
			m.ExpectQuery(`SELECT \* FROM plugins WHERE plugin_id=\? FOR UPDATE`).WithArgs("source").WillReturnRows(sqlmock.NewRows([]string{"plugin_id", "published_version_id"}).AddRow("source", tc.actual))
			if tc.actual == "old" && tc.expected != nil && *tc.expected == "old" {
				m.ExpectQuery("SELECT version_label,released_by,released_at FROM plugin_version_releases").WithArgs("source", "old").WillReturnRows(sqlmock.NewRows([]string{"label", "actor", "at"}).AddRow(tc.label, tc.actor, tc.at))
			}
			m.ExpectRollback()
			if err := applyNode(context.Background(), db, Plan{CategorySnapshotCaptured: true, ID: "run"}, n); err == nil {
				t.Fatal("stale publication accepted")
			}
			if err := m.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestPublicationEvidenceBindsApplyAndReceiptIdentity(t *testing.T) {
	base := Node{SourceID: "source"}
	p := Plan{Nodes: []Node{base}}
	changed := base
	changed.SourcePublishedVersionID = stringPtrApply("old")
	q := Plan{Nodes: []Node{changed}}
	if nodeHash(base) == nodeHash(changed) || applyIdentity(p) == applyIdentity(q) {
		t.Fatal("published pointer absent from persistence identity")
	}
	changed.SourceRelease = &model.PluginVersionRelease{PluginID: "source", VersionID: "old", VersionLabel: "1.0.0", ReleasedBy: "owner", ReleasedAt: time.Unix(100, 0)}
	q.Nodes[0] = changed
	beforeNode, beforePlan := nodeHash(changed), applyIdentity(q)
	changed.SourceRelease.ReleasedBy = "other"
	if nodeHash(changed) == beforeNode || applyIdentity(q) == beforePlan {
		t.Fatal("release metadata absent from persistence identity")
	}
}

func TestCompleteSourceEvidenceBindsPlanAndReceipt(t *testing.T) {
	n := Node{SourceID: "source", SourceRowHash: "original"}
	p := Plan{Nodes: []Node{n}}
	beforeNode, beforePlan := nodeHash(n), applyIdentity(p)
	n.SourceRowHash = "changed"
	p.Nodes[0] = n
	if nodeHash(n) == beforeNode || applyIdentity(p) == beforePlan {
		t.Fatal("complete source evidence omitted from identity")
	}
}
