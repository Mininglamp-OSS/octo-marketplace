package plugin

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

func TestListVersionsReturnsExactScopedTotalForEmptyPage(t *testing.T) {
	db, mock, _ := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	defer db.Close()
	r := New(db)
	scope := Scope{CallerUID: "caller", SpaceID: "space"}
	now := time.Date(2026, 8, 19, 10, 0, 0, 0, time.UTC)
	mock.ExpectQuery(`SELECT .* FROM plugins p.*p.plugin_id=\?.*p.status=1.*p.space_id = \?.*p.owner_uid = \?`).
		WithArgs("plugin-id", scope.SpaceID, scope.CallerUID).
		WillReturnRows(visiblePluginRow("plugin-id", scope, now))
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM plugin_versions v JOIN plugins p ON p.plugin_id=v.plugin_id WHERE v.plugin_id=\? AND p.status=1.*p.space_id = \?.*p.owner_uid = \?`).
		WithArgs("plugin-id", scope.SpaceID, scope.CallerUID).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(42))
	mock.ExpectQuery(`SELECT v.version_id.*WHERE v.plugin_id=\? AND p.status=1.*p.space_id = \?.*p.owner_uid = \?.*LIMIT \? OFFSET \?`).
		WithArgs("plugin-id", scope.SpaceID, scope.CallerUID, 20, 40).
		WillReturnRows(sqlmock.NewRows([]string{"version_id", "plugin_id", "version", "manifest_hash", "plugin_hash", "relations_json", "changelog", "created_by", "created_at"}))

	items, total, err := r.ListVersions(context.Background(), scope, "plugin-id", 20, 40)
	if err != nil || len(items) != 0 || total != 42 {
		t.Fatalf("items=%#v total=%d err=%v", items, total, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestListVersionsRedactsCrossSpaceRelationTargets(t *testing.T) {
	db, mock, _ := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	defer db.Close()
	r := New(db)
	scope := Scope{CallerUID: "caller", SpaceID: "space"}
	now := time.Date(2026, 8, 19, 10, 0, 0, 0, time.UTC)
	mock.ExpectQuery(`SELECT .* FROM plugins p.*p.plugin_id=\?.*p.status=1.*p.space_id = \?.*p.owner_uid = \?`).
		WithArgs("plugin-id", scope.SpaceID, scope.CallerUID).
		WillReturnRows(visiblePluginRow("plugin-id", scope, now))
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM plugin_versions v JOIN plugins p ON p.plugin_id=v.plugin_id WHERE v.plugin_id=\? AND p.status=1.*p.space_id = \?.*p.owner_uid = \?`).
		WithArgs("plugin-id", scope.SpaceID, scope.CallerUID).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	// The immutable snapshot embeds two relation targets; one is a private target
	// the reading caller cannot see.
	relJSON := `[{"target_plugin_id":"vis-target","relation_type":"expert_skill"},{"target_plugin_id":"priv-target","relation_type":"expert_skill","data":{"secret":"x"}}]`
	mock.ExpectQuery(`SELECT v.version_id.*WHERE v.plugin_id=\? AND p.status=1.*p.space_id = \?.*p.owner_uid = \?.*LIMIT \? OFFSET \?`).
		WithArgs("plugin-id", scope.SpaceID, scope.CallerUID, 20, 0).
		WillReturnRows(sqlmock.NewRows([]string{"version_id", "plugin_id", "version", "manifest_hash", "plugin_hash", "relations_json", "changelog", "created_by", "created_at"}).
			AddRow("v1", "plugin-id", "1.0.0", "mh", "ph", relJSON, nil, "creator", now))
	// The visibility re-check returns only the visible target (args order is
	// map-iteration dependent, so match the query shape, not the args). The
	// aliased `plugins p` table is load-bearing: visibilitySQL references p.*,
	// so an unaliased FROM would be a runtime ERROR 1054 against a real DB.
	mock.ExpectQuery(`SELECT p.plugin_id FROM plugins p WHERE p.plugin_id IN \(.*\?.*\) AND p.status=1 AND p.deleted_at IS NULL AND `).
		WillReturnRows(sqlmock.NewRows([]string{"plugin_id"}).AddRow("vis-target"))

	items, _, err := r.ListVersions(context.Background(), scope, "plugin-id", 20, 0)
	if err != nil || len(items) != 1 {
		t.Fatalf("items=%#v err=%v", items, err)
	}
	var kept []map[string]any
	if err := json.Unmarshal(items[0].Relations, &kept); err != nil {
		t.Fatal(err)
	}
	if len(kept) != 1 || kept[0]["target_plugin_id"] != "vis-target" {
		t.Fatalf("relations not redacted to visible target: %s", items[0].Relations)
	}
	if bytesContains(items[0].Relations, "priv-target") {
		t.Fatalf("private target leaked in version snapshot: %s", items[0].Relations)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func bytesContains(raw json.RawMessage, sub string) bool {
	return strings.Contains(string(raw), sub)
}

func TestListVersionsCrossSpaceReturnsNotFoundBeforeCount(t *testing.T) {
	db, mock, _ := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	defer db.Close()
	scope := Scope{CallerUID: "caller", SpaceID: "space"}
	mock.ExpectQuery(`SELECT .* FROM plugins p.*p.plugin_id=\?.*p.status=1.*p.space_id = \?.*p.owner_uid = \?`).
		WithArgs("foreign", scope.SpaceID, scope.CallerUID).
		WillReturnRows(sqlmock.NewRows(pluginTestColumns()))

	_, _, err := New(db).ListVersions(context.Background(), scope, "foreign", 20, 0)
	if err != ErrNotFound {
		t.Fatalf("err=%v, want ErrNotFound", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func ownedPluginRow(id string, scope Scope, now time.Time) *sqlmock.Rows {
	return sqlmock.NewRows(pluginTestColumns()).AddRow(id, "Plugin", model.PluginTypeExpert, 0, nil, []byte(`[]`), "pub", scope.CallerUID, scope.SpaceID, model.PluginVisibilityPrivate, model.PluginListingStatePublished, "Creator", "human", nil, nil, "", 0, nil, []byte(`{"manifest":true}`), []byte(`{"package":true}`), nil, "sha256:m", "sha256:p", nil, nil, 1, now, now, nil)
}

// visiblePluginRow mirrors ownedPluginRow for the Get visibility path, which
// additionally selects the correlated metric counters.
func visiblePluginRow(id string, scope Scope, now time.Time) *sqlmock.Rows {
	columns := append(pluginTestColumns(), "view_count", "install_count", "download_count")
	return sqlmock.NewRows(columns).AddRow(id, "Plugin", model.PluginTypeExpert, 0, nil, []byte(`[]`), "pub", scope.CallerUID, scope.SpaceID, model.PluginVisibilityPrivate, model.PluginListingStatePublished, "Creator", "human", nil, nil, "", 0, nil, []byte(`{"manifest":true}`), []byte(`{"package":true}`), nil, "sha256:m", "sha256:p", nil, nil, 1, now, now, nil, 0, 0, 0)
}
