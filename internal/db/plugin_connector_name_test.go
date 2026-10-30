package db

import (
	"context"
	"errors"
	"testing"

	pluginrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/plugin"
	"github.com/go-sql-driver/mysql"
	migrate "github.com/rubenv/sql-migrate"
)

func TestConnectorNameLifecycle(t *testing.T) {
	database := reviewDB(t)
	repo := pluginrepo.New(database)
	ctx := context.Background()
	sc := pluginrepo.Scope{CallerUID: "owner", SpaceID: "space-a"}
	create := func(id, name, kind string) error {
		_, err := repo.Create(ctx, sc, uploadNameMutation(id, name, kind, sc.SpaceID))
		return err
	}
	if err := create("connector-one", "Alpha", "connector"); err != nil {
		t.Fatal(err)
	}
	if err := create("connector-two", "Beta", "connector"); err != nil {
		t.Fatal(err)
	}
	// Another plugin type has its own namespace.
	if err := create("expert-one", "Alpha", "expert"); err != nil {
		t.Fatal(err)
	}
	p, err := repo.Get(ctx, sc, "connector-two")
	if err != nil {
		t.Fatal(err)
	}
	p.Name = " ＡＬＰＨＡ "
	if _, err := repo.Update(ctx, sc, pluginrepo.Mutation{Plugin: *p, OperatorID: "owner"}); !errors.Is(err, pluginrepo.ErrNameTaken) {
		t.Fatalf("rename collision: %v", err)
	}
	p.Name = " beta "
	if _, err := repo.Update(ctx, sc, pluginrepo.Mutation{Plugin: *p, OperatorID: "owner"}); err != nil {
		t.Fatalf("self rename: %v", err)
	}
	if _, err := database.Exec(`UPDATE plugins SET listing_state='delisted' WHERE plugin_id='connector-one'`); err != nil {
		t.Fatal(err)
	}
	if err := create("connector-three", "alpha", "connector"); !errors.Is(err, pluginrepo.ErrNameTaken) {
		t.Fatalf("delisted name was freed: %v", err)
	}
	// Even direct SQL writes are covered by the unique index/reservation trigger.
	if _, err := database.Exec(`UPDATE plugins SET plugin_name='Alpha',name_key='alpha' WHERE plugin_id='connector-two'`); err == nil {
		t.Fatal("database accepted a duplicate connector name")
	}
}

func TestConnectorNameMigrationPreservesLegacyRows(t *testing.T) {
	cfg, err := mysql.ParseDSN(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	cfg.MultiStatements = false
	t.Setenv("TEST_MYSQL_DSN", cfg.FormatDSN())
	database := isolatedTestDB(t)
	before := migrationSourceThrough(t, "20261027-02-plugin-visibility-reviews.sql")
	if _, err := migrate.Exec(database, "mysql", before, migrate.Up); err != nil {
		t.Fatal(err)
	}
	// Historical duplicates have no keys. Do not silently rename them or prevent
	// startup; runtime writes must still detect their original names.
	seed(t, database, seedPlugin{id: "legacy-connector-a", typ: "connector", visibility: "private", manifest: `{"plugin_name":"Legacy"}`})
	seed(t, database, seedPlugin{id: "legacy-connector-b", typ: "connector", visibility: "private", manifest: `{"plugin_name":"Legacy"}`})
	if _, err := database.Exec(`UPDATE plugins SET plugin_name='Legacy' WHERE plugin_type='connector'`); err != nil {
		t.Fatal(err)
	}
	// This startup path runs with multiStatements disabled in the test DSN.
	if _, err := RunMigrations(database); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := database.QueryRow(`SELECT COUNT(*) FROM plugins WHERE plugin_type='connector' AND plugin_name='Legacy' AND name_key IS NULL`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("legacy rows changed: %d %v", count, err)
	}
	repo := pluginrepo.New(database)
	_, err = repo.Create(context.Background(), pluginrepo.Scope{CallerUID: "owner", SpaceID: "space-a"}, uploadNameMutation("legacy-collision", " legacy ", "connector", "space-a"))
	if !errors.Is(err, pluginrepo.ErrNameTaken) {
		t.Fatalf("legacy duplicate bypass: %v", err)
	}
}
