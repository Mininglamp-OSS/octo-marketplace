package db

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	pluginrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/plugin"
	pluginsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/plugin"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/storage"
	migrationsql "github.com/Mininglamp-OSS/octo-marketplace/migrations/sql"
	migrate "github.com/rubenv/sql-migrate"
)

func TestPluginVersionFoundationMigrationAndReleaseOwnershipMySQL(t *testing.T) {
	db := isolatedTestDB(t)
	// Exercise historical reversibility before the immutable audit boundary.
	source := migrationSourceThrough(t, "20261024-02-plugin-install-attempts.sql")
	migrations, err := source.FindMigrations()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = migrate.Exec(db, "mysql", migrate.MemoryMigrationSource{Migrations: migrations[:len(migrations)-migrationTailCount(t, source, "20261023-00-plugin-version-foundation.sql")]}, migrate.Up); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"first", "second"} {
		if _, err = db.Exec(`INSERT INTO plugins (plugin_id,plugin_name,plugin_type,tags_json,owner_uid,space_id,visibility,manifest_json,plugin_json,manifest_hash,plugin_hash,created_at,updated_at) VALUES (?,?,'skill',JSON_ARRAY(),'owner','space','private',JSON_OBJECT(),JSON_OBJECT(),REPEAT('a',71),REPEAT('b',71),NOW(3),NOW(3))`, id, id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = db.Exec(`INSERT INTO plugin_versions (version_id,plugin_id,version,manifest_json,plugin_json,manifest_hash,plugin_hash,relations_json,created_by,created_at) VALUES ('old','first','1',JSON_OBJECT(),JSON_OBJECT(),REPEAT('a',71),REPEAT('b',71),JSON_ARRAY(),'owner',NOW(3))`); err != nil {
		t.Fatal(err)
	}
	if _, err = migrate.Exec(db, "mysql", source, migrate.Up); err != nil {
		t.Fatal(err)
	}
	var n int
	if err = db.QueryRow(`SELECT COUNT(*) FROM plugins WHERE published_version_id IS NOT NULL`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("fabricated historical release: %d %v", n, err)
	}
	if err = db.QueryRow(`SELECT COUNT(*) FROM plugin_versions WHERE version_id='old' AND artifacts_json IS NULL AND declared_version IS NULL AND snapshot_schema_version IS NULL`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("rewrote historical snapshot: %d %v", n, err)
	}
	if _, err = db.Exec(`INSERT INTO plugin_version_releases VALUES ('second','old','1.0.0','owner',NOW(3))`); err == nil {
		t.Fatal("cross-resource release accepted")
	}
	if _, err = db.Exec(`INSERT INTO plugin_version_releases VALUES ('first','old','1.0.0','owner',NOW(3))`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE plugins SET published_version_id='old' WHERE plugin_id='second'`); err == nil {
		t.Fatal("cross-resource published pointer accepted")
	}
	if _, err = db.Exec(`UPDATE plugins SET published_version_id='old' WHERE plugin_id='first'`); err != nil {
		t.Fatal(err)
	}
	if _, err = migrate.ExecMax(db, "mysql", source, migrate.Down, migrationTailCount(t, source, "20261023-00-plugin-version-foundation.sql")); err != nil {
		t.Fatal(err)
	}
	if _, err = migrate.Exec(db, "mysql", source, migrate.Up); err != nil {
		t.Fatal(err)
	}
}

func TestHistoricalSavedFilesSurviveReplacementAndSoftDeleteMySQL(t *testing.T) {
	db := isolatedTestDB(t)
	source := &migrate.EmbedFileSystemMigrationSource{FileSystem: migrationsql.FS, Root: "."}
	if _, err := migrate.Exec(db, "mysql", source, migrate.Up); err != nil {
		t.Fatal(err)
	}
	repo := pluginrepo.New(db)
	ctx := context.Background()
	sc := pluginrepo.Scope{CallerUID: "owner", SpaceID: "space"}
	blobs := storage.NewLocal(t.TempDir(), "http://localhost")
	svc := pluginsvc.New(repo, blobs)
	caller := pluginsvc.Caller{UID: "owner", Name: "Owner", SpaceID: "space"}
	req := pluginsvc.WriteRequest{Name: "History", Type: model.PluginTypeSkill, Visibility: model.PluginVisibilityPrivate, Version: "1.0.0", Tags: json.RawMessage(`[]`), Manifest: json.RawMessage(`{"$schema":"cowork-plugin-manifest-2.0.json","plugin_name":"History","plugin_type":"skill","name":"history","description":"d","labels":[],"examples":[]}`), Package: versionTestPackage("old")}
	data := "old"
	if err := blobs.PutObject(ctx, "plugins/space/attachments/upload", strings.NewReader(data), int64(len(data)), ""); err != nil {
		t.Fatal(err)
	}
	detail, err := svc.Create(ctx, caller, req)
	if err != nil {
		t.Fatal(err)
	}
	id := detail.Plugin.ID
	v1 := *detail.Plugin.CurrentVersionID
	saved, err := repo.GetSavedVersion(ctx, sc, id, v1)
	if err != nil {
		t.Fatal(err)
	}
	var files []model.PluginVersionArtifact
	if err = json.Unmarshal(saved.Artifacts, &files); err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 || saved.SnapshotSchemaVersion == nil || saved.DeclaredVersion == nil || *saved.DeclaredVersion != "1.0.0" {
		t.Fatalf("incomplete saved version: %+v", saved)
	}
	data = "new"
	req.Version = "2.0.0"
	req.Package = versionTestPackage(data)
	if err = blobs.PutObject(ctx, "plugins/space/attachments/upload", strings.NewReader(data), 3, ""); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.Update(ctx, caller, id, req); err != nil {
		t.Fatal(err)
	}
	if _, err = repo.GetSavedVersion(ctx, pluginrepo.Scope{CallerUID: "other", SpaceID: "elsewhere"}, id, v1); !errors.Is(err, pluginrepo.ErrNotFound) {
		t.Fatalf("cross-Space read=%v", err)
	}
	if _, err = repo.GetSavedVersion(ctx, pluginrepo.Scope{CallerUID: "other", SpaceID: "space"}, id, v1); !errors.Is(err, pluginrepo.ErrNotFound) {
		t.Fatalf("other author read=%v", err)
	}
	if err = blobs.DeleteObject(ctx, "plugins/space/attachments/upload"); err != nil {
		t.Fatal(err)
	}
	if err = svc.Delete(ctx, caller, id); err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if f.ContentType != "storage" {
			continue
		}
		body, err := blobs.GetObject(ctx, f.ObjectKey)
		if err != nil {
			t.Fatal(err)
		}
		got, _ := io.ReadAll(body)
		body.Close()
		if string(got) != "old" {
			t.Fatalf("lost original file: %s", got)
		}
	}
	var count int
	if err = db.QueryRow(`SELECT COUNT(*) FROM plugin_versions WHERE plugin_id=?`, id).Scan(&count); err != nil || count != 2 {
		t.Fatalf("lost historical rows: %d %v", count, err)
	}
}

func versionTestDigest(data string) string {
	sum := sha256.Sum256([]byte(data))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func versionTestPackage(data string) json.RawMessage {
	raw, _ := json.Marshal(map[string]any{
		"$schema": "cowork-plugin-package-2.0.json",
		"attachments": []map[string]any{
			{"path": "SKILL.md", "content_type": "raw", "mime_type": "text/markdown", "raw_content": "# History"},
			{"path": "file.bin", "content_type": "storage", "mime_type": "application/octet-stream", "content_size": len(data), "content_hash": versionTestDigest(data), "storage_uri": "plugins/space/attachments/upload"},
		}})
	return raw
}
