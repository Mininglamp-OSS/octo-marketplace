package db

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	pluginrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/plugin"
	pluginsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/plugin"
)

func TestAdminContainerImportIdempotencyReceiptsMySQL(t *testing.T) {
	database := reviewDB(t)
	svc := pluginsvc.New(pluginrepo.New(database)).WithVersionManagement(true).WithReferences(true)
	admin := pluginsvc.Caller{UID: "admin-1", Name: "Admin", IsSystemAdmin: true}
	ctx := context.Background()
	firstArchive := promotionArchive(t, map[string][]byte{"expert.json": []byte(`{"schema_version":"2","kind":"agent","name":"receipt expert","instruction":"do the work"}`)})
	secondArchive := promotionArchive(t, map[string][]byte{"expert.json": []byte(`{"schema_version":"2","kind":"agent","name":"receipt expert","instruction":"different work"}`)})
	p := pluginsvc.ContainerImportParams{SchemaVersion: "2", IdempotencyKey: "container-key-1", Archive: firstArchive}
	first, err := svc.AdminImportContainer(ctx, admin, p)
	if err != nil {
		t.Fatal(err)
	}
	if first.Plugin == nil || first.Resolved == nil || len(first.Resolved) != 0 {
		t.Fatalf("first import=%+v", first)
	}
	replay, err := svc.AdminImportContainer(ctx, admin, p)
	if err != nil || replay.Plugin == nil || replay.Plugin.ID != first.Plugin.ID {
		t.Fatalf("same-key replay=%+v err=%v", replay, err)
	}
	p.Archive = secondArchive
	if _, err = svc.AdminImportContainer(ctx, admin, p); !errors.Is(err, pluginsvc.ErrIdempotencyConflict) {
		t.Fatalf("same key different input err=%v", err)
	}
	p.Archive = firstArchive
	p.IdempotencyKey = "container-key-2"
	second, err := svc.AdminImportContainer(ctx, admin, p)
	if err != nil || second.Plugin == nil || second.Plugin.ID == first.Plugin.ID {
		t.Fatalf("new key did not create a new import: %+v err=%v", second, err)
	}
	var count int
	if err = database.QueryRow(`SELECT COUNT(*) FROM plugin_container_import_receipts`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("receipt count=%d err=%v", count, err)
	}
	if strings.TrimSpace(first.Plugin.ID) == "" {
		t.Fatal("empty plugin id")
	}
}

// A25 resolved[] reports the pins this import actually froze. A Skill release
// that lands after the import (or is already the target's head when an explicit
// pin is submitted) must never be substituted for the frozen pin.
func TestA25ResolvedReturnsFrozenPinsMySQL(t *testing.T) {
	database := reviewDB(t)
	repo := pluginrepo.New(database).WithVersionManagement(true).WithReferences(true)
	svc := pluginsvc.New(repo).WithVersionManagement(true).WithReferences(true)
	admin := pluginsvc.Caller{UID: "admin-1", Name: "Admin", IsSystemAdmin: true}
	ctx := context.Background()
	seedReleasedTarget(t, database, "skill", model.PluginTypeSkill, "system", "skill-v1", "1", "1.0.0")

	// Import while v1 is the target's current release: the omitted pin freezes to
	// v1 and the response reports it.
	omitted := promotionArchive(t, map[string][]byte{"expert.json": []byte(`{"schema_version":"2","kind":"agent","name":"a25 omitted pin","instruction":"work","skills":[{"id":"skill"}]}`)})
	p := pluginsvc.ContainerImportParams{SchemaVersion: "2", IdempotencyKey: "a25-frozen-pin", Archive: omitted}
	first, err := svc.AdminImportContainer(ctx, admin, p)
	if err != nil {
		t.Fatalf("import with omitted pin: %v", err)
	}
	assertA25Pin(t, first.Resolved, "skill", "skill-v1")

	// A newer Skill release lands. The stored receipt replay must still report the
	// pin frozen by the original import, not the new head.
	releaseNextVersion(t, database, "skill", "skill-v2", "2", "2.0.0")
	replay, err := svc.AdminImportContainer(ctx, admin, p)
	if err != nil {
		t.Fatalf("same-key replay: %v", err)
	}
	if replay.Plugin == nil || replay.Plugin.ID != first.Plugin.ID {
		t.Fatalf("replay plugin = %+v, want %s", replay.Plugin, first.Plugin.ID)
	}
	assertA25Pin(t, replay.Resolved, "skill", "skill-v1")

	// An explicit pin is author evidence and is preserved even when the target has
	// since published a newer head. Resolved must echo that pin, not the head.
	explicit := promotionArchive(t, map[string][]byte{"expert.json": []byte(`{"schema_version":"2","kind":"agent","name":"a25 explicit pin","instruction":"work","skills":[{"id":"skill","version_id":"skill-v1"}]}`)})
	second, err := svc.AdminImportContainer(ctx, admin, pluginsvc.ContainerImportParams{SchemaVersion: "2", IdempotencyKey: "a25-explicit-pin", Archive: explicit})
	if err != nil {
		t.Fatalf("import with explicit v1 pin: %v", err)
	}
	assertA25Pin(t, second.Resolved, "skill", "skill-v1")

	// The written relation and released snapshot carry the same pin the response
	// reported; the current head (v2) is nowhere in the graph.
	var relationPin string
	if err = database.QueryRow(`SELECT target_version_id FROM plugin_relations
		WHERE source_plugin_id=? AND target_plugin_id='skill' AND deleted_at IS NULL`, second.Plugin.ID).Scan(&relationPin); err != nil {
		t.Fatalf("read written relation pin: %v", err)
	}
	if relationPin != "skill-v1" {
		t.Fatalf("written relation pin = %q, want skill-v1", relationPin)
	}
	state, err := repo.GetVersionState(ctx, pluginrepo.Scope{CallerUID: admin.UID, Admin: true}, second.Plugin.ID)
	if err != nil || state.PublishedVersionID == nil {
		t.Fatalf("admin import version state: %+v err=%v", state, err)
	}
	if pin := snapshotTargetPin(t, database, *state.PublishedVersionID, "skill"); pin == nil || *pin != "skill-v1" {
		t.Fatalf("released snapshot pin = %v, want skill-v1", pin)
	}
}

