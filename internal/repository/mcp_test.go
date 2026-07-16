//go:build integration

// This file holds DB-backed concurrency tests for the uniqueness lock. They are
// gated behind the `integration` build tag AND the MARKETPLACE_TEST_MYSQL_DSN
// environment variable so the default `go test ./...` never needs an external
// service (AGENTS.md "Tests should not require external services unless marked
// as integration").
//
// Run against the docker-compose MySQL:
//
//	docker compose up -d mysql
//	MARKETPLACE_TEST_MYSQL_DSN='root:root@tcp(127.0.0.1:3306)/marketplace?parseTime=true&multiStatements=true' \
//	  go test -tags integration ./internal/repository/ -run TestConcurrent -v
//
// The suite proves the SELECT ... FOR UPDATE recipe in mcp.go actually blocks
// concurrent inserts of the same (owner_uid, space_id, name) triple — the
// property the fake-store unit test in the service package cannot demonstrate.
package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	marketdb "github.com/Mininglamp-OSS/octo-marketplace/internal/db"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/id"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

// openTestDB opens the DSN from MARKETPLACE_TEST_MYSQL_DSN, applies migrations,
// and returns a ready handle. The test is skipped when the DSN is unset so the
// tagged suite still no-ops on a machine without MySQL.
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("MARKETPLACE_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("MARKETPLACE_TEST_MYSQL_DSN not set; skipping DB-backed concurrency test")
	}
	database, err := marketdb.Open(dsn)
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	if _, err := marketdb.RunMigrations(database); err != nil {
		t.Fatalf("run migrations: %v", err)
	}
	return database
}

// cleanTuple removes any prior rows for the (owner, space, name) triple so the
// test starts from a known-empty state and can be re-run.
func cleanTuple(t *testing.T, database *sql.DB, owner, space, name string) {
	t.Helper()
	if _, err := database.ExecContext(context.Background(),
		`DELETE FROM mcp_servers WHERE owner_uid = ? AND space_id <=> ? AND name = ?`,
		owner, nullableSpace(space), name,
	); err != nil {
		t.Fatalf("clean tuple: %v", err)
	}
}

