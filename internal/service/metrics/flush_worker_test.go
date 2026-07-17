package metrics

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
)

// mockRepo is a test double for MetricsRepository.
type mockRepo struct {
	calls     []upsertCall
	failN     int // fail the first N calls
	callCount int
}

type upsertCall struct {
	ResourceType  string
	ResourceID    string
	ViewDelta     int64
	DownloadDelta int64
	InstallDelta  int64
}

func (m *mockRepo) UpsertCounts(_ context.Context, resourceType, resourceID string, viewDelta, downloadDelta, installDelta int64) error {
	m.callCount++
	if m.callCount <= m.failN {
		return errors.New("db error")
	}
	m.calls = append(m.calls, upsertCall{
		ResourceType:  resourceType,
		ResourceID:    resourceID,
		ViewDelta:     viewDelta,
		DownloadDelta: downloadDelta,
		InstallDelta:  installDelta,
	})
	return nil
}

func setupTestWorker(t *testing.T, repo MetricsRepository) (*FlushWorker, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	cfg := DefaultFlushWorkerConfig()
	cfg.Interval = 50 * time.Millisecond // fast for tests
	w := NewFlushWorker(rdb, repo, cfg)
	return w, mr
}

func TestParseDirtyKey_Normal(t *testing.T) {
	cases := []struct {
		input     string
		wantType  string
		wantID    string
		wantValid bool
	}{
		{"skill:sk-1", "skill", "sk-1", true},
		{"mcp:mcp-abc-123", "mcp", "mcp-abc-123", true},
		// resourceID containing colons
		{"skill:sk:with:colons", "skill", "sk:with:colons", true},
	}
	for _, tc := range cases {
		parts := strings.SplitN(tc.input, ":", 2)
		if len(parts) != 2 {
			if tc.wantValid {
				t.Errorf("expected valid parse for %q", tc.input)
			}
			continue
		}
		if parts[0] != tc.wantType || parts[1] != tc.wantID {
			t.Errorf("parse %q: got (%q, %q), want (%q, %q)", tc.input, parts[0], parts[1], tc.wantType, tc.wantID)
		}
	}
}

func TestParseDirtyKey_Invalid(t *testing.T) {
	cases := []string{
		"",        // empty
		"nocolon", // no colon
		":notype", // empty type
		"noid:",   // empty id
	}
	for _, input := range cases {
		parts := strings.SplitN(input, ":", 2)
		valid := len(parts) == 2 && parts[0] != "" && parts[1] != ""
		if valid {
			t.Errorf("expected invalid parse for %q but got valid", input)
		}
	}
}

func TestFlushWorker_HappyPath(t *testing.T) {
	repo := &mockRepo{}
	w, mr := setupTestWorker(t, repo)

	ctx := context.Background()

	// Simulate tracked events
	mr.Set("metrics:skill:sk-1:view", "5")
	mr.Set("metrics:skill:sk-1:download", "2")
	mr.SAdd("metrics:dirty", "skill:sk-1")

	mr.Set("metrics:skill:sk-2:view", "10")
	mr.Set("metrics:skill:sk-2:download", "0")
	mr.SAdd("metrics:dirty", "skill:sk-2")

	// Run single flush
	w.flush(ctx)

	if len(repo.calls) != 2 {
		t.Fatalf("expected 2 upsert calls, got %d", len(repo.calls))
	}

	// Verify sk-1
	found := false
	for _, c := range repo.calls {
		if c.ResourceID == "sk-1" {
			found = true
			if c.ResourceType != "skill" || c.ViewDelta != 5 || c.DownloadDelta != 2 {
				t.Errorf("sk-1: got %+v", c)
			}
		}
	}
	if !found {
		t.Error("sk-1 not found in upsert calls")
	}

	// Verify sk-2 (download=0 but view=10, so should still upsert)
	found = false
	for _, c := range repo.calls {
		if c.ResourceID == "sk-2" {
			found = true
			if c.ViewDelta != 10 || c.DownloadDelta != 0 {
				t.Errorf("sk-2: got %+v", c)
			}
		}
	}
	if !found {
		t.Error("sk-2 not found in upsert calls")
	}

	// Verify Redis counters are reset to "0"
	v, _ := mr.Get("metrics:skill:sk-1:view")
	if v != "0" {
		t.Errorf("expected view counter reset to 0, got %q", v)
	}

	// Verify dirty set is empty
	members, _ := mr.Members("metrics:dirty")
	if len(members) != 0 {
		t.Errorf("expected empty dirty set, got %v", members)
	}
}

