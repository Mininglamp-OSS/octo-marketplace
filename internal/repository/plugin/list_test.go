package plugin

import (
	"context"
	"database/sql/driver"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

func TestListReturnsTotalAndAppliesConfirmedFilters(t *testing.T) {
	db, mock, _ := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	defer db.Close()
	scope := Scope{CallerUID: "caller", SpaceID: "space"}
	args := []driver.Value{"home", "space", "caller", model.PluginTypeSkill, "cat", "100%_done!", "mine", "%100!%!_done!!%", "%100!%!_done!!%", "caller", "space"}
	mock.ExpectQuery(`SELECT COUNT\(DISTINCT p.plugin_id\).*JOIN plugin_placements.*pp.placement_code=\?.*p.status=1.*JSON_CONTAINS.*JSON_CONTAINS.*\(p.plugin_name LIKE \? ESCAPE '!' OR JSON_UNQUOTE\(JSON_EXTRACT\(p.manifest_json, '\$\.description'\)\) COLLATE utf8mb4_unicode_ci LIKE \? ESCAPE '!'\).*p.owner_uid=\? AND p.space_id=\?`).WithArgs(args...).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(7))
	mock.ExpectQuery(`SELECT .*JOIN plugin_placements.*p.status=1.*GROUP BY p.plugin_id ORDER BY MIN\(pp.sort_order\) ASC,p.plugin_id ASC LIMIT \? OFFSET \?`).WithArgs(append(args, 20, 0)...).WillReturnRows(sqlmock.NewRows(pluginTestColumns()))
	items, total, err := New(db).List(context.Background(), scope, ListFilter{PlacementCode: "home", Type: model.PluginTypeSkill, CategoryID: "cat", Tags: []string{"100%_done!", "mine"}, Keyword: "100%_done!", Mine: true, Sort: "placement"})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 || total != 7 {
		t.Fatalf("items=%d total=%d", len(items), total)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestAdminListHydratesSceneCodesForPage(t *testing.T) {
	db, mock, _ := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	defer db.Close()
	now := time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)

	mock.ExpectQuery(`SELECT COUNT\(DISTINCT p.plugin_id\).*FROM plugins p.*p.plugin_type=\?`).
		WithArgs(model.PluginTypeSkill).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(2))
	columns := pluginTestColumns()
	summaryColumns := append([]string{}, columns[:19]...)
	summaryColumns = append(summaryColumns, columns[21:]...)
	summaryColumns = append(summaryColumns, "view_count", "install_count", "download_count")
	rows := sqlmock.NewRows(summaryColumns).
		AddRow("plugin-a", "A", model.PluginTypeSkill, 0, nil, []byte(`[]`), "pub", "owner", nil, model.PluginVisibilitySystem, model.PluginListingStatePublished, "Creator", "human", nil, nil, "", 0, nil, []byte(`{}`), "sha256:m1", "sha256:p1", nil, nil, 1, now, now, nil, 0, 0, 0).
		AddRow("plugin-b", "B", model.PluginTypeSkill, 0, nil, []byte(`[]`), "pub", "owner", nil, model.PluginVisibilitySystem, model.PluginListingStatePublished, "Creator", "human", nil, nil, "", 0, nil, []byte(`{}`), "sha256:m2", "sha256:p2", nil, nil, 1, now, now, nil, 0, 0, 0)
	mock.ExpectQuery(`SELECT .*FROM plugins p.*p.plugin_type=\?.*LIMIT \? OFFSET \?`).
		WithArgs(model.PluginTypeSkill, 20, 0).
		WillReturnRows(rows)
	mock.ExpectQuery(`SELECT plugin_id,placement_code\s+FROM plugin_placements\s+WHERE plugin_id IN \(\?,\?\)\s+ORDER BY plugin_id,placement_code`).
		WithArgs("plugin-a", "plugin-b").
		WillReturnRows(sqlmock.NewRows([]string{"plugin_id", "placement_code"}).
			AddRow("plugin-a", "default").
			AddRow("plugin-a", "featured"))

	items, total, err := New(db).List(context.Background(), Scope{Admin: true}, ListFilter{AllSpaces: true, Type: model.PluginTypeSkill})
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 || len(items) != 2 {
		t.Fatalf("items=%d total=%d", len(items), total)
	}
	if got := strings.Join(items[0].SceneCodes, ","); got != "default,featured" {
		t.Fatalf("plugin-a scene codes = %q", got)
	}
	if items[1].SceneCodes == nil || len(items[1].SceneCodes) != 0 {
		t.Fatalf("plugin-b scene codes = %#v, want empty non-nil slice", items[1].SceneCodes)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestEscapeLike(t *testing.T) {
	if got := escapeLike(`a!b%c_d`); got != `a!!b!%c!_d` {
		t.Fatalf("escapeLike=%q", got)
	}
}

func TestBuildListQueryKeywordSearchesNameAndManifestDescription(t *testing.T) {
	_, where, args := buildListQuery(Scope{CallerUID: "caller", SpaceID: "space"}, ListFilter{Keyword: `a!b%c_d`})
	wantClause := `(p.plugin_name LIKE ? ESCAPE '!' OR JSON_UNQUOTE(JSON_EXTRACT(p.manifest_json, '$.description')) COLLATE utf8mb4_unicode_ci LIKE ? ESCAPE '!')`
	if !strings.Contains(where, wantClause) {
		t.Fatalf("keyword clause = %q, want name and manifest description", where)
	}
	if len(args) != 4 || args[2] != `%a!!b!%c!_d%` || args[3] != `%a!!b!%c!_d%` {
		t.Fatalf("keyword args = %#v, want the same escaped pattern for name and description", args)
	}
}

func TestListOrderRanksByPluginMetrics(t *testing.T) {
	tests := []struct {
		sort string
		want string
	}{
		{"views", "rm.view_count"},
		{"installs", "rm.install_count"},
		{"downloads", "rm.download_count"},
		{"comprehensive", "TIMESTAMPDIFF"},
	}
	for _, tt := range tests {
		order := listOrder(ListFilter{Sort: tt.sort})
		if !strings.Contains(order, tt.want) || !strings.Contains(order, "resource_type='plugin'") && tt.sort != "comprehensive" {
			t.Fatalf("listOrder(%q) = %q", tt.sort, order)
		}
	}
	if order := listOrder(ListFilter{Sort: "comprehensive"}); !strings.Contains(order, "install_count") || !strings.Contains(order, "view_count") {
		t.Fatalf("comprehensive order = %q", order)
	}
}

func TestGetScansMetricCounters(t *testing.T) {
	db, mock, _ := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	defer db.Close()
	now := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	columns := append(pluginTestColumns(), "view_count", "install_count", "download_count")
	mock.ExpectQuery(`SELECT .*COALESCE\(\(SELECT rm.view_count.*resource_type='plugin'.* FROM plugins p`).
		WithArgs("plugin-a", "space", "caller").
		WillReturnRows(sqlmock.NewRows(columns).AddRow("plugin-a", "Plugin", model.PluginTypeSkill, 0, nil, []byte(`[]`), "pub", "caller", "space", model.PluginVisibilityPrivate, model.PluginListingStatePublished, "Creator", "human", nil, nil, "icons/a.png", 0, 4, []byte(`{}`), []byte(`{}`), nil, "sha256:m", "sha256:p", nil, nil, 1, now, now, nil, 7, 3, 5))
	p, err := New(db).Get(context.Background(), Scope{CallerUID: "caller", SpaceID: "space"}, "plugin-a")
	if err != nil {
		t.Fatal(err)
	}
	if p.ViewCount != 7 || p.InstallCount != 3 || p.DownloadCount != 5 || p.Icon != "icons/a.png" || p.Rating == nil || *p.Rating != 4 {
		t.Fatalf("counters = %d,%d,%d icon=%q rating=%v", p.ViewCount, p.InstallCount, p.DownloadCount, p.Icon, p.Rating)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestCountDeclaredRelationsIgnoresVisibility pins the P1-1 guard query: it
// counts a plugin's live one-level relations to live targets WITHOUT any
// caller-visibility predicate, so the install path can detect a dependency the
// caller could not see (the visibility-filtered read would have dropped it).
func TestCountDeclaredRelationsIgnoresVisibility(t *testing.T) {
	db, mock, _ := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	defer db.Close()
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM plugin_relations r\s+JOIN plugins p ON p.plugin_id=r.target_plugin_id AND p.status=1 AND p.deleted_at IS NULL\s+WHERE r.source_plugin_id=\? AND r.status=1 AND r.deleted_at IS NULL`).
		WithArgs("expert-1").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(3))
	n, err := New(db).CountDeclaredRelations(context.Background(), "expert-1")
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("declared count = %d, want 3", n)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCountMemberRelationsBatchesLiveTargets(t *testing.T) {
	db, mock, _ := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	defer db.Close()
	mock.ExpectQuery(`SELECT r.source_plugin_id, COUNT\(\*\) FROM plugin_relations r\s+JOIN plugins t ON t.plugin_id=r.target_plugin_id AND t.status=1 AND t.deleted_at IS NULL\s+WHERE r.source_plugin_id IN \(\?,\?\) AND r.relation_type='expert_team_expert' AND r.status=1 AND r.deleted_at IS NULL`).
		WithArgs("team-1", "team-2").
		WillReturnRows(sqlmock.NewRows([]string{"source_plugin_id", "count"}).AddRow("team-1", 4))
	counts, err := New(db).CountMemberRelations(context.Background(), []string{"team-1", "team-2"})
	if err != nil {
		t.Fatal(err)
	}
	if counts["team-1"] != 4 || counts["team-2"] != 0 {
		t.Fatalf("counts = %#v", counts)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
