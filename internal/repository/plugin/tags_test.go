package plugin

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

func TestListTagsAggregatesVisibleRowsWithFilters(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := New(db)
	query := `SELECT jt.tag, COUNT\(DISTINCT p.plugin_id\) cnt FROM plugins p` +
		` JOIN plugin_placements pp ON pp.plugin_id=p.plugin_id AND pp.placement_code=\? AND pp.visible=1` +
		` JOIN JSON_TABLE\(p.tags_json, '\$\[\*\]' COLUMNS \(tag VARCHAR\(128\) CHARACTER SET utf8mb4 PATH '\$'\)\) jt` +
		` WHERE p.status=1 AND p.deleted_at IS NULL AND p.is_embedded=0 AND \(p.visibility IN \('public','system'\) OR \(p.space_id = \? AND \(\(p.visibility = 'space' AND p.listing_state = 'published'\) OR p.owner_uid = \?\)\)\)` +
		` AND p.plugin_type=\? AND p.owner_uid=\? AND p.space_id=\? AND jt.tag IS NOT NULL AND jt.tag <> ''` +
		` AND jt.tag LIKE \? ESCAPE '!'` +
		` GROUP BY jt.tag ORDER BY cnt DESC, jt.tag ASC LIMIT \?`
	mock.ExpectQuery(query).
		WithArgs("default", "space-a", "caller-a", "connector", "caller-a", "space-a", "%de!%v%", 5).
		WillReturnRows(sqlmock.NewRows([]string{"tag", "cnt"}).AddRow("dev", 3).AddRow("devops", 1))
	tags, err := repo.ListTags(context.Background(), Scope{CallerUID: "caller-a", SpaceID: "space-a"}, TagListFilter{
		PlacementCode: "default",
		Type:          model.PluginTypeConnector,
		Keyword:       "de%v",
		Mine:          true,
		Limit:         5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(tags) != 2 || tags[0] != (model.TagFilter{Name: "dev", Count: 3}) {
		t.Fatalf("tags = %#v", tags)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestListTagsDefaultsAndClampsLimit(t *testing.T) {
	for limit, want := range map[int]int{0: 50, 500: 100} {
		db, mock, _ := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
		repo := New(db)
		mock.ExpectQuery(`SELECT jt.tag, .* FROM plugins p JOIN JSON_TABLE.* GROUP BY jt.tag .* LIMIT \?`).
			WithArgs("space-a", "caller-a", want).
			WillReturnRows(sqlmock.NewRows([]string{"tag", "cnt"}))
		if _, err := repo.ListTags(context.Background(), Scope{CallerUID: "caller-a", SpaceID: "space-a"}, TagListFilter{Limit: limit}); err != nil {
			t.Fatal(err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("limit %d: %v", limit, err)
		}
		db.Close()
	}
}