func TestFlushWorker_DeltaAllZero_SkipsUpsert(t *testing.T) {
	repo := &mockRepo{}
	w, mr := setupTestWorker(t, repo)

	ctx := context.Background()

	// Key is dirty but counters are 0 (already flushed or race)
	mr.Set("metrics:skill:sk-1:view", "0")
	mr.Set("metrics:skill:sk-1:download", "0")
	mr.SAdd("metrics:dirty", "skill:sk-1")

	w.flush(ctx)

	if len(repo.calls) != 0 {
		t.Fatalf("expected 0 upsert calls for zero deltas, got %d", len(repo.calls))
	}
}

func TestFlushWorker_DBFailRetry_ThenSADDBack(t *testing.T) {
	// Fail all 3 retries
	repo := &mockRepo{failN: 3}
	w, mr := setupTestWorker(t, repo)

	ctx := context.Background()

	mr.Set("metrics:skill:sk-1:view", "3")
	mr.SAdd("metrics:dirty", "skill:sk-1")

	w.flush(ctx)

	// Should have attempted 3 times
	if repo.callCount != 3 {
		t.Fatalf("expected 3 retry attempts, got %d", repo.callCount)
	}

	// No successful upserts
	if len(repo.calls) != 0 {
		t.Fatalf("expected 0 successful upserts, got %d", len(repo.calls))
	}

	// Key should be re-added to dirty set
	members, _ := mr.Members("metrics:dirty")
	if len(members) != 1 || members[0] != "skill:sk-1" {
		t.Errorf("expected dirty set to have [skill:sk-1], got %v", members)
	}
}

func TestFlushWorker_DBFailRetry_SucceedsOnRetry(t *testing.T) {
	// Fail first 2, succeed on 3rd
	repo := &mockRepo{failN: 2}
	w, mr := setupTestWorker(t, repo)

	ctx := context.Background()

	mr.Set("metrics:skill:sk-1:view", "7")
	mr.SAdd("metrics:dirty", "skill:sk-1")

	w.flush(ctx)

	// Should have 1 successful upsert
	if len(repo.calls) != 1 {
		t.Fatalf("expected 1 successful upsert, got %d", len(repo.calls))
	}
	if repo.calls[0].ViewDelta != 7 {
		t.Errorf("expected viewDelta=7, got %d", repo.calls[0].ViewDelta)
	}
}

func TestFlushWorker_LockNotAcquired_Skips(t *testing.T) {
	repo := &mockRepo{}
	w, mr := setupTestWorker(t, repo)

	ctx := context.Background()

	// Pre-acquire the lock with a different instance
	mr.Set(flushLockKey, "other-instance")
	mr.SetTTL(flushLockKey, 120*time.Second)

	mr.Set("metrics:skill:sk-1:view", "5")
	mr.SAdd("metrics:dirty", "skill:sk-1")

	w.flush(ctx)

	// Should not process anything
	if len(repo.calls) != 0 {
		t.Fatalf("expected 0 upsert calls when lock not acquired, got %d", len(repo.calls))
	}

	// Dirty set should remain unchanged
	members, _ := mr.Members("metrics:dirty")
	if len(members) != 1 {
		t.Errorf("expected dirty set untouched, got %v", members)
	}
}

func TestFlushWorker_LockRelease_ValueCheck(t *testing.T) {
	repo := &mockRepo{}
	w, mr := setupTestWorker(t, repo)

	ctx := context.Background()

	// Empty dirty set so flush completes quickly
	w.flush(ctx)

	// After flush, lock should be released
	ok := mr.Exists(flushLockKey)
	if ok {
		t.Error("expected lock to be released after flush")
	}
}

func TestFlushWorker_LockRelease_DoesNotDeleteOthersLock(t *testing.T) {
	repo := &mockRepo{}
	mr := miniredis.RunT(t)
	rdb := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	cfg := DefaultFlushWorkerConfig()
	w := NewFlushWorker(rdb, repo, cfg)

	ctx := context.Background()

	// Simulate: lock is acquired by another instance (value differs)
	mr.Set(flushLockKey, "other-instance")

	// Call releaseLock directly — should NOT delete since value doesn't match
	w.releaseLock(ctx)

	val, err := mr.Get(flushLockKey)
	if err != nil {
		t.Fatalf("lock key should still exist: %v", err)
	}
	if val != "other-instance" {
		t.Errorf("lock value changed unexpectedly: %q", val)
	}
}

