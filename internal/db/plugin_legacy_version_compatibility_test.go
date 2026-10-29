package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	pluginrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/plugin"
	pluginsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/plugin"
)

// C06 uses a disposable reviewDB, never the running integration business DB.
// Older accepted labels are seeded as historical rows, then all reads/edits use
// the real service and repository. New malformed labels stay forbidden.
func TestLegacyVersionLabelsRemainEditableWithoutRelaxingNewWrites(t *testing.T) {
	database := reviewDB(t)
	repo := pluginrepo.New(database)
	svc := pluginsvc.New(repo).WithVersionManagement(true)
	ctx, owner := context.Background(), managedOwner()
	for i, label := range []string{"1.0", "v1.2.3", "2.0.0-beta.1"} {
		name := fmt.Sprintf("Legacy Skill %d", i)
		req := managedSkillWrite("1.0.0", name, model.PluginVisibilityPrivate)
		created, err := svc.Create(ctx, owner, req)
		if err != nil {
			t.Fatal(err)
		}
		id, versionID := created.Plugin.ID, *created.Plugin.CurrentVersionID
		// Fixture setup reconstructs the exact legacy stored head; the production
		// create/import endpoints must continue rejecting these labels.
		if _, err = database.Exec(`UPDATE plugins SET current_version=? WHERE plugin_id=?`, label, id); err != nil {
			t.Fatal(err)
		}
		if _, err = database.Exec(`UPDATE plugin_versions SET declared_version=? WHERE plugin_id=? AND version_id=?`, label, id, versionID); err != nil {
			t.Fatal(err)
		}
		draft, err := svc.CurrentDetail(ctx, owner, id)
		if err != nil || *draft.Plugin.CurrentVersion != label {
			t.Fatalf("legacy read=%+v %v", draft, err)
		}
		req.Version = label
		req.Manifest = json.RawMessage(fmt.Sprintf(`{"$schema":"cowork-plugin-manifest-2.0.json","plugin_name":%q,"plugin_type":"skill","name":"legacy-skill","description":"edited legacy description","labels":[],"examples":[]}`, name))
		edited, err := svc.Update(ctx, owner, id, req)
		if err != nil {
			t.Fatalf("unchanged legacy %q: %v", label, err)
		}
		if *edited.Plugin.CurrentVersion != label {
			t.Fatal("legacy label changed unexpectedly")
		}
		for _, invalid := range []string{"new-invalid", "3.0"} {
			req.Version = invalid
			if _, err = svc.Update(ctx, owner, id, req); !errors.Is(err, pluginsvc.ErrInvalidRequest) && !errors.Is(err, pluginsvc.ErrVersionRegressed) {
				t.Fatalf("new malformed label %q accepted: %v", invalid, err)
			}
		}
		req = managedSkillWrite(label, fmt.Sprintf("New invalid Skill %d", i), model.PluginVisibilityPrivate)
		if _, err = svc.Create(ctx, owner, req); !errors.Is(err, pluginsvc.ErrInvalidRequest) {
			t.Fatalf("legacy create accepted %q: %v", label, err)
		}
		if _, err = svc.CurrentDetail(ctx, pluginsvc.Caller{UID: owner.UID, SpaceID: "other-space"}, id); !errors.Is(err, pluginsvc.ErrNotFound) {
			t.Fatalf("legacy cross-Space=%v", err)
		}
	}
}
