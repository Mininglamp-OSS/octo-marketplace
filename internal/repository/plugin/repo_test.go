package plugin

import (
	"context"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

func TestGetExplicitlyScopesCallerAndSpace(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := New(db)
	query := `SELECT .* FROM plugins p\s+WHERE p.plugin_id=\? AND p.status=1 AND p.deleted_at IS NULL AND \(p.visibility IN \('public','system'\) OR \(p.space_id = \? AND \(\(p.visibility = 'space' AND p.listing_state = 'published'\) OR p.owner_uid = \?\)\)\)`
	mock.ExpectQuery(query).WithArgs("plugin-a", "space-a", "caller-a").WillReturnRows(sqlmock.NewRows(pluginTestColumns()))
	_, err = repo.Get(context.Background(), Scope{CallerUID: "caller-a", SpaceID: "space-a"}, "plugin-a")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get error = %v, want ErrNotFound", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestGetDoesNotRetryWithoutScope(t *testing.T) {
	db, mock, _ := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	defer db.Close()
	mock.ExpectQuery(`SELECT .* WHERE p.plugin_id=\? .*p.space_id = \? .*p.owner_uid = \?`).WithArgs("foreign", "space-a", "caller-a").WillReturnRows(sqlmock.NewRows(pluginTestColumns()))
	_, err := New(db).Get(context.Background(), Scope{CallerUID: "caller-a", SpaceID: "space-a"}, "foreign")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("error = %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestLockPluginCategoryRejectsMissingOrWrongType(t *testing.T) {
	db, mock, _ := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	defer db.Close()
	mock.ExpectBegin()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	category := "category-id"
	mock.ExpectQuery(`SELECT category_id FROM plugin_categories WHERE category_id=\? AND status=1 AND deleted_at IS NULL AND JSON_CONTAINS.*FOR UPDATE`).
		WithArgs(category, model.PluginTypeSkill).
		WillReturnRows(sqlmock.NewRows([]string{"category_id"}))
	if err = lockPluginCategory(context.Background(), tx, &category, model.PluginTypeSkill); !errors.Is(err, ErrInvalidCategory) {
		t.Fatalf("lockPluginCategory error = %v, want ErrInvalidCategory", err)
	}
	_ = tx.Rollback()
}

func TestCreateCommitsCurrentRelationsAndAuditTogether(t *testing.T) {
	db, mock, _ := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	defer db.Close()
	r := New(db)
	now := time.Date(2026, 8, 19, 8, 0, 0, 0, time.UTC)
	r.now = func() time.Time { return now }
	ids := []string{"relation-id", "audit-id"}
	r.id = func() string { x := ids[0]; ids = ids[1:]; return x }
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT p.plugin_type,p.is_embedded FROM plugins p .* FOR UPDATE`).WithArgs("target-id", "space", "caller").WillReturnRows(sqlmock.NewRows([]string{"plugin_type", "is_embedded"}).AddRow(model.PluginTypeSkill, false))
	mock.ExpectExec(`INSERT INTO plugins`).WithArgs("plugin-id", "Name", model.PluginTypeExpert, false, nil, "[]", "pub", "caller", "space", model.PluginVisibilityPrivate, model.PluginListingStateDraft, "Creator", "human", nil, nil, "", 0, "{}", "{}", nil, "sha256:m", "sha256:p", nil, nil, 1, now, now).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(`INSERT INTO plugin_relations`).WithArgs("relation-id", "plugin-id", "target-id", "expert_skill", 0, "{}", 1, "caller", now, now).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(`INSERT INTO plugin_audit_logs`).WithArgs("audit-id", "plugin-id", "create", "caller", "Caller", "request-id", nil, "sha256:p", "{}", "{}", nil, now).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	_, err := r.Create(context.Background(), Scope{CallerUID: "caller", SpaceID: "space"}, Mutation{Plugin: model.Plugin{ID: "plugin-id", Name: "Name", Type: model.PluginTypeExpert, Tags: []byte(`[]`), Publisher: "pub", Visibility: model.PluginVisibilityPrivate, CreatorName: "Creator", CreatedByType: "human", Manifest: []byte(`{}`), Package: []byte(`{}`), ManifestHash: "sha256:m", PluginHash: "sha256:p", Status: 1}, Relations: []model.PluginRelation{{TargetPluginID: "target-id", Type: "expert_skill", Data: []byte(`{}`), Status: 1}}, OperatorID: "caller", OperatorName: "Caller", RequestID: "request-id"})
	if err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestCreateAttachesVisiblePlacementInSameTx locks the market-visibility fix: a
// create carrying a Placement inserts the placement row (default, visible) in
// the same transaction as the plugin, so an admin-created plugin surfaces in the
// tenant market without a publish. No category-registration lock runs — a plain
// visible placement is enough.
func TestCreateAttachesVisiblePlacementInSameTx(t *testing.T) {
	db, mock, _ := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	defer db.Close()
	r := New(db)
	now := time.Date(2026, 8, 26, 8, 0, 0, 0, time.UTC)
	r.now = func() time.Time { return now }
	ids := []string{"placement-id", "audit-id"}
	r.id = func() string { x := ids[0]; ids = ids[1:]; return x }
	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO plugins`).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(`INSERT INTO plugin_placements \(placement_id,placement_code,plugin_id,category_id,visible,sort_order,created_at,updated_at\)`).
		WithArgs("placement-id", "default", "plugin-id", nil, true, 0, now, now).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(`INSERT INTO plugin_audit_logs`).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	_, err := r.Create(context.Background(), Scope{CallerUID: "caller", SpaceID: "space"}, Mutation{
		Plugin:     model.Plugin{ID: "plugin-id", Tags: []byte(`[]`), Manifest: []byte(`{}`), Package: []byte(`{}`)},
		Placements: []model.PluginPlacement{{PlacementCode: "default", Visible: true, SortOrder: 0}},
		OperatorID: "caller",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestCreateSnapshotsVersionWhenFlagged locks the per-save version history: a
// create with SnapshotVersion appends a plugin_versions row with an
// auto-increment history label (first snapshot -> "1") and points
// current_version_id at it, stamping current_version (defaulting to "1.0.0").
func TestCreateSnapshotsVersionWhenFlagged(t *testing.T) {
	db, mock, _ := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	defer db.Close()
	r := New(db)
	now := time.Date(2026, 8, 27, 8, 0, 0, 0, time.UTC)
	r.now = func() time.Time { return now }
	ids := []string{"version-id", "audit-id"}
	r.id = func() string { x := ids[0]; ids = ids[1:]; return x }
	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO plugins`).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM plugin_versions WHERE plugin_id=\?`).
		WithArgs("plugin-id").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectExec(`INSERT INTO plugin_versions`).
		WithArgs("version-id", "plugin-id", "1", "{}", "{}", nil, "sha256:m", "sha256:p", "[]", nil, "caller", now).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(`UPDATE plugins SET current_version_id=\?,current_version=\?,updated_at=\? WHERE plugin_id=\? AND deleted_at IS NULL`).
		WithArgs("version-id", "1.0.0", now, "plugin-id").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO plugin_audit_logs`).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	_, err := r.Create(context.Background(), Scope{CallerUID: "caller", SpaceID: "space"}, Mutation{
		Plugin:          model.Plugin{ID: "plugin-id", Tags: []byte(`[]`), Manifest: []byte(`{}`), Package: []byte(`{}`), ManifestHash: "sha256:m", PluginHash: "sha256:p", Status: 1},
		OperatorID:      "caller",
		SnapshotVersion: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestUpdateSnapshotsIncrementingVersion locks that an edit snapshots the next
// sequential history label off the existing version count (2 prior -> "3") and
// points current_version_id at it, stamping current_version (default "1.0.0").
func TestUpdateSnapshotsIncrementingVersion(t *testing.T) {
	db, mock, _ := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	defer db.Close()
	r := New(db)
	now := time.Date(2026, 8, 27, 9, 0, 0, 0, time.UTC)
	r.now = func() time.Time { return now }
	ids := []string{"version-id", "audit-id"}
	r.id = func() string { x := ids[0]; ids = ids[1:]; return x }
	scope := Scope{CallerUID: "caller", SpaceID: "space"}
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT .* FROM plugins p WHERE p.plugin_id=\? AND p.owner_uid=\? AND p.space_id=\? AND p.status=1 AND p.deleted_at IS NULL FOR UPDATE`).
		WithArgs("plugin-id", scope.CallerUID, scope.SpaceID).WillReturnRows(ownedPluginRow("plugin-id", scope, now))
	mock.ExpectQuery(`SELECT target_plugin_id FROM plugin_relations WHERE source_plugin_id=\? AND deleted_at IS NULL`).
		WithArgs("plugin-id").WillReturnRows(sqlmock.NewRows([]string{"target_plugin_id"}))
	mock.ExpectExec(`UPDATE plugins SET plugin_name=`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT EXISTS\(SELECT 1 FROM plugin_placements WHERE plugin_id=\? AND placement_code=\?`).WillReturnRows(sqlmock.NewRows([]string{"e"}).AddRow(1))
	mock.ExpectExec(`UPDATE plugin_placements SET category_id=\?,updated_at=\? WHERE plugin_id=\?`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT relation_id,target_plugin_id,relation_type,sort_order,relation_json,status FROM plugin_relations`).
		WithArgs("plugin-id").WillReturnRows(sqlmock.NewRows([]string{"relation_id", "target_plugin_id", "relation_type", "sort_order", "relation_json", "status"}))
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM plugin_versions WHERE plugin_id=\?`).
		WithArgs("plugin-id").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(2))
	mock.ExpectExec(`INSERT INTO plugin_versions`).
		WithArgs("version-id", "plugin-id", "3", "{}", "{}", nil, "sha256:m2", "sha256:p2", "[]", nil, "caller", now).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(`UPDATE plugins SET current_version_id=\?,current_version=\?,updated_at=\? WHERE plugin_id=\? AND deleted_at IS NULL`).
		WithArgs("version-id", "1.0.0", now, "plugin-id").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO plugin_audit_logs`).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	_, err := r.Update(context.Background(), scope, Mutation{
		Plugin:          model.Plugin{ID: "plugin-id", Type: model.PluginTypeExpert, Tags: []byte(`[]`), Manifest: []byte(`{}`), Package: []byte(`{}`), ManifestHash: "sha256:m2", PluginHash: "sha256:p2", Status: 1},
		OperatorID:      "caller",
		SnapshotVersion: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestUpdateSkipsSnapshotWhenContentUnchanged locks the anti-bloat guard: a
// SnapshotVersion update whose manifest/plugin hashes both match the row under
// lock writes NO plugin_versions row (and no current_version pointer bump), so a
// client cannot append unbounded identical version rows by resubmitting the same
// body. The plugins UPDATE + audit still run.
func TestUpdateSkipsSnapshotWhenContentUnchanged(t *testing.T) {
	db, mock, _ := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	defer db.Close()
	r := New(db)
	now := time.Date(2026, 8, 27, 9, 0, 0, 0, time.UTC)
	r.now = func() time.Time { return now }
	r.id = func() string { return "audit-id" }
	scope := Scope{CallerUID: "caller", SpaceID: "space"}
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT .* FROM plugins p WHERE p.plugin_id=\? AND p.owner_uid=\? AND p.space_id=\? AND p.status=1 AND p.deleted_at IS NULL FOR UPDATE`).
		WithArgs("plugin-id", scope.CallerUID, scope.SpaceID).WillReturnRows(ownedPluginRow("plugin-id", scope, now))
	mock.ExpectQuery(`SELECT target_plugin_id FROM plugin_relations WHERE source_plugin_id=\? AND deleted_at IS NULL`).
		WithArgs("plugin-id").WillReturnRows(sqlmock.NewRows([]string{"target_plugin_id"}))
	mock.ExpectExec(`UPDATE plugins SET plugin_name=`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT EXISTS\(SELECT 1 FROM plugin_placements WHERE plugin_id=\? AND placement_code=\?`).WillReturnRows(sqlmock.NewRows([]string{"e"}).AddRow(1))
	mock.ExpectExec(`UPDATE plugin_placements SET category_id=\?,updated_at=\? WHERE plugin_id=\?`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT relation_id,target_plugin_id,relation_type,sort_order,relation_json,status FROM plugin_relations`).
		WithArgs("plugin-id").WillReturnRows(sqlmock.NewRows([]string{"relation_id", "target_plugin_id", "relation_type", "sort_order", "relation_json", "status"}))
	// No COUNT / INSERT plugin_versions / current_version UPDATE — the content is
	// byte-identical to the locked row, so the snapshot is skipped.
	mock.ExpectExec(`INSERT INTO plugin_audit_logs`).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	sync, err := r.Update(context.Background(), scope, Mutation{
		Plugin:          model.Plugin{ID: "plugin-id", Type: model.PluginTypeExpert, Tags: []byte(`[]`), Manifest: []byte(`{}`), Package: []byte(`{}`), ManifestHash: "sha256:m", PluginHash: "sha256:p", Status: 1},
		OperatorID:      "caller",
		SnapshotVersion: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if sync.NewVersionID != "" {
		t.Fatalf("no-op update recorded a new version id %q — content was unchanged", sync.NewVersionID)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestUpdateSnapshotsOnLabelOnlyChange locks that a version-label-only change
// (identical content, a new current_version) is NOT deduped: it must snapshot so
// the new label is persisted (current_version is written only inside
// snapshotVersion).
func TestUpdateSnapshotsOnLabelOnlyChange(t *testing.T) {
	db, mock, _ := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	defer db.Close()
	r := New(db)
	now := time.Date(2026, 8, 27, 9, 0, 0, 0, time.UTC)
	r.now = func() time.Time { return now }
	ids := []string{"version-id", "audit-id"}
	r.id = func() string { x := ids[0]; ids = ids[1:]; return x }
	scope := Scope{CallerUID: "caller", SpaceID: "space"}
	mock.ExpectBegin()
	// The locked row carries current_version "1.0.0" (ownedPluginRow leaves it
	// NULL, so override via a custom row is unnecessary — the mutation submits a
	// non-nil label, which differs from the NULL stored one).
	mock.ExpectQuery(`SELECT .* FROM plugins p WHERE p.plugin_id=\? AND p.owner_uid=\? AND p.space_id=\? AND p.status=1 AND p.deleted_at IS NULL FOR UPDATE`).
		WithArgs("plugin-id", scope.CallerUID, scope.SpaceID).WillReturnRows(ownedPluginRow("plugin-id", scope, now))
	mock.ExpectQuery(`SELECT target_plugin_id FROM plugin_relations WHERE source_plugin_id=\? AND deleted_at IS NULL`).
		WithArgs("plugin-id").WillReturnRows(sqlmock.NewRows([]string{"target_plugin_id"}))
	mock.ExpectExec(`UPDATE plugins SET plugin_name=`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT EXISTS\(SELECT 1 FROM plugin_placements WHERE plugin_id=\? AND placement_code=\?`).WillReturnRows(sqlmock.NewRows([]string{"e"}).AddRow(1))
	mock.ExpectExec(`UPDATE plugin_placements SET category_id=\?,updated_at=\? WHERE plugin_id=\?`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT relation_id,target_plugin_id,relation_type,sort_order,relation_json,status FROM plugin_relations`).
		WithArgs("plugin-id").WillReturnRows(sqlmock.NewRows([]string{"relation_id", "target_plugin_id", "relation_type", "sort_order", "relation_json", "status"}))
	// Same content hashes as the locked row, but a new label ⇒ snapshot fires.
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM plugin_versions WHERE plugin_id=\?`).
		WithArgs("plugin-id").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectExec(`INSERT INTO plugin_versions`).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(`UPDATE plugins SET current_version_id=\?,current_version=\?,updated_at=\? WHERE plugin_id=\? AND deleted_at IS NULL`).
		WithArgs("version-id", "2.0.0", now, "plugin-id").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO plugin_audit_logs`).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	label := "2.0.0"
	_, err := r.Update(context.Background(), scope, Mutation{
		Plugin:          model.Plugin{ID: "plugin-id", Type: model.PluginTypeExpert, Tags: []byte(`[]`), Manifest: []byte(`{}`), Package: []byte(`{}`), ManifestHash: "sha256:m", PluginHash: "sha256:p", CurrentVersion: &label, Status: 1},
		OperatorID:      "caller",
		SnapshotVersion: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestUpdateSnapshotsOnChangelogOnly locks that a changelog-only save (identical
// content and label) still snapshots — the changelog lives only in the version
// row, so deduping it would silently discard the submitted note.
func TestUpdateSnapshotsOnChangelogOnly(t *testing.T) {
	db, mock, _ := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	defer db.Close()
	r := New(db)
	now := time.Date(2026, 8, 27, 9, 0, 0, 0, time.UTC)
	r.now = func() time.Time { return now }
	ids := []string{"version-id", "audit-id"}
	r.id = func() string { x := ids[0]; ids = ids[1:]; return x }
	scope := Scope{CallerUID: "caller", SpaceID: "space"}
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT .* FROM plugins p WHERE p.plugin_id=\? AND p.owner_uid=\? AND p.space_id=\? AND p.status=1 AND p.deleted_at IS NULL FOR UPDATE`).
		WithArgs("plugin-id", scope.CallerUID, scope.SpaceID).WillReturnRows(ownedPluginRow("plugin-id", scope, now))
	mock.ExpectQuery(`SELECT target_plugin_id FROM plugin_relations WHERE source_plugin_id=\? AND deleted_at IS NULL`).
		WithArgs("plugin-id").WillReturnRows(sqlmock.NewRows([]string{"target_plugin_id"}))
	mock.ExpectExec(`UPDATE plugins SET plugin_name=`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT EXISTS\(SELECT 1 FROM plugin_placements WHERE plugin_id=\? AND placement_code=\?`).WillReturnRows(sqlmock.NewRows([]string{"e"}).AddRow(1))
	mock.ExpectExec(`UPDATE plugin_placements SET category_id=\?,updated_at=\? WHERE plugin_id=\?`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT relation_id,target_plugin_id,relation_type,sort_order,relation_json,status FROM plugin_relations`).
		WithArgs("plugin-id").WillReturnRows(sqlmock.NewRows([]string{"relation_id", "target_plugin_id", "relation_type", "sort_order", "relation_json", "status"}))
	// Identical content and label, but a changelog is present ⇒ snapshot fires.
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM plugin_versions WHERE plugin_id=\?`).
		WithArgs("plugin-id").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectExec(`INSERT INTO plugin_versions`).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(`UPDATE plugins SET current_version_id=\?,current_version=\?,updated_at=\? WHERE plugin_id=\? AND deleted_at IS NULL`).
		WithArgs("version-id", "1.0.0", now, "plugin-id").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO plugin_audit_logs`).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	changelog := "fixed a typo"
	_, err := r.Update(context.Background(), scope, Mutation{
		Plugin:          model.Plugin{ID: "plugin-id", Type: model.PluginTypeExpert, Tags: []byte(`[]`), Manifest: []byte(`{}`), Package: []byte(`{}`), ManifestHash: "sha256:m", PluginHash: "sha256:p", Status: 1},
		OperatorID:      "caller",
		Changelog:       &changelog,
		SnapshotVersion: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestUpdateSnapshotsOnRelationOnly locks that a relation-only save (identical
// content and label, but a new edge) snapshots — relations_json lives in the
// version row, so deduping it would lose the graph transition from history.
func TestUpdateSnapshotsOnRelationOnly(t *testing.T) {
	db, mock, _ := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	defer db.Close()
	r := New(db)
	now := time.Date(2026, 8, 27, 9, 0, 0, 0, time.UTC)
	r.now = func() time.Time { return now }
	ids := []string{"relation-new", "version-id", "audit-id"}
	r.id = func() string { x := ids[0]; ids = ids[1:]; return x }
	scope := Scope{CallerUID: "caller", SpaceID: "space"}
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT .* FROM plugins p WHERE p.plugin_id=\? AND p.owner_uid=\? AND p.space_id=\? AND p.status=1 AND p.deleted_at IS NULL FOR UPDATE`).
		WithArgs("plugin-id", scope.CallerUID, scope.SpaceID).WillReturnRows(ownedPluginRow("plugin-id", scope, now))
	mock.ExpectQuery(`SELECT target_plugin_id FROM plugin_relations WHERE source_plugin_id=\? AND deleted_at IS NULL`).
		WithArgs("plugin-id").WillReturnRows(sqlmock.NewRows([]string{"target_plugin_id"}))
	mock.ExpectQuery(`SELECT p.plugin_type,p.is_embedded FROM plugins p .* FOR UPDATE`).
		WithArgs("target-1", "space", "caller").WillReturnRows(sqlmock.NewRows([]string{"plugin_type", "is_embedded"}).AddRow(model.PluginTypeSkill, false))
	mock.ExpectExec(`UPDATE plugins SET plugin_name=`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT EXISTS\(SELECT 1 FROM plugin_placements WHERE plugin_id=\? AND placement_code=\?`).WillReturnRows(sqlmock.NewRows([]string{"e"}).AddRow(1))
	mock.ExpectExec(`UPDATE plugin_placements SET category_id=\?,updated_at=\? WHERE plugin_id=\?`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT relation_id,target_plugin_id,relation_type,sort_order,relation_json,status FROM plugin_relations`).
		WithArgs("plugin-id").WillReturnRows(sqlmock.NewRows([]string{"relation_id", "target_plugin_id", "relation_type", "sort_order", "relation_json", "status"}))
	mock.ExpectExec(`INSERT INTO plugin_relations`).
		WithArgs("relation-new", "plugin-id", "target-1", "expert_skill", 0, nil, 1, "caller", now, now).WillReturnResult(sqlmock.NewResult(1, 1))
	// Identical content and label, but the relation graph changed ⇒ snapshot fires.
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM plugin_versions WHERE plugin_id=\?`).
		WithArgs("plugin-id").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectExec(`INSERT INTO plugin_versions`).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(`UPDATE plugins SET current_version_id=\?,current_version=\?,updated_at=\? WHERE plugin_id=\? AND deleted_at IS NULL`).
		WithArgs("version-id", "1.0.0", now, "plugin-id").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO plugin_audit_logs`).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	_, err := r.Update(context.Background(), scope, Mutation{
		Plugin: model.Plugin{ID: "plugin-id", Type: model.PluginTypeExpert, Tags: []byte(`[]`), Manifest: []byte(`{}`), Package: []byte(`{}`), ManifestHash: "sha256:m", PluginHash: "sha256:p", Status: 1},
		Relations: []model.PluginRelation{
			{TargetPluginID: "target-1", Type: "expert_skill", Status: 1},
		},
		OperatorID:      "caller",
		SnapshotVersion: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestUpdateSnapshotsOnSidecarOnly locks that an attachment-sidecar-only save
// (identical content hashes and label, but a different path→object-key map)
// snapshots — the managed keys are stripped before hashing, so deduping on hashes
// alone would silently move the live row's keys away from the snapshot that
// current_version_id points at.
func TestUpdateSnapshotsOnSidecarOnly(t *testing.T) {
	db, mock, _ := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	defer db.Close()
	r := New(db)
	now := time.Date(2026, 8, 27, 9, 0, 0, 0, time.UTC)
	r.now = func() time.Time { return now }
	ids := []string{"version-id", "audit-id"}
	r.id = func() string { x := ids[0]; ids = ids[1:]; return x }
	scope := Scope{CallerUID: "caller", SpaceID: "space"}
	mock.ExpectBegin()
	// ownedPluginRow carries a NULL sidecar; the mutation submits a non-empty one.
	mock.ExpectQuery(`SELECT .* FROM plugins p WHERE p.plugin_id=\? AND p.owner_uid=\? AND p.space_id=\? AND p.status=1 AND p.deleted_at IS NULL FOR UPDATE`).
		WithArgs("plugin-id", scope.CallerUID, scope.SpaceID).WillReturnRows(ownedPluginRow("plugin-id", scope, now))
	mock.ExpectQuery(`SELECT target_plugin_id FROM plugin_relations WHERE source_plugin_id=\? AND deleted_at IS NULL`).
		WithArgs("plugin-id").WillReturnRows(sqlmock.NewRows([]string{"target_plugin_id"}))
	mock.ExpectExec(`UPDATE plugins SET plugin_name=`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT EXISTS\(SELECT 1 FROM plugin_placements WHERE plugin_id=\? AND placement_code=\?`).WillReturnRows(sqlmock.NewRows([]string{"e"}).AddRow(1))
	mock.ExpectExec(`UPDATE plugin_placements SET category_id=\?,updated_at=\? WHERE plugin_id=\?`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT relation_id,target_plugin_id,relation_type,sort_order,relation_json,status FROM plugin_relations`).
		WithArgs("plugin-id").WillReturnRows(sqlmock.NewRows([]string{"relation_id", "target_plugin_id", "relation_type", "sort_order", "relation_json", "status"}))
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM plugin_versions WHERE plugin_id=\?`).
		WithArgs("plugin-id").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectExec(`INSERT INTO plugin_versions`).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(`UPDATE plugins SET current_version_id=\?,current_version=\?,updated_at=\? WHERE plugin_id=\? AND deleted_at IS NULL`).
		WithArgs("version-id", "1.0.0", now, "plugin-id").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO plugin_audit_logs`).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	_, err := r.Update(context.Background(), scope, Mutation{
		Plugin:          model.Plugin{ID: "plugin-id", Type: model.PluginTypeExpert, Tags: []byte(`[]`), Manifest: []byte(`{}`), Package: []byte(`{}`), AttachmentKeys: []byte(`{"assets/logo.bin":"plugins/space/attachments/logo-abc.bin"}`), ManifestHash: "sha256:m", PluginHash: "sha256:p", Status: 1},
		OperatorID:      "caller",
		SnapshotVersion: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestCreateSkipsSnapshotWhenNotFlagged locks that a create with SnapshotVersion
// unset writes no plugin_versions row.
func TestCreateSkipsSnapshotWhenNotFlagged(t *testing.T) {
	db, mock, _ := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	defer db.Close()
	r := New(db)
	now := time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC)
	r.now = func() time.Time { return now }
	r.id = func() string { return "audit-id" }
	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO plugins`).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(`INSERT INTO plugin_audit_logs`).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	_, err := r.Create(context.Background(), Scope{CallerUID: "caller", SpaceID: "space"}, Mutation{
		Plugin:     model.Plugin{ID: "plugin-id", Tags: []byte(`[]`), Manifest: []byte(`{}`), Package: []byte(`{}`)},
		OperatorID: "caller",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestLockRelationTargetsRequiresActiveTarget(t *testing.T) {
	db, mock, _ := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	defer db.Close()
	mock.ExpectBegin()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	scope := Scope{CallerUID: "caller", SpaceID: "space"}
	mock.ExpectQuery(`SELECT p.plugin_type,p.is_embedded FROM plugins p WHERE p.plugin_id=\? AND p.status=1 AND p.deleted_at IS NULL.*FOR UPDATE`).
		WithArgs("inactive", scope.SpaceID, scope.CallerUID).WillReturnRows(sqlmock.NewRows([]string{"plugin_type", "is_embedded"}))
	err = lockRelationTargets(context.Background(), tx, scope, model.PluginTypeExpert, []model.PluginRelation{{TargetPluginID: "inactive", Type: "expert_skill"}}, nil)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("lockRelationTargets error = %v, want ErrNotFound", err)
	}
	_ = tx.Rollback()
}

func TestLockRelationTargetsValidatesRowType(t *testing.T) {
	db, mock, _ := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	defer db.Close()
	mock.ExpectBegin()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	scope := Scope{CallerUID: "caller", SpaceID: "space"}
	mock.ExpectQuery(`SELECT p.plugin_type,p.is_embedded FROM plugins p WHERE p.plugin_id=\? AND p.status=1 AND p.deleted_at IS NULL.*FOR UPDATE`).
		WithArgs("opaque-target", scope.SpaceID, scope.CallerUID).
		WillReturnRows(sqlmock.NewRows([]string{"plugin_type", "is_embedded"}).AddRow(model.PluginTypeConnector, false))
	err = lockRelationTargets(context.Background(), tx, scope, model.PluginTypeExpert, []model.PluginRelation{{TargetPluginID: "opaque-target", Type: "expert_skill"}}, nil)
	if !errors.Is(err, ErrInvalidRelation) {
		t.Fatalf("lockRelationTargets error = %v, want ErrInvalidRelation", err)
	}
	_ = tx.Rollback()
}

func TestCreateRejectsInvisibleRelationTargetBeforeWriting(t *testing.T) {
	db, mock, _ := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	defer db.Close()
	scope := Scope{CallerUID: "caller", SpaceID: "space"}
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT p.plugin_type,p.is_embedded FROM plugins p .*p.space_id = \?.*p.owner_uid = \?.*FOR UPDATE`).WithArgs("foreign", scope.SpaceID, scope.CallerUID).WillReturnRows(sqlmock.NewRows([]string{"plugin_type", "is_embedded"}))
	mock.ExpectRollback()

	_, err := New(db).Create(context.Background(), scope, Mutation{Plugin: model.Plugin{ID: "plugin-id", Type: model.PluginTypeExpert}, Relations: []model.PluginRelation{{TargetPluginID: "foreign", Type: "expert_skill"}}})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err=%v, want ErrNotFound", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCreateRollsBackWhenAuditAppendFails(t *testing.T) {
	db, mock, _ := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	defer db.Close()
	r := New(db)
	r.id = func() string { return "audit-id" }
	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO plugins`).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(`INSERT INTO plugin_audit_logs`).WillReturnError(errors.New("audit unavailable"))
	mock.ExpectRollback()
	_, err := r.Create(context.Background(), Scope{CallerUID: "caller", SpaceID: "space"}, Mutation{Plugin: model.Plugin{ID: "plugin-id", Tags: []byte(`[]`), Manifest: []byte(`{}`), Package: []byte(`{}`)}, OperatorID: "caller"})
	if err == nil {
		t.Fatal("expected error")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestGetOwnedForUpdateRejectsInactivePlugin(t *testing.T) {
	db, mock, _ := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	defer db.Close()
	mock.ExpectBegin()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	scope := Scope{CallerUID: "caller", SpaceID: "space"}
	mock.ExpectQuery(`SELECT .* FROM plugins p WHERE p.plugin_id=\? AND p.owner_uid=\? AND p.space_id=\? AND p.status=1 AND p.deleted_at IS NULL FOR UPDATE`).
		WithArgs("inactive", scope.CallerUID, scope.SpaceID).
		WillReturnRows(sqlmock.NewRows(pluginTestColumns()))
	if _, err = getOwnedForUpdate(context.Background(), tx, scope, "inactive"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("getOwnedForUpdate error = %v, want ErrNotFound", err)
	}
	_ = tx.Rollback()
}

func TestDeleteRejectsLiveIncomingRelationWithoutMutatingGraph(t *testing.T) {
	db, mock, _ := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	defer db.Close()
	r := New(db)
	scope := Scope{CallerUID: "caller", SpaceID: "space"}
	now := time.Date(2026, 8, 19, 8, 0, 0, 0, time.UTC)

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT .* FROM plugins p WHERE p.plugin_id=\? AND p.owner_uid=\? AND p.space_id=\?.*FOR UPDATE`).
		WithArgs("target", scope.CallerUID, scope.SpaceID).
		WillReturnRows(ownedPluginRow("target", scope, now))
	mock.ExpectQuery(`SELECT r.relation_id FROM plugin_relations r.*JOIN plugins source ON source.plugin_id=r.source_plugin_id.*r.target_plugin_id=\?.*r.deleted_at IS NULL.*source.deleted_at IS NULL AND source.status=1.*FOR UPDATE`).
		WithArgs("target").
		WillReturnRows(sqlmock.NewRows([]string{"relation_id"}).AddRow("incoming"))
	mock.ExpectRollback()

	err := r.Delete(context.Background(), scope, "target", "caller", "Caller", "request", nil)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("Delete error = %v, want ErrConflict", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestDeleteInvalidatesOnlyOutgoingRelationsAfterReferenceCheck(t *testing.T) {
	db, mock, _ := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	defer db.Close()
	r := New(db)
	scope := Scope{CallerUID: "caller", SpaceID: "space"}
	now := time.Date(2026, 8, 19, 8, 0, 0, 0, time.UTC)
	r.now = func() time.Time { return now }
	r.id = func() string { return "audit-id" }

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT .* FROM plugins p WHERE p.plugin_id=\? AND p.owner_uid=\? AND p.space_id=\?.*FOR UPDATE`).
		WithArgs("plugin-id", scope.CallerUID, scope.SpaceID).
		WillReturnRows(ownedPluginRow("plugin-id", scope, now))
	mock.ExpectQuery(`SELECT r.relation_id FROM plugin_relations r.*r.target_plugin_id=\?.*FOR UPDATE`).
		WithArgs("plugin-id").
		WillReturnRows(sqlmock.NewRows([]string{"relation_id"}))
	mock.ExpectExec(`UPDATE plugins SET deleted_at=\?,updated_at=\?.*`).
		WithArgs(now, now, "plugin-id", scope.CallerUID, scope.SpaceID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE plugin_relations SET deleted_at=\?,updated_at=\?.*WHERE source_plugin_id=\? AND deleted_at IS NULL`).
		WithArgs(now, now, "plugin-id").
		WillReturnResult(sqlmock.NewResult(0, 2))
	expectCancelPendingReview(mock, now, "plugin-id", "caller", "Caller")
	mock.ExpectExec(`INSERT INTO plugin_audit_logs`).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	if err := r.Delete(context.Background(), scope, "plugin-id", "caller", "Caller", "request", nil); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestListPlacementCategoriesCarriesVisibilityScope(t *testing.T) {
	db, mock, _ := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	defer db.Close()
	q := regexp.QuoteMeta("AND p.plugin_type=? AND p.status=1 AND p.deleted_at IS NULL AND") + `.*p.space_id = \? .*p.owner_uid = \?.*` +
		regexp.QuoteMeta("WHERE cp.placement_code=? AND cp.plugin_type=? AND cp.visible=1 AND c.status=1 AND c.deleted_at IS NULL")
	mock.ExpectQuery(q).WithArgs(model.PluginTypeExpert, "space-a", "caller-a", "home", model.PluginTypeExpert).WillReturnRows(sqlmock.NewRows([]string{"category_id", "name", "icon_key", "plugin_types_json", "sort_order", "status", "created_at", "updated_at", "plugin_count"}))
	_, err := New(db).ListPlacementCategories(context.Background(), Scope{CallerUID: "caller-a", SpaceID: "space-a"}, "home", model.PluginTypeExpert)
	if err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestUpdateExemptsOwnEmbeddedChildFromAdoptionGuard is the P0-1 regression: a
// container top (is_embedded=0) that already owns an expert_skill edge to an
// is_embedded=1 skill may resubmit that edge on an Update — the embedded-target
// adoption guard exempts a target the source already owns, so a backfilled
// expert stays editable by its owner without orphaning its bundled skill.
func TestUpdateExemptsOwnEmbeddedChildFromAdoptionGuard(t *testing.T) {
	db, mock, _ := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	defer db.Close()
	r := New(db)
	now := time.Date(2026, 8, 27, 8, 0, 0, 0, time.UTC)
	r.now = func() time.Time { return now }
	r.id = func() string { return "audit-id" }
	scope := Scope{CallerUID: "caller", SpaceID: "space"}
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT .* FROM plugins p WHERE p.plugin_id=\? AND p.owner_uid=\? AND p.space_id=\? AND p.status=1 AND p.deleted_at IS NULL FOR UPDATE`).
		WithArgs("expert-1", scope.CallerUID, scope.SpaceID).
		WillReturnRows(sqlmock.NewRows(pluginTestColumns()).AddRow("expert-1", "Expert", model.PluginTypeExpert, 0, nil, []byte(`[]`), "", "caller", "space", model.PluginVisibilitySpace, model.PluginListingStatePublished, "Creator", "human", nil, nil, "", 0, []byte(`{}`), []byte(`{}`), nil, "sha256:m", "sha256:before", nil, nil, 1, now, now, nil))
	// The live-target set the exemption is built from: the top already owns the edge
	// to the embedded skill.
	mock.ExpectQuery(`SELECT target_plugin_id FROM plugin_relations WHERE source_plugin_id=\? AND deleted_at IS NULL`).
		WithArgs("expert-1").WillReturnRows(sqlmock.NewRows([]string{"target_plugin_id"}).AddRow("skill-emb"))
	// The relation target is is_embedded=1 but IS in the owned set → allowed.
	mock.ExpectQuery(`SELECT p.plugin_type,p.is_embedded FROM plugins p .* FOR UPDATE`).
		WithArgs("skill-emb", "space", "caller").
		WillReturnRows(sqlmock.NewRows([]string{"plugin_type", "is_embedded"}).AddRow(model.PluginTypeSkill, true))
	mock.ExpectExec(`UPDATE plugins SET`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT EXISTS\(SELECT 1 FROM plugin_placements WHERE plugin_id=\? AND placement_code=\?`).WillReturnRows(sqlmock.NewRows([]string{"e"}).AddRow(1))
	mock.ExpectExec(`UPDATE plugin_placements SET category_id=\?,updated_at=\? WHERE plugin_id=\?`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT relation_id,target_plugin_id,relation_type,sort_order,relation_json,status FROM plugin_relations\s+WHERE source_plugin_id=\? AND deleted_at IS NULL ORDER BY relation_id FOR UPDATE`).
		WithArgs("expert-1").
		WillReturnRows(sqlmock.NewRows([]string{"relation_id", "target_plugin_id", "relation_type", "sort_order", "relation_json", "status"}).
			AddRow("rel-1", "skill-emb", "expert_skill", 0, nil, 1))
	mock.ExpectExec(`INSERT INTO plugin_audit_logs`).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	_, err := r.Update(context.Background(), scope, Mutation{
		Plugin:    model.Plugin{ID: "expert-1", Name: "Expert", Type: model.PluginTypeExpert, Tags: []byte(`[]`), Visibility: model.PluginVisibilitySpace, Manifest: []byte(`{}`), Package: []byte(`{}`), ManifestHash: "sha256:m", PluginHash: "sha256:after", Status: 1},
		Relations: []model.PluginRelation{{ID: "rel-1", TargetPluginID: "skill-emb", Type: "expert_skill", Status: 1}},
	})
	if err != nil {
		t.Fatalf("Update rejected the source's own embedded child: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestUpdateRejectsAdoptingForeignEmbeddedChild proves the guard still bites: a
// NEW edge to an is_embedded=1 target the source does NOT already own is refused
// with ErrInvalidRelation, so a standalone Update cannot adopt another graph's
// bundled skill.
func TestUpdateRejectsAdoptingForeignEmbeddedChild(t *testing.T) {
	db, mock, _ := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	defer db.Close()
	r := New(db)
	now := time.Date(2026, 8, 27, 8, 0, 0, 0, time.UTC)
	r.now = func() time.Time { return now }
	scope := Scope{CallerUID: "caller", SpaceID: "space"}
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT .* FROM plugins p WHERE p.plugin_id=\? AND p.owner_uid=\? AND p.space_id=\? AND p.status=1 AND p.deleted_at IS NULL FOR UPDATE`).
		WithArgs("expert-1", scope.CallerUID, scope.SpaceID).
		WillReturnRows(sqlmock.NewRows(pluginTestColumns()).AddRow("expert-1", "Expert", model.PluginTypeExpert, 0, nil, []byte(`[]`), "", "caller", "space", model.PluginVisibilitySpace, model.PluginListingStatePublished, "Creator", "human", nil, nil, "", 0, []byte(`{}`), []byte(`{}`), nil, "sha256:m", "sha256:before", nil, nil, 1, now, now, nil))
	// The source owns no live edges, so the foreign embedded target is not exempt.
	mock.ExpectQuery(`SELECT target_plugin_id FROM plugin_relations WHERE source_plugin_id=\? AND deleted_at IS NULL`).
		WithArgs("expert-1").WillReturnRows(sqlmock.NewRows([]string{"target_plugin_id"}))
	mock.ExpectQuery(`SELECT p.plugin_type,p.is_embedded FROM plugins p .* FOR UPDATE`).
		WithArgs("foreign-emb", "space", "caller").
		WillReturnRows(sqlmock.NewRows([]string{"plugin_type", "is_embedded"}).AddRow(model.PluginTypeSkill, true))
	mock.ExpectRollback()

	_, err := r.Update(context.Background(), scope, Mutation{
		Plugin:    model.Plugin{ID: "expert-1", Name: "Expert", Type: model.PluginTypeExpert, Tags: []byte(`[]`), Visibility: model.PluginVisibilitySpace, Manifest: []byte(`{}`), Package: []byte(`{}`), Status: 1},
		Relations: []model.PluginRelation{{TargetPluginID: "foreign-emb", Type: "expert_skill", Status: 1}},
	})
	if !errors.Is(err, ErrInvalidRelation) {
		t.Fatalf("err = %v, want ErrInvalidRelation", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func pluginTestColumns() []string {
	return []string{"plugin_id", "plugin_name", "plugin_type", "is_embedded", "category_id", "tags_json", "publisher", "owner_uid", "space_id", "visibility", "listing_state", "creator_name", "created_by_type", "created_by_bot_uid", "created_by_bot_name", "icon", "tool_count", "manifest_json", "plugin_json", "attachment_keys_json", "manifest_hash", "plugin_hash", "current_version_id", "current_version", "status", "created_at", "updated_at", "deleted_at"}
}

// expectCancelPendingReview scripts the review cascade EVERY plugin teardown
// runs, immediately after that row's outgoing relations are invalidated.
//
// A pending request must not outlive its plugin: once plugins.deleted_at is set,
// every review read filters the row out and every decision path refuses the
// deleted plugin, so nobody — not even the applicant — can ever settle it. The
// statement is scripted here rather than left implicit because these tests assert
// the exact transaction shape; a teardown that skips it fails on the audit insert.
//
// Zero rows affected is the normal case (most plugins have no open request), which
// is why the cascade never treats it as an error.
func expectCancelPendingReview(mock sqlmock.Sqlmock, now time.Time, pluginID, operatorID, operatorName string) {
	mock.ExpectExec(`UPDATE plugin_review_requests SET status='canceled'`).
		WithArgs(operatorID, operatorName, reasonCanceledOnDelete, now, now, pluginID).
		WillReturnResult(sqlmock.NewResult(0, 0))
}

func TestUpdateSynchronizesRelationsToTargetState(t *testing.T) {
	db, mock, _ := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	defer db.Close()
	r := New(db)
	now := time.Date(2026, 8, 19, 8, 0, 0, 0, time.UTC)
	r.now = func() time.Time { return now }
	ids := []string{"relation-new", "audit-id"}
	r.id = func() string { x := ids[0]; ids = ids[1:]; return x }
	scope := Scope{CallerUID: "caller", SpaceID: "space"}
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT .* FROM plugins p WHERE p.plugin_id=\? AND p.owner_uid=\? AND p.space_id=\? AND p.status=1 AND p.deleted_at IS NULL FOR UPDATE`).
		WithArgs("plugin-id", scope.CallerUID, scope.SpaceID).
		WillReturnRows(sqlmock.NewRows(pluginTestColumns()).AddRow("plugin-id", "Plugin", model.PluginTypeExpert, 0, nil, []byte(`[]`), "pub", "caller", "space", model.PluginVisibilityPrivate, model.PluginListingStatePublished, "Creator", "human", nil, nil, "", 0, []byte(`{}`), []byte(`{}`), nil, "sha256:m", "sha256:before", nil, nil, 1, now, now, nil))
	mock.ExpectQuery(`SELECT target_plugin_id FROM plugin_relations WHERE source_plugin_id=\? AND deleted_at IS NULL`).WithArgs("plugin-id").WillReturnRows(sqlmock.NewRows([]string{"target_plugin_id"}).AddRow("target-0").AddRow("target-1"))
	mock.ExpectQuery(`SELECT p.plugin_type,p.is_embedded FROM plugins p .* FOR UPDATE`).WithArgs("target-1", "space", "caller").WillReturnRows(sqlmock.NewRows([]string{"plugin_type", "is_embedded"}).AddRow(model.PluginTypeSkill, false))
	mock.ExpectQuery(`SELECT p.plugin_type,p.is_embedded FROM plugins p .* FOR UPDATE`).WithArgs("target-2", "space", "caller").WillReturnRows(sqlmock.NewRows([]string{"plugin_type", "is_embedded"}).AddRow(model.PluginTypeSkill, false))
	mock.ExpectExec(`UPDATE plugins SET`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT EXISTS\(SELECT 1 FROM plugin_placements WHERE plugin_id=\? AND placement_code=\?`).WillReturnRows(sqlmock.NewRows([]string{"e"}).AddRow(1))
	mock.ExpectExec(`UPDATE plugin_placements SET category_id=\?,updated_at=\? WHERE plugin_id=\?`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT relation_id,target_plugin_id,relation_type,sort_order,relation_json,status FROM plugin_relations\s+WHERE source_plugin_id=\? AND deleted_at IS NULL ORDER BY relation_id FOR UPDATE`).
		WithArgs("plugin-id").
		WillReturnRows(sqlmock.NewRows([]string{"relation_id", "target_plugin_id", "relation_type", "sort_order", "relation_json", "status"}).
			AddRow("rel-0", "target-0", "expert_skill", 0, nil, 1).
			AddRow("rel-1", "target-1", "expert_skill", 0, nil, 1))
	mock.ExpectExec(`UPDATE plugin_relations SET target_plugin_id=`).WithArgs("target-1", "expert_skill", 2, nil, 1, now, "rel-1", "plugin-id").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO plugin_relations`).WithArgs("relation-new", "plugin-id", "target-2", "expert_skill", 0, nil, 1, "caller", now, now).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(`UPDATE plugin_relations SET deleted_at=`).WithArgs(now, now, "rel-0", "plugin-id").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO plugin_audit_logs`).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	sync, err := r.Update(context.Background(), scope, Mutation{
		Plugin: model.Plugin{ID: "plugin-id", Name: "Plugin", Type: model.PluginTypeExpert, Tags: []byte(`[]`), Publisher: "pub", Visibility: model.PluginVisibilityPrivate, Manifest: []byte(`{}`), Package: []byte(`{}`), ManifestHash: "sha256:m", PluginHash: "sha256:after", Status: 1},
		Relations: []model.PluginRelation{
			{ID: "rel-1", TargetPluginID: "target-1", Type: "expert_skill", SortOrder: 2, Status: 1},
			{TargetPluginID: "target-2", Type: "expert_skill", Status: 1},
		},
		OperatorID: "caller",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(sync.Created) != 1 || sync.Created[0] != "relation-new" ||
		len(sync.Updated) != 1 || sync.Updated[0] != "rel-1" ||
		len(sync.Deleted) != 1 || sync.Deleted[0] != "rel-0" {
		t.Fatalf("sync = %#v", sync)
	}
	if len(sync.Relations) != 2 || sync.Relations[1].ID != "relation-new" {
		t.Fatalf("relations = %#v", sync.Relations)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestUpdateRejectsUnknownSubmittedRelationID(t *testing.T) {
	db, mock, _ := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	defer db.Close()
	r := New(db)
	now := time.Date(2026, 8, 19, 8, 0, 0, 0, time.UTC)
	r.now = func() time.Time { return now }
	scope := Scope{CallerUID: "caller", SpaceID: "space"}
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT .* FROM plugins p WHERE p.plugin_id=\? AND p.owner_uid=\? AND p.space_id=\? AND p.status=1 AND p.deleted_at IS NULL FOR UPDATE`).
		WithArgs("plugin-id", scope.CallerUID, scope.SpaceID).
		WillReturnRows(sqlmock.NewRows(pluginTestColumns()).AddRow("plugin-id", "Plugin", model.PluginTypeExpert, 0, nil, []byte(`[]`), "pub", "caller", "space", model.PluginVisibilityPrivate, model.PluginListingStatePublished, "Creator", "human", nil, nil, "", 0, []byte(`{}`), []byte(`{}`), nil, "sha256:m", "sha256:before", nil, nil, 1, now, now, nil))
	mock.ExpectQuery(`SELECT target_plugin_id FROM plugin_relations WHERE source_plugin_id=\? AND deleted_at IS NULL`).WithArgs("plugin-id").WillReturnRows(sqlmock.NewRows([]string{"target_plugin_id"}))
	mock.ExpectQuery(`SELECT p.plugin_type,p.is_embedded FROM plugins p .* FOR UPDATE`).WithArgs("target-1", "space", "caller").WillReturnRows(sqlmock.NewRows([]string{"plugin_type", "is_embedded"}).AddRow(model.PluginTypeSkill, false))
	mock.ExpectExec(`UPDATE plugins SET`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT EXISTS\(SELECT 1 FROM plugin_placements WHERE plugin_id=\? AND placement_code=\?`).WillReturnRows(sqlmock.NewRows([]string{"e"}).AddRow(1))
	mock.ExpectExec(`UPDATE plugin_placements SET category_id=\?,updated_at=\? WHERE plugin_id=\?`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT relation_id,target_plugin_id,relation_type,sort_order,relation_json,status FROM plugin_relations\s+WHERE source_plugin_id=\? AND deleted_at IS NULL ORDER BY relation_id FOR UPDATE`).
		WithArgs("plugin-id").
		WillReturnRows(sqlmock.NewRows([]string{"relation_id", "target_plugin_id", "relation_type", "sort_order", "relation_json", "status"}))
	mock.ExpectRollback()

	_, err := r.Update(context.Background(), scope, Mutation{
		Plugin:    model.Plugin{ID: "plugin-id", Name: "Plugin", Type: model.PluginTypeExpert, Tags: []byte(`[]`), Manifest: []byte(`{}`), Package: []byte(`{}`), Status: 1},
		Relations: []model.PluginRelation{{ID: "rel-forged", TargetPluginID: "target-1", Type: "expert_skill", Status: 1}},
	})
	if !errors.Is(err, ErrInvalidRelation) {
		t.Fatalf("err = %v, want ErrInvalidRelation", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestUpdateSyncsPlacementCategory(t *testing.T) {
	db, mock, _ := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	defer db.Close()
	r := New(db)
	now := time.Date(2026, 8, 22, 8, 0, 0, 0, time.UTC)
	r.now = func() time.Time { return now }
	r.id = func() string { return "audit-id" }
	scope := Scope{CallerUID: "caller", SpaceID: "space"}
	category := "cat-1"

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT .* FROM plugins p WHERE p.plugin_id=\? AND p.owner_uid=\? AND p.space_id=\?.*FOR UPDATE`).
		WithArgs("plugin-id", scope.CallerUID, scope.SpaceID).
		WillReturnRows(ownedPluginRow("plugin-id", scope, now))
	mock.ExpectQuery(`SELECT category_id FROM plugin_categories WHERE category_id=\?.*FOR UPDATE`).
		WithArgs(category, model.PluginTypeExpert).
		WillReturnRows(sqlmock.NewRows([]string{"category_id"}).AddRow(category))
	mock.ExpectQuery(`SELECT target_plugin_id FROM plugin_relations WHERE source_plugin_id=\? AND deleted_at IS NULL`).WithArgs("plugin-id").WillReturnRows(sqlmock.NewRows([]string{"target_plugin_id"}))
	mock.ExpectExec(`UPDATE plugins SET`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT EXISTS\(SELECT 1 FROM plugin_placements WHERE plugin_id=\? AND placement_code=\?`).WillReturnRows(sqlmock.NewRows([]string{"e"}).AddRow(1))
	mock.ExpectExec(`UPDATE plugin_placements SET category_id=\?,updated_at=\? WHERE plugin_id=\?`).
		WithArgs(category, now, "plugin-id").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT relation_id,target_plugin_id,relation_type,sort_order,relation_json,status FROM plugin_relations`).
		WithArgs("plugin-id").
		WillReturnRows(sqlmock.NewRows([]string{"relation_id", "target_plugin_id", "relation_type", "sort_order", "relation_json", "status"}))
	mock.ExpectExec(`INSERT INTO plugin_audit_logs`).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	_, err := r.Update(context.Background(), scope, Mutation{Plugin: model.Plugin{ID: "plugin-id", Name: "Plugin", Type: model.PluginTypeExpert, CategoryID: &category, Tags: []byte(`[]`), Visibility: model.PluginVisibilityPrivate, Manifest: []byte(`{}`), Package: []byte(`{}`), Status: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestUpdateInsertsDefaultPlacementWhenMissing is the placement-orphan
// regression: a save on a plugin with no default placement row (e.g. an old
// tenant create that never published — publish-removal left no other path to
// insert one) inserts the default placement so the plugin becomes market-listable
// again, rather than an update-only that matches nothing and leaves it invisible.
func TestUpdateInsertsDefaultPlacementWhenMissing(t *testing.T) {
	db, mock, _ := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	defer db.Close()
	r := New(db)
	now := time.Date(2026, 8, 22, 8, 0, 0, 0, time.UTC)
	r.now = func() time.Time { return now }
	ids := []string{"placement-id", "audit-id"}
	r.id = func() string { x := ids[0]; ids = ids[1:]; return x }
	scope := Scope{CallerUID: "caller", SpaceID: "space"}
	category := "cat-1"
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT .* FROM plugins p WHERE p.plugin_id=\? AND p.owner_uid=\? AND p.space_id=\?.*FOR UPDATE`).
		WithArgs("plugin-id", scope.CallerUID, scope.SpaceID).WillReturnRows(ownedPluginRow("plugin-id", scope, now))
	mock.ExpectQuery(`SELECT category_id FROM plugin_categories WHERE category_id=\?.*FOR UPDATE`).
		WithArgs(category, model.PluginTypeExpert).WillReturnRows(sqlmock.NewRows([]string{"category_id"}).AddRow(category))
	mock.ExpectQuery(`SELECT target_plugin_id FROM plugin_relations WHERE source_plugin_id=\? AND deleted_at IS NULL`).
		WithArgs("plugin-id").WillReturnRows(sqlmock.NewRows([]string{"target_plugin_id"}))
	mock.ExpectExec(`UPDATE plugins SET`).WillReturnResult(sqlmock.NewResult(0, 1))
	// No default placement exists ⇒ insert it (self-heal), do not update.
	mock.ExpectQuery(`SELECT EXISTS\(SELECT 1 FROM plugin_placements WHERE plugin_id=\? AND placement_code=\?`).
		WillReturnRows(sqlmock.NewRows([]string{"e"}).AddRow(0))
	mock.ExpectExec(`INSERT INTO plugin_placements \(placement_id,placement_code,plugin_id,category_id,visible,sort_order,created_at,updated_at\)`).
		WithArgs("placement-id", "default", "plugin-id", category, true, 0, now, now).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectQuery(`SELECT relation_id,target_plugin_id,relation_type,sort_order,relation_json,status FROM plugin_relations`).
		WithArgs("plugin-id").WillReturnRows(sqlmock.NewRows([]string{"relation_id", "target_plugin_id", "relation_type", "sort_order", "relation_json", "status"}))
	mock.ExpectExec(`INSERT INTO plugin_audit_logs`).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	_, err := r.Update(context.Background(), scope, Mutation{Plugin: model.Plugin{ID: "plugin-id", Name: "Plugin", Type: model.PluginTypeExpert, CategoryID: &category, Tags: []byte(`[]`), Visibility: model.PluginVisibilityPrivate, Manifest: []byte(`{}`), Package: []byte(`{}`), Status: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestUpdateClearsPlacementCategoryWhenCleared locks that an update with no
// category (an explicit clear) NULLs the placement's category too — with publish
// gone, the update owns placement configuration, so the scene-scoped market
// stops filtering the plugin under its old category.
func TestUpdateClearsPlacementCategoryWhenCleared(t *testing.T) {
	db, mock, _ := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	defer db.Close()
	r := New(db)
	now := time.Date(2026, 8, 22, 8, 0, 0, 0, time.UTC)
	r.now = func() time.Time { return now }
	r.id = func() string { return "audit-id" }
	scope := Scope{CallerUID: "caller", SpaceID: "space"}

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT .* FROM plugins p WHERE p.plugin_id=\? AND p.owner_uid=\? AND p.space_id=\?.*FOR UPDATE`).
		WithArgs("plugin-id", scope.CallerUID, scope.SpaceID).
		WillReturnRows(ownedPluginRow("plugin-id", scope, now))
	// nil category: no plugin_categories lock query is issued.
	mock.ExpectQuery(`SELECT target_plugin_id FROM plugin_relations WHERE source_plugin_id=\? AND deleted_at IS NULL`).WithArgs("plugin-id").WillReturnRows(sqlmock.NewRows([]string{"target_plugin_id"}))
	mock.ExpectExec(`UPDATE plugins SET`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT EXISTS\(SELECT 1 FROM plugin_placements WHERE plugin_id=\? AND placement_code=\?`).WillReturnRows(sqlmock.NewRows([]string{"e"}).AddRow(1))
	mock.ExpectExec(`UPDATE plugin_placements SET category_id=\?,updated_at=\? WHERE plugin_id=\?`).
		WithArgs(nil, now, "plugin-id").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT relation_id,target_plugin_id,relation_type,sort_order,relation_json,status FROM plugin_relations`).
		WithArgs("plugin-id").
		WillReturnRows(sqlmock.NewRows([]string{"relation_id", "target_plugin_id", "relation_type", "sort_order", "relation_json", "status"}))
	mock.ExpectExec(`INSERT INTO plugin_audit_logs`).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	_, err := r.Update(context.Background(), scope, Mutation{Plugin: model.Plugin{ID: "plugin-id", Name: "Plugin", Type: model.PluginTypeExpert, CategoryID: nil, Tags: []byte(`[]`), Visibility: model.PluginVisibilityPrivate, Manifest: []byte(`{}`), Package: []byte(`{}`), Status: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestCreateGraphInsertsAllPluginsBeforeRelationsAndCommitsAtomically locks the
// container-import contract: every plugin row is inserted (phase 1) before any
// relation target is locked (phase 2), so an intra-graph edge (the expert's
// expert_skill edge to a skill created in the same transaction) resolves, and
// one create audit is appended per plugin (phase 3) — all in one transaction.
func TestCreateGraphInsertsAllPluginsBeforeRelationsAndCommitsAtomically(t *testing.T) {
	db, mock, _ := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	defer db.Close()
	r := New(db)
	now := time.Date(2026, 8, 26, 8, 0, 0, 0, time.UTC)
	r.now = func() time.Time { return now }
	ids := []string{"rel-1", "audit-1", "audit-2"}
	r.id = func() string { x := ids[0]; ids = ids[1:]; return x }
	space := ""
	skill := model.Plugin{ID: "skill-id", Name: "S", Type: model.PluginTypeSkill, Tags: []byte(`[]`), OwnerUID: "admin", SpaceID: &space, Visibility: model.PluginVisibilityPublic, CreatorName: "Root", CreatedByType: "human", Manifest: []byte(`{}`), Package: []byte(`{}`), ManifestHash: "sha256:sm", PluginHash: "sha256:sp", Status: 1}
	expert := model.Plugin{ID: "expert-id", Name: "E", Type: model.PluginTypeExpert, Tags: []byte(`[]`), OwnerUID: "admin", SpaceID: &space, Visibility: model.PluginVisibilityPublic, CreatorName: "Root", CreatedByType: "human", Manifest: []byte(`{}`), Package: []byte(`{}`), ManifestHash: "sha256:em", PluginHash: "sha256:ep", Status: 1}

	mock.ExpectBegin()
	// Phase 1: both plugin rows inserted first.
	mock.ExpectExec(`INSERT INTO plugins`).WithArgs("skill-id", "S", model.PluginTypeSkill, false, nil, "[]", "", "admin", "", model.PluginVisibilityPublic, model.PluginListingStateDraft, "Root", "human", nil, nil, "", 0, "{}", "{}", nil, "sha256:sm", "sha256:sp", nil, nil, 1, now, now).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(`INSERT INTO plugins`).WithArgs("expert-id", "E", model.PluginTypeExpert, false, nil, "[]", "", "admin", "", model.PluginVisibilityPublic, model.PluginListingStateDraft, "Root", "human", nil, nil, "", 0, "{}", "{}", nil, "sha256:em", "sha256:ep", nil, nil, 1, now, now).WillReturnResult(sqlmock.NewResult(1, 1))
	// Phase 2: the expert's relation target (the just-inserted skill) is locked
	// cross-Space (admin) and the edge inserted.
	mock.ExpectQuery(`SELECT p.plugin_type,p.is_embedded FROM plugins p WHERE p.plugin_id=\? AND p.status=1 AND p.deleted_at IS NULL AND 1=1 FOR UPDATE`).WithArgs("skill-id").WillReturnRows(sqlmock.NewRows([]string{"plugin_type", "is_embedded"}).AddRow(model.PluginTypeSkill, false))
	mock.ExpectExec(`INSERT INTO plugin_relations`).WithArgs("rel-1", "expert-id", "skill-id", "expert_skill", 0, "{}", 1, "admin", now, now).WillReturnResult(sqlmock.NewResult(1, 1))
	// Phase 3: one create audit per plugin.
	mock.ExpectExec(`INSERT INTO plugin_audit_logs`).WithArgs("audit-1", "skill-id", "create", "admin", "Root", "req", nil, "sha256:sp", "{}", "{}", nil, now).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(`INSERT INTO plugin_audit_logs`).WithArgs("audit-2", "expert-id", "create", "admin", "Root", "req", nil, "sha256:ep", "{}", "{}", nil, now).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	nodes := []Mutation{
		{Plugin: skill, OperatorID: "admin", OperatorName: "Root", RequestID: "req"},
		{Plugin: expert, Relations: []model.PluginRelation{{TargetPluginID: "skill-id", Type: "expert_skill", Data: []byte(`{}`), Status: 1}}, OperatorID: "admin", OperatorName: "Root", RequestID: "req"},
	}
	syncs, err := r.CreateGraph(context.Background(), Scope{CallerUID: "admin", Admin: true}, nodes)
	if err != nil {
		t.Fatal(err)
	}
	if len(syncs) != 2 || len(syncs[1].Created) != 1 || syncs[1].Created[0] != "rel-1" {
		t.Fatalf("syncs = %#v", syncs)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestCreateGraphInsertsPlacementForMarkedNode locks the container-import market
// fix: only the node the service marks with a Placement (the top expert/team)
// gets a placement row, inserted in the same transaction after its relations and
// before the audit — the bundled skill node carries none.
func TestCreateGraphInsertsPlacementForMarkedNode(t *testing.T) {
	db, mock, _ := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	defer db.Close()
	r := New(db)
	now := time.Date(2026, 8, 26, 8, 0, 0, 0, time.UTC)
	r.now = func() time.Time { return now }
	ids := []string{"rel-1", "placement-1", "audit-1", "audit-2"}
	r.id = func() string { x := ids[0]; ids = ids[1:]; return x }
	space := ""
	skill := model.Plugin{ID: "skill-id", Name: "S", Type: model.PluginTypeSkill, Tags: []byte(`[]`), OwnerUID: "admin", SpaceID: &space, Visibility: model.PluginVisibilityPublic, CreatorName: "Root", CreatedByType: "human", Manifest: []byte(`{}`), Package: []byte(`{}`), ManifestHash: "sha256:sm", PluginHash: "sha256:sp", Status: 1}
	expert := model.Plugin{ID: "expert-id", Name: "E", Type: model.PluginTypeExpert, Tags: []byte(`[]`), OwnerUID: "admin", SpaceID: &space, Visibility: model.PluginVisibilityPublic, CreatorName: "Root", CreatedByType: "human", Manifest: []byte(`{}`), Package: []byte(`{}`), ManifestHash: "sha256:em", PluginHash: "sha256:ep", Status: 1}

	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO plugins`).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(`INSERT INTO plugins`).WillReturnResult(sqlmock.NewResult(1, 1))
	// expert node: relation target lock, relation insert, then its placement.
	mock.ExpectQuery(`SELECT p.plugin_type,p.is_embedded FROM plugins p WHERE p.plugin_id=\? AND p.status=1 AND p.deleted_at IS NULL AND 1=1 FOR UPDATE`).WithArgs("skill-id").WillReturnRows(sqlmock.NewRows([]string{"plugin_type", "is_embedded"}).AddRow(model.PluginTypeSkill, false))
	mock.ExpectExec(`INSERT INTO plugin_relations`).WithArgs("rel-1", "expert-id", "skill-id", "expert_skill", 0, "{}", 1, "admin", now, now).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(`INSERT INTO plugin_placements \(placement_id,placement_code,plugin_id,category_id,visible,sort_order,created_at,updated_at\)`).
		WithArgs("placement-1", "default", "expert-id", nil, true, 0, now, now).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(`INSERT INTO plugin_audit_logs`).WithArgs("audit-1", "skill-id", "create", "admin", "Root", "req", nil, "sha256:sp", "{}", "{}", nil, now).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(`INSERT INTO plugin_audit_logs`).WithArgs("audit-2", "expert-id", "create", "admin", "Root", "req", nil, "sha256:ep", "{}", "{}", nil, now).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	nodes := []Mutation{
		{Plugin: skill, OperatorID: "admin", OperatorName: "Root", RequestID: "req"},
		{Plugin: expert, Relations: []model.PluginRelation{{TargetPluginID: "skill-id", Type: "expert_skill", Data: []byte(`{}`), Status: 1}}, Placements: []model.PluginPlacement{{PlacementCode: "default", Visible: true, SortOrder: 0}}, OperatorID: "admin", OperatorName: "Root", RequestID: "req"},
	}
	if _, err := r.CreateGraph(context.Background(), Scope{CallerUID: "admin", Admin: true}, nodes); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestCreateGraphRejectsDuplicatePluginIDs guards the pre-assigned-ID contract:
// a graph that reuses a plugin ID is rejected before any relation is written.
func TestCreateGraphRejectsDuplicatePluginIDs(t *testing.T) {
	db, mock, _ := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	defer db.Close()
	r := New(db)
	now := time.Date(2026, 8, 26, 8, 0, 0, 0, time.UTC)
	r.now = func() time.Time { return now }
	space := ""
	p := model.Plugin{ID: "dup", Name: "S", Type: model.PluginTypeSkill, Tags: []byte(`[]`), OwnerUID: "admin", SpaceID: &space, Visibility: model.PluginVisibilityPublic, CreatorName: "Root", CreatedByType: "human", Manifest: []byte(`{}`), Package: []byte(`{}`), ManifestHash: "sha256:m", PluginHash: "sha256:p", Status: 1}
	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO plugins`).WithArgs("dup", "S", model.PluginTypeSkill, false, nil, "[]", "", "admin", "", model.PluginVisibilityPublic, model.PluginListingStateDraft, "Root", "human", nil, nil, "", 0, "{}", "{}", nil, "sha256:m", "sha256:p", nil, nil, 1, now, now).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectRollback()
	if _, err := r.CreateGraph(context.Background(), Scope{CallerUID: "admin", Admin: true}, []Mutation{{Plugin: p}, {Plugin: p}}); !errors.Is(err, ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestRebuildGraphSwapsChildrenPreservesTopAndSoftDeletesOld locks the
// container-reupload contract at the persistence boundary: the top plugin row is
// updated in place (its id survives, never re-inserted), the new embedded child
// is inserted, the top's relations resync to the new child (old edge deleted, new
// edge created), and the previous child plus its outgoing relations are
// soft-deleted — all in one transaction with an update audit for the top, a
// delete audit for the old child, and a create audit for the new child.
func TestRebuildGraphSwapsChildrenPreservesTopAndSoftDeletesOld(t *testing.T) {
	db, mock, _ := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	defer db.Close()
	r := New(db)
	now := time.Date(2026, 8, 26, 8, 0, 0, 0, time.UTC)
	r.now = func() time.Time { return now }
	ids := []string{"rel-new", "audit-del", "audit-top", "audit-child"}
	r.id = func() string { x := ids[0]; ids = ids[1:]; return x }
	scope := Scope{CallerUID: "admin", Admin: true}
	space := ""
	newSkill := model.Plugin{ID: "skill-new", Name: "S2", Type: model.PluginTypeSkill, IsEmbedded: true, Tags: []byte(`[]`), OwnerUID: "admin", SpaceID: &space, Visibility: model.PluginVisibilitySystem, ListingState: model.PluginListingStateDraft, CreatorName: "Root", CreatedByType: "human", Manifest: []byte(`{}`), Package: []byte(`{}`), ManifestHash: "sha256:sm", PluginHash: "sha256:sp", Status: 1}
	top := model.Plugin{ID: "expert-9", Name: "E2", Type: model.PluginTypeExpert, Tags: []byte(`["ops"]`), Visibility: model.PluginVisibilityPublic, Icon: "icons/e.png", Manifest: []byte(`{"m":2}`), Package: []byte(`{"p":2}`), ManifestHash: "sha256:m2", PluginHash: "sha256:p2", Status: 1}

	mock.ExpectBegin()
	// Phase 0: lock + prove the top exists (admin: no owner/space predicate).
	mock.ExpectQuery(`SELECT .* FROM plugins p WHERE p.plugin_id=\? AND p.status=1 AND p.deleted_at IS NULL FOR UPDATE`).
		WithArgs("expert-9").
		WillReturnRows(sqlmock.NewRows(pluginTestColumns()).AddRow("expert-9", "E", model.PluginTypeExpert, 0, nil, []byte(`["ops"]`), "", "owner-1", "", model.PluginVisibilityPublic, model.PluginListingStatePublished, "Orig", "human", nil, nil, "icons/e.png", 0, []byte(`{}`), []byte(`{}`), nil, "sha256:m", "sha256:pold", nil, nil, 1, now, now, nil))
	// In-tx child derivation: AFTER the top FOR UPDATE lock, the previous embedded
	// child set is read from the committed graph (an expert's is_embedded expert_skill
	// targets) — never a caller-supplied stale list. The DB's is_embedded=1 join is
	// what returns skill-old here; a standalone target would be filtered out. This
	// ordering proves the resolution runs under the lock, closing the concurrent-
	// reupload / delete-vs-reupload orphan race.
	mock.ExpectQuery(`SELECT r.target_plugin_id FROM plugin_relations r\s+JOIN plugins p ON p.plugin_id=r.target_plugin_id\s+WHERE r.source_plugin_id=\? AND r.relation_type=\? AND r.deleted_at IS NULL\s+AND p.is_embedded=1 AND p.status=1 AND p.deleted_at IS NULL\s+ORDER BY r.sort_order,r.relation_id`).
		WithArgs("expert-9", "expert_skill").
		WillReturnRows(sqlmock.NewRows([]string{"target_plugin_id"}).AddRow("skill-old"))
	// Phase 1: insert the new embedded child row. Its visibility/listing state/owner
	// are re-stamped from the locked top (`before`: owner-1, published), not the
	// caller's pre-parse System/admin/draft stamping — the P2-1 locked-snapshot
	// guard. A legacy `public` locked visibility is normalized to `system`
	// (NormalizeLegacyVisibility). The listing state matters as much as the rest: a
	// child left `draft` under a published top makes every non-owner install fail
	// with ErrDependencyHidden.
	mock.ExpectExec(`INSERT INTO plugins`).WithArgs("skill-new", "S2", model.PluginTypeSkill, true, nil, "[]", "", "owner-1", "", model.PluginVisibilitySystem, model.PluginListingStatePublished, "Root", "human", nil, nil, "", 0, "{}", "{}", nil, "sha256:sm", "sha256:sp", nil, nil, 1, now, now).WillReturnResult(sqlmock.NewResult(1, 1))
	// Phase 3: update the top row in place (id/owner/space/created_at untouched).
	mock.ExpectExec(`UPDATE plugins SET plugin_name=.*WHERE plugin_id=\? AND deleted_at IS NULL`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT EXISTS\(SELECT 1 FROM plugin_placements WHERE plugin_id=\? AND placement_code=\?`).WillReturnRows(sqlmock.NewRows([]string{"e"}).AddRow(1))
	mock.ExpectExec(`UPDATE plugin_placements SET category_id=\?,updated_at=\? WHERE plugin_id=\?`).WillReturnResult(sqlmock.NewResult(0, 1))
	// Phase 4: resync the top's relations to the new child.
	mock.ExpectQuery(`SELECT p.plugin_type,p.is_embedded FROM plugins p WHERE p.plugin_id=\? AND p.status=1 AND p.deleted_at IS NULL AND 1=1 FOR UPDATE`).WithArgs("skill-new").WillReturnRows(sqlmock.NewRows([]string{"plugin_type", "is_embedded"}).AddRow(model.PluginTypeSkill, false))
	mock.ExpectQuery(`SELECT relation_id,target_plugin_id,relation_type,sort_order,relation_json,status FROM plugin_relations\s+WHERE source_plugin_id=\? AND deleted_at IS NULL ORDER BY relation_id FOR UPDATE`).
		WithArgs("expert-9").
		WillReturnRows(sqlmock.NewRows([]string{"relation_id", "target_plugin_id", "relation_type", "sort_order", "relation_json", "status"}).
			AddRow("rel-old", "skill-old", "expert_skill", 0, nil, 1))
	mock.ExpectExec(`INSERT INTO plugin_relations`).WithArgs("rel-new", "expert-9", "skill-new", "expert_skill", 0, nil, 1, "admin", now, now).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(`UPDATE plugin_relations SET deleted_at=`).WithArgs(now, now, "rel-old", "expert-9").WillReturnResult(sqlmock.NewResult(0, 1))
	// Phase 5: soft-delete the previous child + its outgoing relations, delete audit.
	mock.ExpectQuery(`SELECT .* FROM plugins p WHERE p.plugin_id=\? AND p.status=1 AND p.deleted_at IS NULL FOR UPDATE`).
		WithArgs("skill-old").
		WillReturnRows(sqlmock.NewRows(pluginTestColumns()).AddRow("skill-old", "S", model.PluginTypeSkill, 1, nil, []byte(`[]`), "", "admin", "", model.PluginVisibilitySystem, model.PluginListingStatePublished, "Root", "human", nil, nil, "", 0, []byte(`{}`), []byte(`{}`), nil, "sha256:som", "sha256:sop", nil, nil, 1, now, now, nil))
	mock.ExpectExec(`UPDATE plugins SET deleted_at=\?,updated_at=\? WHERE plugin_id=\? AND deleted_at IS NULL`).WithArgs(now, now, "skill-old").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE plugin_relations SET deleted_at=\?,updated_at=\?\s+WHERE source_plugin_id=\? AND deleted_at IS NULL`).WithArgs(now, now, "skill-old").WillReturnResult(sqlmock.NewResult(0, 1))
	expectCancelPendingReview(mock, now, "skill-old", "admin", "Root")
	mock.ExpectExec(`INSERT INTO plugin_audit_logs`).WithArgs("audit-del", "skill-old", "delete", "admin", "Root", "req", "sha256:sop", nil, "{}", "{}", nil, now).WillReturnResult(sqlmock.NewResult(1, 1))
	// Phase 6: update audit for the top, create audit for the new child.
	mock.ExpectExec(`INSERT INTO plugin_audit_logs`).WithArgs("audit-top", "expert-9", "update", "admin", "Root", "req", "sha256:pold", "sha256:p2", `{"m":2}`, `{"p":2}`, nil, now).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(`INSERT INTO plugin_audit_logs`).WithArgs("audit-child", "skill-new", "create", "admin", "Root", "req", nil, "sha256:sp", "{}", "{}", nil, now).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	sync, err := r.RebuildGraph(context.Background(), scope,
		Mutation{Plugin: top, Relations: []model.PluginRelation{{TargetPluginID: "skill-new", Type: "expert_skill", Status: 1}}, OperatorID: "admin", OperatorName: "Root", RequestID: "req"},
		[]Mutation{{Plugin: newSkill, OperatorID: "admin", OperatorName: "Root", RequestID: "req"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(sync.Created) != 1 || sync.Created[0] != "rel-new" || len(sync.Deleted) != 1 || sync.Deleted[0] != "rel-old" {
		t.Fatalf("sync = %#v", sync)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestRebuildGraphMissingTopIsNotFound proves a rebuild of an absent (or
// out-of-scope) top plugin rolls back with ErrNotFound before any write.
func TestRebuildGraphMissingTopIsNotFound(t *testing.T) {
	db, mock, _ := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	defer db.Close()
	r := New(db)
	now := time.Date(2026, 8, 26, 8, 0, 0, 0, time.UTC)
	r.now = func() time.Time { return now }
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT .* FROM plugins p WHERE p.plugin_id=\? AND p.status=1 AND p.deleted_at IS NULL FOR UPDATE`).
		WithArgs("ghost").WillReturnRows(sqlmock.NewRows(pluginTestColumns()))
	mock.ExpectRollback()
	top := model.Plugin{ID: "ghost", Name: "E", Type: model.PluginTypeExpert, Tags: []byte(`[]`), Manifest: []byte(`{}`), Package: []byte(`{}`), Status: 1}
	_, err := r.RebuildGraph(context.Background(), Scope{CallerUID: "admin", Admin: true}, Mutation{Plugin: top}, nil)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestLockRelationTargetsRejectsEmbeddedExternalTarget locks the adoption guard:
// a standalone write path (inGraph nil) that points a relation at an is_embedded=1
// target is refused with ErrInvalidRelation, so a tenant/admin cannot adopt
// another graph's bundled skill / squad member (which a later reupload would
// soft-delete underneath them).
func TestLockRelationTargetsRejectsEmbeddedExternalTarget(t *testing.T) {
	db, mock, _ := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	defer db.Close()
	mock.ExpectBegin()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	scope := Scope{CallerUID: "caller", SpaceID: "space"}
	mock.ExpectQuery(`SELECT p.plugin_type,p.is_embedded FROM plugins p WHERE p.plugin_id=\? AND p.status=1 AND p.deleted_at IS NULL.*FOR UPDATE`).
		WithArgs("embedded", scope.SpaceID, scope.CallerUID).
		WillReturnRows(sqlmock.NewRows([]string{"plugin_type", "is_embedded"}).AddRow(model.PluginTypeSkill, true))
	err = lockRelationTargets(context.Background(), tx, scope, model.PluginTypeExpert, []model.PluginRelation{{TargetPluginID: "embedded", Type: "expert_skill"}}, nil)
	if !errors.Is(err, ErrInvalidRelation) {
		t.Fatalf("lockRelationTargets error = %v, want ErrInvalidRelation", err)
	}
	_ = tx.Rollback()
}

// TestLockRelationTargetsAllowsEmbeddedIntraGraphTarget proves the exemption: a
// container top wiring its just-created embedded child (the target ID is in the
// in-graph set) is allowed even though the target is is_embedded=1.
func TestLockRelationTargetsAllowsEmbeddedIntraGraphTarget(t *testing.T) {
	db, mock, _ := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	defer db.Close()
	mock.ExpectBegin()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	scope := Scope{CallerUID: "admin", Admin: true}
	mock.ExpectQuery(`SELECT p.plugin_type,p.is_embedded FROM plugins p WHERE p.plugin_id=\? AND p.status=1 AND p.deleted_at IS NULL AND 1=1 FOR UPDATE`).
		WithArgs("bundled-skill").
		WillReturnRows(sqlmock.NewRows([]string{"plugin_type", "is_embedded"}).AddRow(model.PluginTypeSkill, true))
	inGraph := map[string]struct{}{"bundled-skill": {}, "expert-top": {}}
	if err = lockRelationTargets(context.Background(), tx, scope, model.PluginTypeExpert, []model.PluginRelation{{TargetPluginID: "bundled-skill", Type: "expert_skill"}}, inGraph); err != nil {
		t.Fatalf("intra-graph embedded edge rejected: %v", err)
	}
	_ = tx.Rollback()
}

// TestDeleteGraphSoftDeletesTopAndEmbeddedChildren locks the atomic container
// delete: the top is proved under lock, refused if a live incoming relation
// exists, then soft-deleted with its outgoing edges and a delete audit; each
// embedded child is soft-deleted through softDeleteRebuiltChild with its own
// delete audit — all in one transaction.
func TestDeleteGraphSoftDeletesTopAndEmbeddedChildren(t *testing.T) {
	db, mock, _ := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	defer db.Close()
	r := New(db)
	now := time.Date(2026, 8, 26, 8, 0, 0, 0, time.UTC)
	r.now = func() time.Time { return now }
	ids := []string{"audit-top", "audit-child"}
	r.id = func() string { x := ids[0]; ids = ids[1:]; return x }
	scope := Scope{CallerUID: "admin", Admin: true}

	mock.ExpectBegin()
	// Lock + prove the top exists (admin: no owner/space predicate).
	mock.ExpectQuery(`SELECT .* FROM plugins p WHERE p.plugin_id=\? AND p.status=1 AND p.deleted_at IS NULL FOR UPDATE`).
		WithArgs("expert-9").
		WillReturnRows(sqlmock.NewRows(pluginTestColumns()).AddRow("expert-9", "E", model.PluginTypeExpert, 0, nil, []byte(`[]`), "", "owner-1", "", model.PluginVisibilityPublic, model.PluginListingStatePublished, "Orig", "human", nil, nil, "", 0, []byte(`{}`), []byte(`{}`), nil, "sha256:m", "sha256:ptop", nil, nil, 1, now, now, nil))
	// No live incoming relation to the top.
	mock.ExpectQuery(`SELECT r.relation_id FROM plugin_relations r.*r.target_plugin_id=\?.*FOR UPDATE`).
		WithArgs("expert-9").WillReturnRows(sqlmock.NewRows([]string{"relation_id"}))
	// In-tx child derivation: AFTER the top lock + incoming-relation guard, the
	// embedded child set is read from the committed graph (the expert's is_embedded
	// expert_skill targets) rather than supplied by the caller — the query order
	// proves it runs under the lock, so a delete racing a concurrent reupload tears
	// down the children the prior op actually committed.
	mock.ExpectQuery(`SELECT r.target_plugin_id FROM plugin_relations r\s+JOIN plugins p ON p.plugin_id=r.target_plugin_id\s+WHERE r.source_plugin_id=\? AND r.relation_type=\? AND r.deleted_at IS NULL\s+AND p.is_embedded=1 AND p.status=1 AND p.deleted_at IS NULL\s+ORDER BY r.sort_order,r.relation_id`).
		WithArgs("expert-9", "expert_skill").
		WillReturnRows(sqlmock.NewRows([]string{"target_plugin_id"}).AddRow("skill-emb"))
	// Soft-delete the top + its outgoing relations, then the top delete audit.
	mock.ExpectExec(`UPDATE plugins SET deleted_at=\?,updated_at=\? WHERE plugin_id=\? AND deleted_at IS NULL`).WithArgs(now, now, "expert-9").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE plugin_relations SET deleted_at=\?,updated_at=\?\s+WHERE source_plugin_id=\? AND deleted_at IS NULL`).WithArgs(now, now, "expert-9").WillReturnResult(sqlmock.NewResult(0, 1))
	expectCancelPendingReview(mock, now, "expert-9", "admin", "Root")
	mock.ExpectExec(`INSERT INTO plugin_audit_logs`).WithArgs("audit-top", "expert-9", "delete", "admin", "Root", "req", "sha256:ptop", nil, "{}", "{}", nil, now).WillReturnResult(sqlmock.NewResult(1, 1))
	// The embedded child: lock, soft-delete + its relations, delete audit.
	mock.ExpectQuery(`SELECT .* FROM plugins p WHERE p.plugin_id=\? AND p.status=1 AND p.deleted_at IS NULL FOR UPDATE`).
		WithArgs("skill-emb").
		WillReturnRows(sqlmock.NewRows(pluginTestColumns()).AddRow("skill-emb", "S", model.PluginTypeSkill, 1, nil, []byte(`[]`), "", "owner-1", "", model.PluginVisibilitySystem, model.PluginListingStatePublished, "Orig", "human", nil, nil, "", 0, []byte(`{}`), []byte(`{}`), nil, "sha256:sm", "sha256:pemb", nil, nil, 1, now, now, nil))
	mock.ExpectExec(`UPDATE plugins SET deleted_at=\?,updated_at=\? WHERE plugin_id=\? AND deleted_at IS NULL`).WithArgs(now, now, "skill-emb").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE plugin_relations SET deleted_at=\?,updated_at=\?\s+WHERE source_plugin_id=\? AND deleted_at IS NULL`).WithArgs(now, now, "skill-emb").WillReturnResult(sqlmock.NewResult(0, 1))
	expectCancelPendingReview(mock, now, "skill-emb", "admin", "Root")
	mock.ExpectExec(`INSERT INTO plugin_audit_logs`).WithArgs("audit-child", "skill-emb", "delete", "admin", "Root", "req", "sha256:pemb", nil, "{}", "{}", nil, now).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	if err := r.DeleteGraph(context.Background(), scope, "expert-9", "admin", "Root", "req", nil); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestDeleteGraphRejectsLiveIncomingRelationToTop proves the top keeps Delete's
// incoming-relation guard: a live plugin still referencing the top blocks the
// whole graph delete with ErrConflict before any write.
func TestDeleteGraphRejectsLiveIncomingRelationToTop(t *testing.T) {
	db, mock, _ := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	defer db.Close()
	r := New(db)
	now := time.Date(2026, 8, 26, 8, 0, 0, 0, time.UTC)
	r.now = func() time.Time { return now }
	scope := Scope{CallerUID: "admin", Admin: true}
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT .* FROM plugins p WHERE p.plugin_id=\? AND p.status=1 AND p.deleted_at IS NULL FOR UPDATE`).
		WithArgs("expert-9").
		WillReturnRows(sqlmock.NewRows(pluginTestColumns()).AddRow("expert-9", "E", model.PluginTypeExpert, 0, nil, []byte(`[]`), "", "owner-1", "", model.PluginVisibilityPublic, model.PluginListingStatePublished, "Orig", "human", nil, nil, "", 0, []byte(`{}`), []byte(`{}`), nil, "sha256:m", "sha256:ptop", nil, nil, 1, now, now, nil))
	mock.ExpectQuery(`SELECT r.relation_id FROM plugin_relations r.*r.target_plugin_id=\?.*FOR UPDATE`).
		WithArgs("expert-9").WillReturnRows(sqlmock.NewRows([]string{"relation_id"}).AddRow("incoming"))
	mock.ExpectRollback()
	if err := r.DeleteGraph(context.Background(), scope, "expert-9", "admin", "Root", "req", nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestRebuildGraphSoftDeletesCurrentChildFromCommittedGraph is the round-5
// concurrency regression: RebuildGraph must NOT act on a caller-supplied,
// pre-parse child list — it derives the embedded child set from the CURRENT
// committed graph AFTER taking the top's FOR UPDATE lock. Here the DB reports
// "skill-current" (the child a prior committed reupload R1 swapped in), so THAT
// row — not any stale "skill-stale" a racing op R2 read before the lock — is the
// one soft-deleted in phase 5. This closes the concurrent-reupload /
// delete-vs-reupload race where a stale snapshot severed the top→child edge but
// no-op'd on the already-gone old child, orphaning the swapped-in one.
func TestRebuildGraphSoftDeletesCurrentChildFromCommittedGraph(t *testing.T) {
	db, mock, _ := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	defer db.Close()
	r := New(db)
	now := time.Date(2026, 8, 26, 8, 0, 0, 0, time.UTC)
	r.now = func() time.Time { return now }
	ids := []string{"rel-new", "audit-del", "audit-top", "audit-child"}
	r.id = func() string { x := ids[0]; ids = ids[1:]; return x }
	scope := Scope{CallerUID: "admin", Admin: true}
	space := ""
	newSkill := model.Plugin{ID: "skill-new", Name: "S2", Type: model.PluginTypeSkill, IsEmbedded: true, Tags: []byte(`[]`), OwnerUID: "admin", SpaceID: &space, Visibility: model.PluginVisibilitySystem, CreatorName: "Root", CreatedByType: "human", Manifest: []byte(`{}`), Package: []byte(`{}`), ManifestHash: "sha256:sm", PluginHash: "sha256:sp", Status: 1}
	top := model.Plugin{ID: "expert-9", Name: "E2", Type: model.PluginTypeExpert, Tags: []byte(`["ops"]`), Visibility: model.PluginVisibilityPublic, Manifest: []byte(`{"m":2}`), Package: []byte(`{"p":2}`), ManifestHash: "sha256:m2", PluginHash: "sha256:p2", Status: 1}

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT .* FROM plugins p WHERE p.plugin_id=\? AND p.status=1 AND p.deleted_at IS NULL FOR UPDATE`).
		WithArgs("expert-9").
		WillReturnRows(sqlmock.NewRows(pluginTestColumns()).AddRow("expert-9", "E", model.PluginTypeExpert, 0, nil, []byte(`["ops"]`), "", "owner-1", "", model.PluginVisibilityPublic, model.PluginListingStatePublished, "Orig", "human", nil, nil, "", 0, []byte(`{}`), []byte(`{}`), nil, "sha256:m", "sha256:pold", nil, nil, 1, now, now, nil))
	// The DB — not the caller — decides the child set: it reports skill-current.
	mock.ExpectQuery(`SELECT r.target_plugin_id FROM plugin_relations r\s+JOIN plugins p ON p.plugin_id=r.target_plugin_id\s+WHERE r.source_plugin_id=\? AND r.relation_type=\? AND r.deleted_at IS NULL\s+AND p.is_embedded=1 AND p.status=1 AND p.deleted_at IS NULL\s+ORDER BY r.sort_order,r.relation_id`).
		WithArgs("expert-9", "expert_skill").
		WillReturnRows(sqlmock.NewRows([]string{"target_plugin_id"}).AddRow("skill-current"))
	// The child inherits the locked top's owner and listing state; its legacy
	// `public` visibility is normalized to `system` (NormalizeLegacyVisibility) on
	// re-stamp.
	mock.ExpectExec(`INSERT INTO plugins`).WithArgs("skill-new", "S2", model.PluginTypeSkill, true, nil, "[]", "", "owner-1", "", model.PluginVisibilitySystem, model.PluginListingStatePublished, "Root", "human", nil, nil, "", 0, "{}", "{}", nil, "sha256:sm", "sha256:sp", nil, nil, 1, now, now).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(`UPDATE plugins SET plugin_name=.*WHERE plugin_id=\? AND deleted_at IS NULL`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT EXISTS\(SELECT 1 FROM plugin_placements WHERE plugin_id=\? AND placement_code=\?`).WillReturnRows(sqlmock.NewRows([]string{"e"}).AddRow(1))
	mock.ExpectExec(`UPDATE plugin_placements SET category_id=\?,updated_at=\? WHERE plugin_id=\?`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT p.plugin_type,p.is_embedded FROM plugins p WHERE p.plugin_id=\? AND p.status=1 AND p.deleted_at IS NULL AND 1=1 FOR UPDATE`).WithArgs("skill-new").WillReturnRows(sqlmock.NewRows([]string{"plugin_type", "is_embedded"}).AddRow(model.PluginTypeSkill, false))
	mock.ExpectQuery(`SELECT relation_id,target_plugin_id,relation_type,sort_order,relation_json,status FROM plugin_relations\s+WHERE source_plugin_id=\? AND deleted_at IS NULL ORDER BY relation_id FOR UPDATE`).
		WithArgs("expert-9").
		WillReturnRows(sqlmock.NewRows([]string{"relation_id", "target_plugin_id", "relation_type", "sort_order", "relation_json", "status"}).AddRow("rel-old", "skill-current", "expert_skill", 0, nil, 1))
	mock.ExpectExec(`INSERT INTO plugin_relations`).WithArgs("rel-new", "expert-9", "skill-new", "expert_skill", 0, nil, 1, "admin", now, now).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(`UPDATE plugin_relations SET deleted_at=`).WithArgs(now, now, "rel-old", "expert-9").WillReturnResult(sqlmock.NewResult(0, 1))
	// Phase 5 tears down skill-current (the DB-derived child), never a stale id.
	mock.ExpectQuery(`SELECT .* FROM plugins p WHERE p.plugin_id=\? AND p.status=1 AND p.deleted_at IS NULL FOR UPDATE`).
		WithArgs("skill-current").
		WillReturnRows(sqlmock.NewRows(pluginTestColumns()).AddRow("skill-current", "S", model.PluginTypeSkill, 1, nil, []byte(`[]`), "", "admin", "", model.PluginVisibilitySystem, model.PluginListingStatePublished, "Root", "human", nil, nil, "", 0, []byte(`{}`), []byte(`{}`), nil, "sha256:som", "sha256:sop", nil, nil, 1, now, now, nil))
	mock.ExpectExec(`UPDATE plugins SET deleted_at=\?,updated_at=\? WHERE plugin_id=\? AND deleted_at IS NULL`).WithArgs(now, now, "skill-current").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE plugin_relations SET deleted_at=\?,updated_at=\?\s+WHERE source_plugin_id=\? AND deleted_at IS NULL`).WithArgs(now, now, "skill-current").WillReturnResult(sqlmock.NewResult(0, 1))
	expectCancelPendingReview(mock, now, "skill-current", "admin", "Root")
	mock.ExpectExec(`INSERT INTO plugin_audit_logs`).WithArgs("audit-del", "skill-current", "delete", "admin", "Root", "req", "sha256:sop", nil, "{}", "{}", nil, now).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(`INSERT INTO plugin_audit_logs`).WithArgs("audit-top", "expert-9", "update", "admin", "Root", "req", "sha256:pold", "sha256:p2", `{"m":2}`, `{"p":2}`, nil, now).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(`INSERT INTO plugin_audit_logs`).WithArgs("audit-child", "skill-new", "create", "admin", "Root", "req", nil, "sha256:sp", "{}", "{}", nil, now).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	if _, err := r.RebuildGraph(context.Background(), scope,
		Mutation{Plugin: top, Relations: []model.PluginRelation{{TargetPluginID: "skill-new", Type: "expert_skill", Status: 1}}, OperatorID: "admin", OperatorName: "Root", RequestID: "req"},
		[]Mutation{{Plugin: newSkill, OperatorID: "admin", OperatorName: "Root", RequestID: "req"}}); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestDeleteGraphDerivesEmbeddedSquadChildrenUnderLock exercises the expert_team
// branch of the in-tx child derivation: after the team top is locked, the member
// experts are read from the committed graph (its embedded expert_team_expert
// targets) and then each member's own embedded expert_skill targets — the two-
// level teardown a caller list used to carry. Deriving it under the lock is what
// keeps a delete racing a concurrent squad reupload from orphaning a swapped-in
// member or member-skill.
func TestDeleteGraphDerivesEmbeddedSquadChildrenUnderLock(t *testing.T) {
	db, mock, _ := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	defer db.Close()
	r := New(db)
	now := time.Date(2026, 8, 26, 8, 0, 0, 0, time.UTC)
	r.now = func() time.Time { return now }
	ids := []string{"audit-top", "audit-member", "audit-mskill"}
	r.id = func() string { x := ids[0]; ids = ids[1:]; return x }
	scope := Scope{CallerUID: "admin", Admin: true}

	mock.ExpectBegin()
	// Lock + prove the team top exists.
	mock.ExpectQuery(`SELECT .* FROM plugins p WHERE p.plugin_id=\? AND p.status=1 AND p.deleted_at IS NULL FOR UPDATE`).
		WithArgs("team-1").
		WillReturnRows(sqlmock.NewRows(pluginTestColumns()).AddRow("team-1", "T", model.PluginTypeExpertTeam, 0, nil, []byte(`[]`), "", "owner-1", "", model.PluginVisibilityPublic, model.PluginListingStatePublished, "Orig", "human", nil, nil, "", 0, []byte(`{}`), []byte(`{}`), nil, "sha256:m", "sha256:ptop", nil, nil, 1, now, now, nil))
	// No live incoming relation to the top.
	mock.ExpectQuery(`SELECT r.relation_id FROM plugin_relations r.*r.target_plugin_id=\?.*FOR UPDATE`).
		WithArgs("team-1").WillReturnRows(sqlmock.NewRows([]string{"relation_id"}))
	// In-tx derivation, level 1: the team's embedded member experts.
	mock.ExpectQuery(`SELECT r.target_plugin_id FROM plugin_relations r\s+JOIN plugins p ON p.plugin_id=r.target_plugin_id\s+WHERE r.source_plugin_id=\? AND r.relation_type=\? AND r.deleted_at IS NULL\s+AND p.is_embedded=1 AND p.status=1 AND p.deleted_at IS NULL\s+ORDER BY r.sort_order,r.relation_id`).
		WithArgs("team-1", "expert_team_expert").
		WillReturnRows(sqlmock.NewRows([]string{"target_plugin_id"}).AddRow("member-1"))
	// Level 2: that member's own embedded bundled skills.
	mock.ExpectQuery(`SELECT r.target_plugin_id FROM plugin_relations r\s+JOIN plugins p ON p.plugin_id=r.target_plugin_id\s+WHERE r.source_plugin_id=\? AND r.relation_type=\? AND r.deleted_at IS NULL\s+AND p.is_embedded=1 AND p.status=1 AND p.deleted_at IS NULL\s+ORDER BY r.sort_order,r.relation_id`).
		WithArgs("member-1", "expert_skill").
		WillReturnRows(sqlmock.NewRows([]string{"target_plugin_id"}).AddRow("mskill-1"))
	// Soft-delete the top + its outgoing relations, top delete audit.
	mock.ExpectExec(`UPDATE plugins SET deleted_at=\?,updated_at=\? WHERE plugin_id=\? AND deleted_at IS NULL`).WithArgs(now, now, "team-1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE plugin_relations SET deleted_at=\?,updated_at=\?\s+WHERE source_plugin_id=\? AND deleted_at IS NULL`).WithArgs(now, now, "team-1").WillReturnResult(sqlmock.NewResult(0, 1))
	expectCancelPendingReview(mock, now, "team-1", "admin", "Root")
	mock.ExpectExec(`INSERT INTO plugin_audit_logs`).WithArgs("audit-top", "team-1", "delete", "admin", "Root", "req", "sha256:ptop", nil, "{}", "{}", nil, now).WillReturnResult(sqlmock.NewResult(1, 1))
	// Child member: lock, soft-delete + relations, delete audit.
	mock.ExpectQuery(`SELECT .* FROM plugins p WHERE p.plugin_id=\? AND p.status=1 AND p.deleted_at IS NULL FOR UPDATE`).
		WithArgs("member-1").
		WillReturnRows(sqlmock.NewRows(pluginTestColumns()).AddRow("member-1", "M", model.PluginTypeExpert, 1, nil, []byte(`[]`), "", "owner-1", "", model.PluginVisibilitySystem, model.PluginListingStatePublished, "Orig", "human", nil, nil, "", 0, []byte(`{}`), []byte(`{}`), nil, "sha256:mm", "sha256:pmem", nil, nil, 1, now, now, nil))
	mock.ExpectExec(`UPDATE plugins SET deleted_at=\?,updated_at=\? WHERE plugin_id=\? AND deleted_at IS NULL`).WithArgs(now, now, "member-1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE plugin_relations SET deleted_at=\?,updated_at=\?\s+WHERE source_plugin_id=\? AND deleted_at IS NULL`).WithArgs(now, now, "member-1").WillReturnResult(sqlmock.NewResult(0, 1))
	expectCancelPendingReview(mock, now, "member-1", "admin", "Root")
	mock.ExpectExec(`INSERT INTO plugin_audit_logs`).WithArgs("audit-member", "member-1", "delete", "admin", "Root", "req", "sha256:pmem", nil, "{}", "{}", nil, now).WillReturnResult(sqlmock.NewResult(1, 1))
	// Child member-skill: lock, soft-delete + relations, delete audit.
	mock.ExpectQuery(`SELECT .* FROM plugins p WHERE p.plugin_id=\? AND p.status=1 AND p.deleted_at IS NULL FOR UPDATE`).
		WithArgs("mskill-1").
		WillReturnRows(sqlmock.NewRows(pluginTestColumns()).AddRow("mskill-1", "S", model.PluginTypeSkill, 1, nil, []byte(`[]`), "", "owner-1", "", model.PluginVisibilitySystem, model.PluginListingStatePublished, "Orig", "human", nil, nil, "", 0, []byte(`{}`), []byte(`{}`), nil, "sha256:sm", "sha256:pms", nil, nil, 1, now, now, nil))
	mock.ExpectExec(`UPDATE plugins SET deleted_at=\?,updated_at=\? WHERE plugin_id=\? AND deleted_at IS NULL`).WithArgs(now, now, "mskill-1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE plugin_relations SET deleted_at=\?,updated_at=\?\s+WHERE source_plugin_id=\? AND deleted_at IS NULL`).WithArgs(now, now, "mskill-1").WillReturnResult(sqlmock.NewResult(0, 1))
	expectCancelPendingReview(mock, now, "mskill-1", "admin", "Root")
	mock.ExpectExec(`INSERT INTO plugin_audit_logs`).WithArgs("audit-mskill", "mskill-1", "delete", "admin", "Root", "req", "sha256:pms", nil, "{}", "{}", nil, now).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	if err := r.DeleteGraph(context.Background(), scope, "team-1", "admin", "Root", "req", nil); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
