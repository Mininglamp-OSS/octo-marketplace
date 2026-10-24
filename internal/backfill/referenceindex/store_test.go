package referenceindex

import (
	"context"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func evidence(mock sqlmock.Sqlmock, d Dataset, locked bool) {
	suffix := " LIMIT \\?"
	if locked {
		suffix += " FOR SHARE"
	}
	resources := sqlmock.NewRows([]string{"plugin_id", "plugin_type", "is_embedded"})
	for _, r := range d.Resources {
		resources.AddRow(r.ID, r.Type, r.Embedded)
	}
	mock.ExpectQuery("SELECT plugin_id,plugin_type,is_embedded FROM plugins ORDER BY plugin_id" + suffix).WithArgs(101).WillReturnRows(resources)
	snapshots := sqlmock.NewRows([]string{"plugin_id", "version_id", "relations_json"})
	for _, s := range d.Snapshots {
		snapshots.AddRow(s.PluginID, s.VersionID, []byte(s.Relations))
	}
	mock.ExpectQuery("SELECT plugin_id,version_id,relations_json FROM plugin_versions ORDER BY version_id" + suffix).WithArgs(101).WillReturnRows(snapshots)
	releases := sqlmock.NewRows([]string{"plugin_id", "version_id"})
	for _, r := range d.Releases {
		releases.AddRow(r.PluginID, r.VersionID)
	}
	mock.ExpectQuery("SELECT plugin_id,version_id FROM plugin_version_releases ORDER BY version_id" + suffix).WithArgs(101).WillReturnRows(releases)
}

func TestReadAlwaysRollsBackAndRedactsErrors(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectBegin()
	evidence(mock, sample(t), false)
	mock.ExpectRollback()
	d, err := Read(context.Background(), db, 100)
	if err != nil || len(d.Snapshots) != 2 {
		t.Fatalf("read: %v %+v", err, d)
	}
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT").WillReturnError(errors.New("password=secret"))
	mock.ExpectRollback()
	if _, err := Read(context.Background(), db, 100); err != ErrRead {
		t.Fatalf("redaction: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestApplyInsertRepeatConflictAndStalePreview(t *testing.T) {
	for _, mode := range []string{"insert", "repeat", "conflict", "stale", "missing-release"} {
		t.Run(mode, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			d := sample(t)
			digest := Build(d)[0].SnapshotDigest
			if mode == "missing-release" {
				d.Releases = nil
			}
			if mode == "stale" {
				digest = "0000000000000000000000000000000000000000000000000000000000000000"
			}
			mock.ExpectBegin()
			evidence(mock, d, true)
			bad := mode == "conflict" || mode == "stale" || mode == "missing-release"
			if mode != "stale" && mode != "missing-release" {
				rows := sqlmock.NewRows([]string{"source_plugin_id", "relation_id", "relation_type", "target_plugin_id", "target_version_id"})
				if mode == "repeat" {
					rows.AddRow("expert", "edge", "expert_skill", "skill", "skill-v1")
				}
				if mode == "conflict" {
					rows.AddRow("expert", "extra", "expert_skill", "skill", "skill-v1")
				}
				mock.ExpectQuery("SELECT source_plugin_id,relation_id,relation_type,target_plugin_id,target_version_id FROM plugin_version_relation_refs").WithArgs("expert-v1").WillReturnRows(rows)
				if mode == "insert" {
					mock.ExpectExec("INSERT INTO plugin_version_relation_refs").WithArgs("expert", "expert-v1", "edge", "expert_skill", "skill", "skill-v1").WillReturnResult(sqlmock.NewResult(0, 1))
				}
			}
			if bad {
				mock.ExpectRollback()
			} else {
				mock.ExpectCommit()
			}
			n, err := ApplyVersion(context.Background(), db, 100, "expert-v1", digest)
			if bad {
				if err != ErrConflict {
					t.Fatalf("expected conflict: %v", err)
				}
			} else if err != nil || (mode == "insert" && n != 1) || (mode == "repeat" && n != 0) {
				t.Fatalf("apply %d: %v", n, err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
