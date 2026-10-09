package plugin

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

func TestDeleteSceneProtectsDefault(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT scene_code FROM plugin_scenes WHERE scene_id=\? FOR UPDATE`).
		WithArgs("default-id").
		WillReturnRows(sqlmock.NewRows([]string{"scene_code"}).AddRow("default"))
	mock.ExpectRollback()

	err = New(db).DeleteScene(context.Background(), "default-id")
	if err != ErrConflict {
		t.Fatalf("error=%v, want ErrConflict", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestUpdatePlacementProtectsDefaultVisibility(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT placement_code FROM plugin_placements WHERE placement_id=\? FOR UPDATE`).
		WithArgs("placement-1").
		WillReturnRows(sqlmock.NewRows([]string{"placement_code"}).AddRow("default"))
	mock.ExpectRollback()
	visible := false

	err = New(db).UpdatePlacement(context.Background(), "placement-1", PlacementUpdate{IsVisible: &visible})
	if err != ErrConflict {
		t.Fatalf("error=%v, want ErrConflict", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCreatePlacementProtectsDefaultVisibility(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT scene_code FROM plugin_scenes WHERE scene_code=\? FOR UPDATE`).
		WithArgs("default").
		WillReturnRows(sqlmock.NewRows([]string{"scene_code"}).AddRow("default"))
	mock.ExpectRollback()

	err = New(db).CreatePlacement(context.Background(), "placement-1", "default", "skill-1", false, 0)
	if err != ErrConflict {
		t.Fatalf("error=%v, want ErrConflict", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestUpdatePlacementOnlyWritesSuppliedFields(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Date(2026, 10, 8, 2, 0, 0, 0, time.UTC)
	repo := New(db)
	repo.now = func() time.Time { return now }
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT placement_code FROM plugin_placements WHERE placement_id=\? FOR UPDATE`).
		WithArgs("placement-1").
		WillReturnRows(sqlmock.NewRows([]string{"placement_code"}).AddRow("featured"))
	mock.ExpectExec(`UPDATE plugin_placements SET sort_order=\?,updated_at=\? WHERE placement_id=\?`).
		WithArgs(25, now, "placement-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	sortOrder := 25

	if err := repo.UpdatePlacement(context.Background(), "placement-1", PlacementUpdate{SortOrder: &sortOrder}); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestGetSceneIncludesUsageCounts(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Now().UTC()
	mock.ExpectQuery(`SELECT s\.scene_id.*plugin_count.*category_count.*FROM plugin_scenes s WHERE s\.scene_id=\?`).
		WithArgs("scene-1").
		WillReturnRows(sqlmock.NewRows([]string{"scene_id", "scene_code", "name", "description", "sort_order", "created_at", "updated_at", "plugin_count", "category_count"}).
			AddRow("scene-1", "featured", "Featured", "", 10, now, now, 4, 2))

	scene, err := New(db).GetScene(context.Background(), "scene-1")
	if err != nil {
		t.Fatal(err)
	}
	if scene.PluginCount != 4 || scene.CategoryCount != 2 {
		t.Fatalf("scene=%#v", scene)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestUpdateSceneOnlyWritesSuppliedFields(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Date(2026, 10, 8, 2, 0, 0, 0, time.UTC)
	repo := New(db)
	repo.now = func() time.Time { return now }
	mock.ExpectExec(`UPDATE plugin_scenes SET description=\?,updated_at=\? WHERE scene_id=\?`).
		WithArgs("Updated", now, "scene-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	description := "Updated"

	if err := repo.UpdateScene(context.Background(), "scene-1", SceneUpdate{Description: &description}); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestDeleteSceneSweepsOnlyInvisibleReferences(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT scene_code FROM plugin_scenes WHERE scene_id=\? FOR UPDATE`).
		WithArgs("scene-1").
		WillReturnRows(sqlmock.NewRows([]string{"scene_code"}).AddRow("featured"))
	mock.ExpectQuery(`SELECT.*JOIN plugins p.*plugin_category_placements.*visible=1`).
		WithArgs("featured", "featured").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectExec(`DELETE FROM plugin_placements WHERE placement_code=\?`).
		WithArgs("featured").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`DELETE FROM plugin_category_placements WHERE placement_code=\?`).
		WithArgs("featured").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`DELETE FROM plugin_scenes WHERE scene_id=\?`).
		WithArgs("scene-1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	if err := New(db).DeleteScene(context.Background(), "scene-1"); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestListPlacementsFiltersSceneTypeAndKeyword(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery(`SELECT COUNT\(\*\).*pp\.placement_code=\?.*p\.plugin_type=\?.*p\.plugin_name LIKE \? ESCAPE '!'`).
		WithArgs("featured", model.PluginTypeSkill, "%ops%").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	now := time.Now().UTC()
	columns := []string{"placement_id", "scene_id", "scene_code", "scene_name", "plugin_id", "plugin_name", "plugin_type", "category_id", "category_name", "visible", "sort_order", "created_at", "updated_at"}
	mock.ExpectQuery(`SELECT pp\.placement_id.*ORDER BY s\.sort_order,pp\.sort_order,p\.plugin_name,p\.plugin_id,pp\.placement_id LIMIT \? OFFSET \?`).
		WithArgs("featured", model.PluginTypeSkill, "%ops%", 10, 20).
		WillReturnRows(sqlmock.NewRows(columns).AddRow("placement-1", "scene-1", "featured", "Featured", "skill-1", "Ops", model.PluginTypeSkill, nil, nil, true, 5, now, now))

	items, total, err := New(db).ListPlacements(context.Background(), PlacementListFilter{SceneCode: "featured", Type: model.PluginTypeSkill, Keyword: "ops", Limit: 10, Offset: 20})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(items) != 1 || items[0].PluginID != "skill-1" || items[0].CategoryID != nil {
		t.Fatalf("total=%d items=%#v", total, items)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCreatePlacementLocksSceneAndInheritsPluginCategory(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Date(2026, 10, 8, 2, 0, 0, 0, time.UTC)
	repo := New(db)
	repo.now = func() time.Time { return now }

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT scene_code FROM plugin_scenes WHERE scene_code=\? FOR UPDATE`).
		WithArgs("featured").
		WillReturnRows(sqlmock.NewRows([]string{"scene_code"}).AddRow("featured"))
	mock.ExpectQuery(`SELECT category_id FROM plugins WHERE plugin_id=\?.*FOR UPDATE`).
		WithArgs("skill-1").
		WillReturnRows(sqlmock.NewRows([]string{"category_id"}).AddRow("cat-1"))
	mock.ExpectQuery(`SELECT EXISTS\(SELECT 1 FROM plugin_placements WHERE plugin_id=\? AND placement_code=\?\)`).
		WithArgs("skill-1", "featured").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectExec(`INSERT INTO plugin_placements`).
		WithArgs("placement-1", "featured", "skill-1", "cat-1", true, 8, now, now).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	if err := repo.CreatePlacement(context.Background(), "placement-1", "featured", "skill-1", true, 8); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestBatchSetPlacementsRollsBackWhenAnyPluginIsMissing(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Date(2026, 10, 8, 2, 0, 0, 0, time.UTC)
	repo := New(db)
	repo.now = func() time.Time { return now }

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT scene_code FROM plugin_scenes WHERE scene_code=\? FOR UPDATE`).
		WithArgs("featured").
		WillReturnRows(sqlmock.NewRows([]string{"scene_code"}).AddRow("featured"))
	mock.ExpectQuery(`SELECT category_id FROM plugins WHERE plugin_id=\?.*FOR UPDATE`).
		WithArgs("skill-1").
		WillReturnRows(sqlmock.NewRows([]string{"category_id"}).AddRow(nil))
	mock.ExpectQuery(`SELECT placement_id FROM plugin_placements`).
		WithArgs("skill-1", "featured").
		WillReturnRows(sqlmock.NewRows([]string{"placement_id"}))
	mock.ExpectExec(`INSERT INTO plugin_placements`).
		WithArgs("placement-1", "featured", "skill-1", nil, true, 10, now, now).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectQuery(`SELECT category_id FROM plugins WHERE plugin_id=\?.*FOR UPDATE`).
		WithArgs("missing").
		WillReturnRows(sqlmock.NewRows([]string{"category_id"}))
	mock.ExpectRollback()

	err = repo.BatchSetPlacements(context.Background(), "featured", []PlacementBatchItem{
		{PlacementID: "placement-1", PluginID: "skill-1"},
		{PlacementID: "placement-2", PluginID: "missing", InputIndex: 7},
	}, true, true, 10)
	var itemErr *BatchItemError
	if !errors.As(err, &itemErr) || itemErr.Index != 7 || !errors.Is(err, ErrNotFound) {
		t.Fatalf("error=%#v, want failed input index 7 wrapping ErrNotFound", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestBatchRemovePlacementsRollsBackWhenAnyPluginIsMissing(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT scene_code FROM plugin_scenes WHERE scene_code=\? FOR UPDATE`).
		WithArgs("featured").WillReturnRows(sqlmock.NewRows([]string{"scene_code"}).AddRow("featured"))
	mock.ExpectQuery(`SELECT category_id FROM plugins WHERE plugin_id=\?.*FOR UPDATE`).
		WithArgs("skill-1").WillReturnRows(sqlmock.NewRows([]string{"category_id"}).AddRow(nil))
	mock.ExpectExec(`DELETE FROM plugin_placements WHERE placement_code=\? AND plugin_id=\?`).
		WithArgs("featured", "skill-1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT category_id FROM plugins WHERE plugin_id=\?.*FOR UPDATE`).
		WithArgs("missing").WillReturnRows(sqlmock.NewRows([]string{"category_id"}))
	mock.ExpectRollback()

	err = New(db).BatchSetPlacements(context.Background(), "featured", []PlacementBatchItem{
		{PluginID: "skill-1"},
		{PluginID: "missing", InputIndex: 7},
	}, false, false, 0)
	var itemErr *BatchItemError
	if !errors.As(err, &itemErr) || itemErr.Index != 7 || !errors.Is(err, ErrNotFound) {
		t.Fatalf("error=%#v, want failed input index 7 wrapping ErrNotFound", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
