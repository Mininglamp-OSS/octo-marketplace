package db

import (
	"database/sql"
	"testing"

	pluginsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/plugin"
)

// Publication fixtures state their intended saved label explicitly, as managed
// Expert/Team clients must now do. A NULL label keeps the legacy-baseline error
// path testable; it does not make an incomplete resource publishable.
func explicitTestPublication(t *testing.T, db *sql.DB, id string) pluginsvc.PublishParams {
	t.Helper()
	var label sql.NullString
	if err := db.QueryRow(`SELECT current_version FROM plugins WHERE plugin_id=?`, id).Scan(&label); err != nil {
		t.Fatal(err)
	}
	version := "1.0.0"
	if label.Valid && label.String != "" {
		version = label.String
	}
	return pluginsvc.PublishParams{PluginID: id, Version: version}
}