// A Team import reports the frozen member Expert pin. The pinned member
// snapshot keeps its own Skill pin, so the imported Team graph selects
// member-v1 -> skill-v1 even after newer heads exist.
func TestA25ResolvedTeamMemberPinsMySQL(t *testing.T) {
	database := reviewDB(t)
	repo := pluginrepo.New(database).WithVersionManagement(true).WithReferences(true)
	svc := pluginsvc.New(repo).WithVersionManagement(true).WithReferences(true)
	admin := pluginsvc.Caller{UID: "admin-1", Name: "Admin", IsSystemAdmin: true}
	ctx := context.Background()
	seedReleasedTarget(t, database, "skill", model.PluginTypeSkill, "system", "skill-v1", "1", "1.0.0")
	skillPin := "skill-v1"
	memberRels := []model.PluginRelation{{ID: "m-skill", SourcePluginID: "member", TargetPluginID: "skill", Type: "expert_skill", TargetPluginType: "skill", TargetVersionID: &skillPin, Status: 1}}
	// The admin-created Team is a system row, so its referenced member must also
	// be system-visible (the same audience rule AdminCreate enforces).
	seed(t, database, seedPlugin{id: "member", typ: "expert", visibility: "system", listingState: "published", currentVersion: "1.0.0"})
	seedReleasedVersion(t, database, "member", "member-v1", "1.0.0", memberRels)
	if _, err := database.Exec(`UPDATE plugins SET published_version_id='member-v1' WHERE plugin_id='member'`); err != nil {
		t.Fatal(err)
	}
	// Newer heads exist before the import; the explicit member pin must win.
	releaseNextVersion(t, database, "member", "member-v2", "2", "2.0.0")
	releaseNextVersion(t, database, "skill", "skill-v2", "2", "2.0.0")

	archive := promotionArchive(t, map[string][]byte{"squad.json": []byte(`{"schema_version":"2","kind":"squad","name":"a25 team","instruction":"work","members":[{"id":"member","version_id":"member-v1","member_key":"lead","is_leader":true}]}`)})
	imported, err := svc.AdminImportContainer(ctx, admin, pluginsvc.ContainerImportParams{SchemaVersion: "2", IdempotencyKey: "a25-team-pin", Archive: archive})
	if err != nil {
		t.Fatalf("team import: %v", err)
	}
	assertA25Pin(t, imported.Resolved, "member", "member-v1")
	// The member's frozen Skill pin is carried by its released snapshot.
	if pin := snapshotTargetPin(t, database, "member-v1", "skill"); pin == nil || *pin != "skill-v1" {
		t.Fatalf("pinned member snapshot skill pin = %v, want skill-v1", pin)
	}
	state, err := repo.GetVersionState(ctx, pluginrepo.Scope{CallerUID: admin.UID, Admin: true}, imported.Plugin.ID)
	if err != nil || state.PublishedVersionID == nil {
		t.Fatalf("team version state: %+v err=%v", state, err)
	}
	if pin := snapshotTargetPin(t, database, *state.PublishedVersionID, "member"); pin == nil || *pin != "member-v1" {
		t.Fatalf("released team snapshot member pin = %v, want member-v1", pin)
	}
}

// assertA25Pin checks the single resolved[] entry for targetID carries wantPin.
func assertA25Pin(t *testing.T, resolved []pluginsvc.ResolvedDependency, targetID, wantPin string) {
	t.Helper()
	if len(resolved) != 1 {
		t.Fatalf("resolved = %+v, want one entry", resolved)
	}
	got := resolved[0]
	if got.TargetPluginID != targetID || got.TargetVersionID == nil || *got.TargetVersionID != wantPin {
		t.Fatalf("resolved = %+v, want %s@%s", got, targetID, wantPin)
	}
}
