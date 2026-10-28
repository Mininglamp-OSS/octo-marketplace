package migration

import (
	"context"
	"testing"

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