func countLive(t *testing.T, database *sql.DB, owner, space, name string) int {
	t.Helper()
	var n int
	if err := database.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM mcp_servers
		  WHERE owner_uid = ? AND space_id <=> ? AND name = ? AND deleted_at IS NULL`,
		owner, nullableSpace(space), name,
	).Scan(&n); err != nil {
		t.Fatalf("count live: %v", err)
	}
	return n
}

func newTestMCP(name, owner, space string) *model.MCP {
	now := time.Now()
	return &model.MCP{
		ID:   id.New(),
		Name: name,
		// Slug empty → generated column slug_live = NULL → excluded from the
		// per-Space slug UNIQUE index. Tests that specifically exercise slug
		// uniqueness set Slug on the returned struct before Create.
		Slug:       "",
		Category:   "dev",
		Visibility: model.VisibilityPrivate,
		OwnerUID:   owner,
		SpaceID:    space,
		Transport:  model.TransportStdio,
		Connection: model.Connection{Command: "npx"},
		CreatedAt:  now,
		UpdatedAt:  now,
	}
}

// TestConcurrentCreateSameName fires N Create calls for the identical
// (owner_uid, space_id, name) triple at once and asserts exactly one wins while
// the rest get ErrNameTaken, and that the table holds exactly one live row.
// This is the positive concurrency proof the migration comment and doc §7
// require (no check-then-insert double-insert).
func TestConcurrentCreateSameName(t *testing.T) {
	database := openTestDB(t)

	const (
		owner = "owner-concurrency"
		space = "space-concurrency"
		name  = "Concurrent MCP"
		n     = 16
	)
	cleanTuple(t, database, owner, space, name)
	t.Cleanup(func() { cleanTuple(t, database, owner, space, name) })

	repo := New(database)

	var (
		start   = make(chan struct{})
		wg      sync.WaitGroup
		mu      sync.Mutex
		success int
		taken   int
		others  []error
	)
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			m := newTestMCP(name, owner, space) // distinct PK, same uniqueness triple
			<-start                             // barrier: release all goroutines together
			err := repo.Create(context.Background(), m)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				success++
			case errors.Is(err, ErrNameTaken):
				taken++
			default:
				others = append(others, err)
			}
		}()
	}
	close(start)
	wg.Wait()

	if len(others) > 0 {
		t.Fatalf("unexpected errors from concurrent Create: %v", others)
	}
	if success != 1 {
		t.Fatalf("success=%d want=1 (exactly one create must win)", success)
	}
	if taken != n-1 {
		t.Fatalf("name_taken=%d want=%d", taken, n-1)
	}
	if live := countLive(t, database, owner, space, name); live != 1 {
		t.Fatalf("live rows=%d want=1 (row lock failed to prevent double-insert)", live)
	}
}

// TestConcurrentRenameCollision seeds two live records with different names,
// then fires N Update calls that all rename the second record onto the first's
// name. Exactly zero renames may succeed (the target name is already live and
// owned by the same caller), every attempt must return ErrNameTaken, and the
// original name must remain single-owner.
func TestConcurrentRenameCollision(t *testing.T) {
	database := openTestDB(t)

	const (
		owner    = "owner-rename"
		space    = "space-rename"
		nameA    = "Existing MCP"
		nameB    = "Rename Source MCP"
		attempts = 12
	)
	cleanTuple(t, database, owner, space, nameA)
	cleanTuple(t, database, owner, space, nameB)
	t.Cleanup(func() {
		cleanTuple(t, database, owner, space, nameA)
		cleanTuple(t, database, owner, space, nameB)
	})

	repo := New(database)
	ctx := context.Background()

	if err := repo.Create(ctx, newTestMCP(nameA, owner, space)); err != nil {
		t.Fatalf("seed A: %v", err)
	}
	source := newTestMCP(nameB, owner, space)
	if err := repo.Create(ctx, source); err != nil {
		t.Fatalf("seed B: %v", err)
	}

	var (
		start   = make(chan struct{})
		wg      sync.WaitGroup
		mu      sync.Mutex
		success int
		taken   int
		others  []error
	)
	wg.Add(attempts)
	for i := 0; i < attempts; i++ {
		go func() {
			defer wg.Done()
			m := *source
			m.Name = nameA // collide with the live nameA row
			m.UpdatedAt = time.Now()
			<-start
			err := repo.Update(ctx, &m)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				success++
			case errors.Is(err, ErrNameTaken):
				taken++
			default:
				others = append(others, err)
			}
		}()
	}
	close(start)
	wg.Wait()

	if len(others) > 0 {
		t.Fatalf("unexpected errors from concurrent Update: %v", others)
	}
	if success != 0 {
		t.Fatalf("rename success=%d want=0 (target name is live)", success)
	}
	if taken != attempts {
		t.Fatalf("name_taken=%d want=%d", taken, attempts)
	}
	if live := countLive(t, database, owner, space, nameA); live != 1 {
		t.Fatalf("live rows for nameA=%d want=1", live)
	}
}

// TestConcurrentCreateSameSlug fires N Create calls that DIFFER by name+id
// but share the same (space_id, slug) tuple, and asserts the DB UNIQUE
// index (migration 03) admits exactly one winner. The rest must fail with
// ErrSlugTaken — proof that the constraint fires and mapDupKey routes the
// error to the slug family (not the older name family). Same recipe as
// TestConcurrentCreateSameName, but the collision axis is slug not name.
func TestConcurrentCreateSameSlug(t *testing.T) {
	ctx := context.Background()
	database := openTestDB(t)
	defer database.Close()

	repo := New(database)
	space := "space-slug-race"
	slug := "shared-slug"

	const attempts = 12
	var (
		mu      sync.Mutex
		wg      sync.WaitGroup
		start   = make(chan struct{})
		success int
		taken   int
		others  []error
	)
	for i := 0; i < attempts; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			m := newTestMCP(fmt.Sprintf("name-%d", i), fmt.Sprintf("owner-%d", i), space)
			m.Slug = slug
			<-start
			err := repo.Create(ctx, m)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				success++
			case errors.Is(err, ErrSlugTaken):
				taken++
			default:
				others = append(others, err)
			}
		}()
	}
	close(start)
	wg.Wait()

	if len(others) > 0 {
		t.Fatalf("unexpected errors: %v", others)
	}
	if success != 1 {
		t.Fatalf("wanted exactly one winner, got success=%d", success)
	}
	if taken != attempts-1 {
		t.Fatalf("slug_taken=%d want=%d", taken, attempts-1)
	}
	// One live row with this slug in this Space.
	var n int
	if err := database.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM mcp_servers WHERE space_id = ? AND slug = ? AND deleted_at IS NULL`,
		space, slug,
	).Scan(&n); err != nil {
		t.Fatalf("count live: %v", err)
	}
	if n != 1 {
		t.Fatalf("live rows with slug=%d want=1", n)
	}
}
