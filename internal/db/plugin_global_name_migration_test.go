package db

import (
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	"github.com/go-sql-driver/mysql"
	migrate "github.com/rubenv/sql-migrate"
)

type globalNameFixture struct {
	id, kind, visibility, listing, draft, published string
	status                                          int
	embedded, deleted, legacy                       bool
}

func insertGlobalNameFixture(t *testing.T, database *sql.DB, fixture globalNameFixture) error {
	t.Helper()
	var draft, published any
	if !fixture.legacy {
		key, err := model.NormalizePluginName(fixture.draft)
		if err != nil {
			t.Fatal(err)
		}
		draft = key
		if fixture.published != "" {
			key, err = model.NormalizePluginName(fixture.published)
			if err != nil {
				t.Fatal(err)
			}
			published = key
		}
	}
	_, err := database.Exec(`INSERT INTO plugins(plugin_id,plugin_name,plugin_type,is_embedded,tags_json,owner_uid,space_id,visibility,listing_state,manifest_json,plugin_json,manifest_hash,plugin_hash,status,deleted_at,name_key,published_name_key,name_scope,created_at,updated_at) VALUES (?,?,?,?,JSON_ARRAY(),'owner',?,?,?,JSON_OBJECT(),JSON_OBJECT(),REPEAT('a',71),REPEAT('b',71),?,IF(?,NOW(3),NULL),?,?,?,NOW(3),NOW(3))`, fixture.id, fixture.draft, fixture.kind, fixture.embedded, fixture.id, fixture.visibility, fixture.listing, fixture.status, fixture.deleted, draft, published, fixture.id)
	return err
}

func TestGlobalNameMigrationUpgradeDownPreservesHistoricalRows(t *testing.T) {
	database := isolatedTestDB(t)
	prior := migrationSourceThrough(t, "20261027-02-plugin-visibility-reviews.sql")
	if _, err := migrate.Exec(database, "mysql", prior, migrate.Up); err != nil {
		t.Fatal(err)
	}
	for _, fixture := range []globalNameFixture{
		{id: "legacy-public", kind: "skill", visibility: "public", listing: "published", draft: "Official", status: 1, legacy: true},
		{id: "legacy-system", kind: "skill", visibility: "system", listing: "published", draft: "Official", status: 1, legacy: true},
		{id: "historical-private", kind: "skill", visibility: "private", listing: "published", draft: "Official", status: 1},
		{id: "modern-system", kind: "skill", visibility: "system", listing: "published", draft: "Official", status: 1},
	} {
		if err := insertGlobalNameFixture(t, database, fixture); err != nil {
			t.Fatal(err)
		}
	}
	source := migrationSourceThrough(t, "20261029-00-plugin-global-name-uniqueness.sql")
	if count, err := migrate.Exec(database, "mysql", source, migrate.Up); err != nil || count != 1 {
		t.Fatalf("upgrade: count=%d err=%v", count, err)
	}
	var count int
	if err := database.QueryRow(`SELECT COUNT(*) FROM plugins WHERE global_name_key IS NULL`).Scan(&count); err != nil || count != 3 {
		t.Fatalf("historical NULL and private exclusions: count=%d err=%v", count, err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM plugins WHERE global_name_key='official'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("active reservation: count=%d err=%v", count, err)
	}
	if count, err := migrate.ExecMax(database, "mysql", source, migrate.Down, 1); err != nil || count != 1 {
		t.Fatalf("down: count=%d err=%v", count, err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name='plugins' AND column_name='global_name_key'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("derived column remains: count=%d err=%v", count, err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM information_schema.statistics WHERE table_schema=DATABASE() AND table_name='plugins' AND index_name='uq_plugins_global_name'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("global index remains: count=%d err=%v", count, err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM plugins`).Scan(&count); err != nil || count != 4 {
		t.Fatalf("rollback changed records: count=%d err=%v", count, err)
	}
	if count, err := migrate.Exec(database, "mysql", source, migrate.Up); err != nil || count != 1 {
		t.Fatalf("re-upgrade: count=%d err=%v", count, err)
	}
}

func TestGlobalNameConstraintEffectiveReleaseAndExclusions(t *testing.T) {
	database := reviewDB(t)
	for _, kind := range []string{"skill", "expert", "expert_team"} {
		t.Run(kind, func(t *testing.T) {
			base := globalNameFixture{id: kind + "-market", kind: kind, visibility: "system", listing: "published", draft: "Unreleased draft", published: "Official", status: 1}
			if err := insertGlobalNameFixture(t, database, base); err != nil {
				t.Fatal(err)
			}
			var globalKey string
			if err := database.QueryRow(`SELECT global_name_key FROM plugins WHERE plugin_id=?`, base.id).Scan(&globalKey); err != nil || globalKey != "official" {
				t.Fatalf("effective published key=%q err=%v", globalKey, err)
			}
			duplicate := base
			duplicate.id, duplicate.visibility, duplicate.draft, duplicate.published = kind+"-duplicate", "public", " ＯＦＦＩＣＩＡＬ ", ""
			err := insertGlobalNameFixture(t, database, duplicate)
			var mysqlError *mysql.MySQLError
			if !errors.As(err, &mysqlError) || mysqlError.Number != 1062 || !strings.Contains(mysqlError.Message, "uq_plugins_global_name") {
				t.Fatalf("cross-Space normalized duplicate accepted: %v", err)
			}
			newMarket := duplicate
			newMarket.id, newMarket.draft = kind+"-different-release", base.draft
			if err := insertGlobalNameFixture(t, database, newMarket); err != nil {
				t.Fatalf("unreleased draft reserved globally: %v", err)
			}
			for _, exclusion := range []string{"private", "space", "draft", "delisted", "inactive", "deleted", "embedded", "connector"} {
				fixture := duplicate
				fixture.id = kind + "-" + exclusion
				switch exclusion {
				case "private", "space":
					fixture.visibility = exclusion
				case "draft", "delisted":
					fixture.listing = exclusion
				case "inactive":
					fixture.status = 0
				case "deleted":
					fixture.deleted = true
				case "embedded":
					fixture.embedded = true
				case "connector":
					fixture.kind = "connector"
				}
				if err := insertGlobalNameFixture(t, database, fixture); err != nil {
					t.Fatalf("%s occupied global namespace: %v", exclusion, err)
				}
				var key sql.NullString
				if err := database.QueryRow(`SELECT global_name_key FROM plugins WHERE plugin_id=?`, fixture.id).Scan(&key); err != nil || key.Valid {
					t.Fatalf("%s key=%+v err=%v", exclusion, key, err)
				}
			}
			// Existing private names remain valid, but widening one into the
			// occupied global namespace must be checked on UPDATE as well.
			_, err = database.Exec(`UPDATE plugins SET visibility='system' WHERE plugin_id=?`, kind+"-private")
			if !errors.As(err, &mysqlError) || mysqlError.Number != 1062 || !strings.Contains(mysqlError.Message, "uq_plugins_global_name") {
				t.Fatalf("conflicting global promotion accepted: %v", err)
			}
			if _, err := database.Exec(`UPDATE plugins SET listing_state='delisted' WHERE plugin_id=?`, base.id); err != nil {
				t.Fatal(err)
			}
			if _, err := database.Exec(`UPDATE plugins SET visibility='system' WHERE plugin_id=?`, kind+"-private"); err != nil {
				t.Fatalf("delisted name was not released globally: %v", err)
			}
		})
	}
}