func TestFlushWorker_LockRelease_AfterContextCancel(t *testing.T) {
	// slowRepo cancels the context on the first UpsertCounts call,
	// simulating a graceful shutdown while flush is in progress.
	cancelOnCall := &contextCancelRepo{t: t}
	mr := miniredis.RunT(t)
	rdb := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	cfg := DefaultFlushWorkerConfig()
	cfg.Batch = 500
	w := NewFlushWorker(rdb, &cancelOnCall.mockRepo, cfg)

	// Set up dirty items
	mr.Set("metrics:skill:sk-1:view", "5")
	mr.SAdd("metrics:dirty", "skill:sk-1")
	mr.Set("metrics:skill:sk-2:view", "3")
	mr.SAdd("metrics:dirty", "skill:sk-2")

	ctx, cancel := context.WithCancel(context.Background())
	cancelOnCall.cancel = cancel

	// flush acquires lock, processes sk-1 (which triggers cancel), then exits
	w.flush(ctx)

	// Lock must be released even though context was cancelled mid-flush
	ok := mr.Exists(flushLockKey)
	if ok {
		t.Error("expected lock to be released after context cancellation during flush")
	}
}

// contextCancelRepo cancels the provided context on the first UpsertCounts call.
type contextCancelRepo struct {
	mockRepo
	cancel context.CancelFunc
	t      *testing.T
}

func (r *contextCancelRepo) UpsertCounts(ctx context.Context, resourceType, resourceID string, viewDelta, downloadDelta, installDelta int64) error {
	// Cancel the context to simulate shutdown during flush
	if r.cancel != nil {
		r.cancel()
		r.cancel = nil
	}
	return r.mockRepo.UpsertCounts(ctx, resourceType, resourceID, viewDelta, downloadDelta, installDelta)
}

func TestFlushWorker_NonSkillType_Skipped(t *testing.T) {
	repo := &mockRepo{}
	w, mr := setupTestWorker(t, repo)

	ctx := context.Background()

	// Non-skill type in dirty set
	mr.Set("metrics:mcp:mcp-1:view", "10")
	mr.SAdd("metrics:dirty", "mcp:mcp-1")

	w.flush(ctx)

	// Should not process non-skill types in v1
	if len(repo.calls) != 0 {
		t.Fatalf("expected 0 upserts for non-skill type, got %d", len(repo.calls))
	}
}

func TestFlushWorker_ResourceIDWithColons(t *testing.T) {
	repo := &mockRepo{}
	w, mr := setupTestWorker(t, repo)

	ctx := context.Background()

	// resourceID contains colons
	mr.Set("metrics:skill:uuid:with:colons:view", "3")
	mr.SAdd("metrics:dirty", "skill:uuid:with:colons")

	w.flush(ctx)

	if len(repo.calls) != 1 {
		t.Fatalf("expected 1 upsert call, got %d", len(repo.calls))
	}
	if repo.calls[0].ResourceID != "uuid:with:colons" {
		t.Errorf("expected resourceID 'uuid:with:colons', got %q", repo.calls[0].ResourceID)
	}
}

func TestFlushWorker_MultipleBatches(t *testing.T) {
	repo := &mockRepo{}
	mr := miniredis.RunT(t)
	rdb := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	cfg := DefaultFlushWorkerConfig()
	cfg.Batch = 2 // Small batch for testing multiple iterations
	w := NewFlushWorker(rdb, repo, cfg)

	ctx := context.Background()

	// Add more items than batch size
	for i := 0; i < 5; i++ {
		key := fmt.Sprintf("skill:sk-%d", i)
		mr.Set(fmt.Sprintf("metrics:skill:sk-%d:view", i), strconv.Itoa(i+1))
		mr.SAdd("metrics:dirty", key)
	}

	w.flush(ctx)

	// All should be processed across multiple batches
	if len(repo.calls) != 5 {
		t.Fatalf("expected 5 upsert calls across batches, got %d", len(repo.calls))
	}
}

func TestFlushWorker_GracefulShutdown(t *testing.T) {
	repo := &mockRepo{}
	mr := miniredis.RunT(t)
	rdb := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	cfg := DefaultFlushWorkerConfig()
	cfg.Interval = 10 * time.Millisecond
	w := NewFlushWorker(rdb, repo, cfg)

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		w.Start(ctx)
		close(done)
	}()

	// Let it tick once
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case <-done:
		// OK - worker shut down
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not shut down in time")
	}
}

func TestParseCounterResult_EdgeCases(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	ctx := context.Background()

	// Key doesn't exist — GetSet returns redis.Nil
	pipe := rdb.Pipeline()
	cmd := pipe.GetSet(ctx, "nonexistent", "0")
	_, _ = pipe.Exec(ctx)
	result := parseCounterResult(cmd)
	if result != 0 {
		t.Errorf("expected 0 for nonexistent key, got %d", result)
	}

	// Key with non-numeric value
	mr.Set("bad", "notanumber")
	pipe = rdb.Pipeline()
	cmd = pipe.GetSet(ctx, "bad", "0")
	_, _ = pipe.Exec(ctx)
	result = parseCounterResult(cmd)
	if result != 0 {
		t.Errorf("expected 0 for non-numeric value, got %d", result)
	}
}
