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

func TestSnapshotArtifactInventoryIsDeterministicAndComplete(t *testing.T) {
	space := "a"
	p := model.Plugin{SpaceID: &space, Package: json.RawMessage(`{"attachments":[{"path":"scripts/run.sh","content_type":"storage","content_size":3,"content_hash":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},{"path":"SKILL.md","content_type":"raw","raw_content":"hello"}]}`), AttachmentKeys: json.RawMessage(`{"scripts/run.sh":"plugins/a/attachments/versions/hash"}`)}
	raw, err := snapshotArtifacts(p)
	if err != nil {
		t.Fatal(err)
	}
	var files []model.PluginVersionArtifact
	if err = json.Unmarshal(raw, &files); err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 || files[0].Path != "SKILL.md" || files[0].Size != 5 || files[0].Digest != "sha256:2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824" || files[1].ObjectKey != "plugins/a/attachments/versions/hash" {
		t.Fatalf("inventory=%s", raw)
	}
}

func TestSnapshotArtifactsRefusesUnknownBytesOrUnsafePaths(t *testing.T) {
	for _, pkg := range []string{
		`{"attachments":[{"path":"../file","content_type":"raw"}]}`,
		`{"attachments":[{"path":"SKILL.md","content_type":"raw"},{"path":"SKILL.md","content_type":"raw"}]}`,
		`{"attachments":[{"path":"file","content_type":"storage","content_hash":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}]}`,
		`{"attachments":[{"path":"file","content_type":"storage","content_hash":"sha256:bad"}]}`,
	} {
		if _, err := snapshotArtifacts(model.Plugin{Package: json.RawMessage(pkg)}); err == nil {
			t.Fatalf("accepted %s", pkg)
		}
	}
}

func TestSavedVersionReadsFailClosedAcrossOwnersSpacesAndResources(t *testing.T) {
	for _, tc := range []struct{ pluginID, versionID, uid, space string }{
		{"plugin", "version", "other-owner", "space"},
		{"plugin", "version", "owner", "other-space"},
		{"wrong-plugin", "version", "owner", "space"},
		{"plugin", "wrong-version", "owner", "space"},
	} {
		db, mock, _ := sqlmock.New()
		r := New(db)
		mock.ExpectQuery(`SELECT v.version_id.*p.owner_uid=\? AND p.space_id=\?`).WithArgs(tc.pluginID, tc.versionID, tc.uid, tc.space).WillReturnRows(sqlmock.NewRows([]string{"version_id"}))
		if _, err := r.GetSavedVersion(context.Background(), Scope{CallerUID: tc.uid, SpaceID: tc.space}, tc.pluginID, tc.versionID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("read err=%v", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
		db.Close()
	}
}

func TestConnectorSnapshotAdvancesLabelWithoutCreatingVersion(t *testing.T) {
	db, mock, _ := sqlmock.New()
	defer db.Close()
	mock.ExpectBegin()
	tx, _ := db.Begin()
	label := "2.0.0"
	mock.ExpectExec(`UPDATE plugins SET current_version=\?,updated_at=\? WHERE plugin_id=\? AND deleted_at IS NULL`).
		WithArgs(label, nil, "connector").
		WillReturnResult(sqlmock.NewResult(0, 1))
	if id, err := snapshotVersion(context.Background(), tx, func() string { t.Fatal("connector minted version ID"); return "" }, nil, model.Plugin{ID: "connector", Type: model.PluginTypeConnector, CurrentVersion: &label}, nil, nil, "owner"); err != nil || id != "" {
		t.Fatalf("version=%q err=%v", id, err)
	}
	mock.ExpectRollback()
	_ = tx.Rollback()
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSnapshotArtifactsRejectsNonFrozenOrCrossSpaceKeys(t *testing.T) {
	space := "a"
	digest := "sha256:" + strings.Repeat("a", 64)
	stored := func(pkg, keys string) model.Plugin {
		return model.Plugin{SpaceID: &space, Package: json.RawMessage(pkg), AttachmentKeys: json.RawMessage(keys)}
	}
	storageAtt := `{"attachments":[{"path":"assets/a.bin","content_type":"storage","content_size":1,"content_hash":"` + digest + `"}]}`
	for name, p := range map[string]model.Plugin{
		"mutable upload key":        stored(storageAtt, `{"assets/a.bin":"plugins/a/attachments/upload.bin"}`),
		"cross-space immutable key": stored(storageAtt, `{"assets/a.bin":"plugins/b/attachments/versions/hash"}`),
		"legacy ref without inventory": stored(
			`{"attachments":[{"path":"skill/ref.json","content_type":"raw","raw_content":"{\"object_key\":\"plugins/a/attachments/legacy.zip\"}"}]}`,
			`{}`),
	} {
		if _, err := snapshotArtifacts(p); !errors.Is(err, ErrLegacySnapshotArtifacts) {
			t.Fatalf("%s: err=%v", name, err)
		}
	}
}

func TestVersionStateDoesNotInferPublicationFromCurrent(t *testing.T) {
	db, mock, _ := sqlmock.New()
	defer db.Close()
	mock.ExpectQuery(`SELECT p.current_version_id,p.published_version_id.*p.space_id = \?.*p.owner_uid = \?`).WithArgs("plugin", "space", "owner").WillReturnRows(sqlmock.NewRows([]string{"current_version_id", "published_version_id"}).AddRow("saved-version", nil))
	state, err := New(db).GetVersionState(context.Background(), Scope{CallerUID: "owner", SpaceID: "space"}, "plugin")
	if err != nil {
		t.Fatal(err)
	}
	if state.PublishedVersionID != nil || state.CurrentVersionID == nil || !strings.HasPrefix(*state.CurrentVersionID, "saved") {
		t.Fatalf("state=%+v", state)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
