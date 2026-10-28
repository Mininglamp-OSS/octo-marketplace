package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

func platformSnapshotFixture(t *testing.T, p model.Plugin, field, key, digest string, size int64) model.Plugin {
	t.Helper()
	var attachment map[string]any
	if field == "storage" {
		attachment = map[string]any{"path": "assets/a.bin", "content_type": "storage", "content_size": size, "content_hash": digest}
		p.AttachmentKeys, _ = json.Marshal(map[string]string{"assets/a.bin": key})
	} else {
		ref, _ := json.Marshal(map[string]any{field: key, field + "_hash": digest, field + "_size": size})
		attachment = map[string]any{"path": "skill/ref.json", "content_type": "raw", "raw_content": string(ref)}
	}
	p.Package, _ = json.Marshal(map[string]any{"attachments": []any{attachment}})
	return p
}

func TestPlatformSnapshotArtifactOwnershipAndInventory(t *testing.T) {
	space := "owned"
	system := model.Plugin{ID: "skill-a", Visibility: model.PluginVisibilitySystem}
	historical := system
	historical.SpaceID = &space
	tenant := model.Plugin{ID: "skill-a", Visibility: model.PluginVisibilitySpace, SpaceID: &space}
	digest := "sha256:" + strings.Repeat("a", 64)
	platformKey := "platform/plugins/skill-a/attachments/versions/" + strings.Repeat("a", 64)
	for _, field := range []string{"storage", "object_key", "zip_object_key", "file_url"} {
		t.Run(field, func(t *testing.T) {
			for _, tc := range []struct {
				name      string
				p         model.Plugin
				key, hash string
				size      int64
				allowed   bool
			}{
				{"platform", system, platformKey, digest, 3, true},
				{"historical platform", historical, platformKey, digest, 0, true},
				{"historical own Space", historical, "plugins/owned/attachments/versions/legacy", digest, 3, true},
				{"tenant own Space", tenant, "plugins/owned/attachments/versions/legacy", digest, 3, true},
				{"foreign resource", system, strings.Replace(platformKey, "skill-a/", "skill-b/", 1), digest, 3, false},
				{"resource prefix collision", system, strings.Replace(platformKey, "skill-a/", "skill-a-other/", 1), digest, 3, false},
				{"foreign Space", historical, "plugins/foreign/attachments/versions/hash", digest, 3, false},
				{"Space absent", system, "plugins/owned/attachments/versions/hash", digest, 3, false},
				{"tenant platform", tenant, platformKey, digest, 3, false},
				{"mutable", system, "platform/plugins/skill-a/attachments/upload.bin", digest, 3, false},
				{"invalid key digest", system, strings.TrimSuffix(platformKey, "a"), digest, 3, false},
				{"nested key", system, platformKey + "/child", digest, 3, false},
				{"traversal", system, strings.Replace(platformKey, "/versions/", "/x/../versions/", 1), digest, 3, false},
				{"query", system, platformKey + "?q=1", digest, 3, false},
				{"negative size", system, platformKey, digest, -1, false},
				{"missing digest", system, platformKey, "", 3, false},
				{"nonhex digest", system, platformKey, "sha256:" + strings.Repeat("z", 64), 3, false},
			} {
				t.Run(tc.name, func(t *testing.T) {
					raw, err := snapshotArtifacts(platformSnapshotFixture(t, tc.p, field, tc.key, tc.hash, tc.size))
					if !tc.allowed {
						if !errors.Is(err, ErrLegacySnapshotArtifacts) {
							t.Fatalf("expected closed failure, got %v", err)
						}
						return
					}
					if err != nil {
						t.Fatal(err)
					}
					var inventory []model.PluginVersionArtifact
					if err := json.Unmarshal(raw, &inventory); err != nil {
						t.Fatal(err)
					}
					found := false
					for _, item := range inventory {
						if item.ObjectKey == tc.key {
							found = item.Size == tc.size && item.Digest == tc.hash
						}
					}
					if !found {
						t.Fatalf("missing storage inventory: %s", raw)
					}
				})
			}
		})
	}
}

// Exercise the actual version persistence helper, including its stored inventory
// and current-version pointer, without creating a database or calling MySQL.
func TestPlatformSnapshotVersionPersistence(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	key := "platform/plugins/skill-a/attachments/versions/" + strings.Repeat("a", 64)
	p := platformSnapshotFixture(t, model.Plugin{ID: "skill-a", Type: model.PluginTypeSkill, Visibility: model.PluginVisibilitySystem, Manifest: json.RawMessage(`{}`)}, "storage", key, digest, 3)
	inventory, _ := json.Marshal([]model.PluginVersionArtifact{{Path: "assets/a.bin", ContentType: "storage", ObjectKey: key, Size: 3, Digest: digest}})
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectBegin()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM plugin_versions WHERE plugin_id=\?`).WithArgs(p.ID).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectExec(`INSERT INTO plugin_versions`).WithArgs("version-a", p.ID, "1", string(p.Manifest), string(p.Package), string(p.AttachmentKeys), "", "", "[]", nil, "admin", nil, "1.0.0", model.PluginSnapshotSchemaVersion, string(inventory)).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(`UPDATE plugins SET current_version_id=\?,current_version=\?,updated_at=\? WHERE plugin_id=\?`).WithArgs("version-a", "1.0.0", nil, p.ID).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	id, err := snapshotVersion(context.Background(), tx, func() string { return "version-a" }, nil, p, nil, nil, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if id != "version-a" {
		t.Fatalf("version=%q", id)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
