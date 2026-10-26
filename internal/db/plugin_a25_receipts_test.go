package db

import (
	"context"
	"errors"
	"strings"
	"testing"

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
