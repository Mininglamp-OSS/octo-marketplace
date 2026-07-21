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
	calls         []upsertCall
	failN         int // fail the first N calls
	callCount     int
	cleanupCalls  int
	cleanupCutoff time.Time
}

type upsertCall struct {
	FlushID       string
	ResourceType  string
	ResourceID    string
	ViewDelta     int64
	DownloadDelta int64
	InstallDelta  int64
}

func (m *mockRepo) UpsertCountsOnce(_ context.Context, flushID, resourceType, resourceID string, viewDelta, downloadDelta, installDelta int64) error {
	m.callCount++
	if m.callCount <= m.failN {
		return errors.New("db error")
	}
	m.calls = append(m.calls, upsertCall{
		FlushID:       flushID,
		ResourceType:  resourceType,
		ResourceID:    resourceID,
		ViewDelta:     viewDelta,
		DownloadDelta: downloadDelta,
		InstallDelta:  installDelta,
	})
	return nil
}

func (m *mockRepo) DeleteAppliedFlushesBefore(_ context.Context, cutoff time.Time) error {
	m.cleanupCalls++
	m.cleanupCutoff = cutoff
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
	mr.Set("metrics:skill:sk-1:install", "0")
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
	mr.Set("metrics:skill:sk-1:download", "2")
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

	// Key is removed from dirty and retained as a durable pending delta.
	members, _ := mr.Members("metrics:dirty")
	if len(members) != 0 {
		t.Errorf("expected dirty set to be empty, got %v", members)
	}
	pendingIDs, _ := mr.Members("metrics:pending")
	if len(pendingIDs) != 1 {
		t.Fatalf("expected one pending delta, got %v", pendingIDs)
	}
	viewVal, _ := mr.Get("metrics:skill:sk-1:view")
	if viewVal != "0" {
		t.Errorf("expected view counter drained to 0, got %q", viewVal)
	}
	dlVal, _ := mr.Get("metrics:skill:sk-1:download")
	if dlVal != "0" {
		t.Errorf("expected download counter drained to 0, got %q", dlVal)
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

func TestFlushWorker_CleansLedgerWhenNoPending(t *testing.T) {
	repo := &mockRepo{}
	w, _ := setupTestWorker(t, repo)

	w.flush(context.Background())

	if repo.cleanupCalls != 1 {
		t.Fatalf("cleanupCalls = %d, want 1", repo.cleanupCalls)
	}
	if repo.cleanupCutoff.IsZero() {
		t.Fatal("cleanup cutoff was not set")
	}
}

func TestFlushWorker_SkipsLedgerCleanupWhenPendingExists(t *testing.T) {
	repo := &mockRepo{}
	w, mr := setupTestWorker(t, repo)
	mr.SAdd("metrics:pending", "flush-old")
	mr.HSet("metrics:pending:flush-old", "member", "skill:sk-1")

	w.flush(context.Background())

	if repo.cleanupCalls != 0 {
		t.Fatalf("cleanupCalls = %d, want 0 while pending exists", repo.cleanupCalls)
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
	w := NewFlushWorker(rdb, cancelOnCall, cfg)

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

	members, _ := mr.Members("metrics:dirty")
	if len(members) != 1 {
		t.Fatalf("expected exactly one unprocessed dirty member requeued, got %v", members)
	}
	requeuedID := strings.TrimPrefix(members[0], "skill:")
	viewVal, err := mr.Get("metrics:skill:" + requeuedID + ":view")
	if err != nil {
		t.Fatalf("expected requeued member counter to remain in Redis: %v", err)
	}
	if viewVal == "0" {
		t.Fatalf("requeued member %q counter was reset before processing", members[0])
	}
}

// contextCancelRepo cancels the provided context on the first UpsertCounts call.
type contextCancelRepo struct {
	mockRepo
	cancel context.CancelFunc
	t      *testing.T
}

func (r *contextCancelRepo) UpsertCountsOnce(ctx context.Context, flushID, resourceType, resourceID string, viewDelta, downloadDelta, installDelta int64) error {
	// Cancel the context to simulate shutdown during flush
	if r.cancel != nil {
		r.cancel()
		r.cancel = nil
	}
	return r.mockRepo.UpsertCountsOnce(ctx, flushID, resourceType, resourceID, viewDelta, downloadDelta, installDelta)
}

func TestFlushWorker_NonSkillType_Requeued(t *testing.T) {
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
	members, _ := mr.Members("metrics:dirty")
	if len(members) != 1 || members[0] != "mcp:mcp-1" {
		t.Fatalf("expected non-skill member to be requeued, got %v", members)
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

func TestParseCounterValue_EdgeCases(t *testing.T) {
	if got := parseCounterValue(nil); got != 0 {
		t.Errorf("expected 0 for nil, got %d", got)
	}
	if got := parseCounterValue("notanumber"); got != 0 {
		t.Errorf("expected 0 for non-numeric value, got %d", got)
	}
	if got := parseCounterValue("42"); got != 42 {
		t.Errorf("expected 42, got %d", got)
	}
}

func TestFlushWorker_DrainCountersCreatesPendingDelta(t *testing.T) {
	repo := &mockRepo{}
	w, mr := setupTestWorker(t, repo)

	ctx := context.Background()
	mr.Set("metrics:skill:scripted:view", "4")
	mr.Set("metrics:skill:scripted:download", "3")
	mr.Set("metrics:skill:scripted:install", "2")
	mr.SAdd("metrics:dirty", "skill:scripted")

	pending, hasDelta, err := w.drainToPending(ctx, "skill:scripted", "skill", "scripted")
	if err != nil {
		t.Fatal(err)
	}
	if !hasDelta {
		t.Fatal("expected pending delta")
	}
	if pending.ViewDelta != 4 || pending.DownloadDelta != 3 || pending.InstallDelta != 2 {
		t.Fatalf("deltas = (%d,%d,%d), want (4,3,2)", pending.ViewDelta, pending.DownloadDelta, pending.InstallDelta)
	}
	for _, key := range []string{"view", "download", "install"} {
		got, err := mr.Get("metrics:skill:scripted:" + key)
		if err != nil {
			t.Fatal(err)
		}
		if got != "0" {
			t.Fatalf("%s counter = %q, want 0", key, got)
		}
	}
	members, _ := mr.Members("metrics:dirty")
	if len(members) != 0 {
		t.Fatalf("dirty members = %v, want empty", members)
	}
	pendingIDs, _ := mr.Members("metrics:pending")
	if len(pendingIDs) != 1 || pendingIDs[0] != pending.FlushID {
		t.Fatalf("pending IDs = %v, want [%s]", pendingIDs, pending.FlushID)
	}
}

func TestFlushWorker_InstallCounter_Flushed(t *testing.T) {
	repo := &mockRepo{}
	w, mr := setupTestWorker(t, repo)

	ctx := context.Background()

	// Simulate install event tracked by TrackInstall
	mr.Set("metrics:skill:sk-1:install", "4")
	mr.SAdd("metrics:dirty", "skill:sk-1")

	w.flush(ctx)

	if len(repo.calls) != 1 {
		t.Fatalf("expected 1 upsert call, got %d", len(repo.calls))
	}
	if repo.calls[0].InstallDelta != 4 {
		t.Errorf("expected installDelta=4, got %d", repo.calls[0].InstallDelta)
	}
	if repo.calls[0].ViewDelta != 0 {
		t.Errorf("expected viewDelta=0, got %d", repo.calls[0].ViewDelta)
	}
	if repo.calls[0].DownloadDelta != 0 {
		t.Errorf("expected downloadDelta=0, got %d", repo.calls[0].DownloadDelta)
	}

	// Verify install counter is reset
	v, _ := mr.Get("metrics:skill:sk-1:install")
	if v != "0" {
		t.Errorf("expected install counter reset to 0, got %q", v)
	}
}

func TestFlushWorker_AllCounters_Flushed(t *testing.T) {
	repo := &mockRepo{}
	w, mr := setupTestWorker(t, repo)

	ctx := context.Background()

	// Simulate all three events tracked
	mr.Set("metrics:skill:sk-1:view", "10")
	mr.Set("metrics:skill:sk-1:download", "5")
	mr.Set("metrics:skill:sk-1:install", "3")
	mr.SAdd("metrics:dirty", "skill:sk-1")

	w.flush(ctx)

	if len(repo.calls) != 1 {
		t.Fatalf("expected 1 upsert call, got %d", len(repo.calls))
	}
	c := repo.calls[0]
	if c.ViewDelta != 10 || c.DownloadDelta != 5 || c.InstallDelta != 3 {
		t.Errorf("expected (10,5,3), got (%d,%d,%d)", c.ViewDelta, c.DownloadDelta, c.InstallDelta)
	}
}

func TestFlushWorker_DBFail_PendingThenNextFlushSucceeds(t *testing.T) {
	// First flush: DB fails all 3 retries, leaving the delta in Redis pending.
	// Second flush: DB succeeds from pending without needing dirty counters.
	repo := &mockRepo{failN: 3}
	w, mr := setupTestWorker(t, repo)

	ctx := context.Background()

	mr.Set("metrics:skill:sk-1:view", "5")
	mr.Set("metrics:skill:sk-1:download", "2")
	mr.Set("metrics:skill:sk-1:install", "1")
	mr.SAdd("metrics:dirty", "skill:sk-1")

	// First flush — fails
	w.flush(ctx)

	if len(repo.calls) != 0 {
		t.Fatalf("expected 0 successful upserts after first flush, got %d", len(repo.calls))
	}

	// Verify counters were drained and the delta moved to pending.
	viewVal, _ := mr.Get("metrics:skill:sk-1:view")
	if viewVal != "0" {
		t.Errorf("expected view=0 after failed flush, got %q", viewVal)
	}
	installVal, _ := mr.Get("metrics:skill:sk-1:install")
	if installVal != "0" {
		t.Errorf("expected install=0 after failed flush, got %q", installVal)
	}
	pendingIDs, _ := mr.Members("metrics:pending")
	if len(pendingIDs) != 1 {
		t.Fatalf("expected one pending delta after failed flush, got %v", pendingIDs)
	}

	// Simulate DB recovery — no more failures
	repo.failN = 0
	repo.callCount = 0

	// Second flush — succeeds
	w.flush(ctx)

	if len(repo.calls) != 1 {
		t.Fatalf("expected 1 successful upsert after second flush, got %d", len(repo.calls))
	}
	c := repo.calls[0]
	if c.ViewDelta != 5 || c.DownloadDelta != 2 || c.InstallDelta != 1 {
		t.Errorf("expected (5,2,1), got (%d,%d,%d)", c.ViewDelta, c.DownloadDelta, c.InstallDelta)
	}
	pendingIDs, _ = mr.Members("metrics:pending")
	if len(pendingIDs) != 0 {
		t.Fatalf("expected pending set empty after successful retry, got %v", pendingIDs)
	}
}

func TestFlushWorker_ContextCancel_LeavesPendingDelta(t *testing.T) {
	// Simulates: counters are drained into pending, then the context is cancelled
	// before DB persistence. The pending delta remains available for retry.
	cancelOnUpsert := &contextCancelOnUpsertRepo{t: t}
	mr := miniredis.RunT(t)
	rdb := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	cfg := DefaultFlushWorkerConfig()
	cfg.Batch = 500
	w := NewFlushWorker(rdb, cancelOnUpsert, cfg)

	// Set up dirty item with all three counters
	mr.Set("metrics:skill:sk-cancel:view", "7")
	mr.Set("metrics:skill:sk-cancel:download", "3")
	mr.Set("metrics:skill:sk-cancel:install", "2")
	mr.SAdd("metrics:dirty", "skill:sk-cancel")

	ctx, cancel := context.WithCancel(context.Background())
	cancelOnUpsert.cancel = cancel

	// flush: acquires lock → moves counters to pending → upsert triggers cancel.
	w.flush(ctx)

	// No successful upserts
	if len(cancelOnUpsert.calls) != 0 {
		t.Fatalf("expected 0 successful upserts, got %d", len(cancelOnUpsert.calls))
	}

	// Counters are drained, and the pending delta is retained despite cancellation.
	viewVal, _ := mr.Get("metrics:skill:sk-cancel:view")
	if viewVal != "0" {
		t.Errorf("expected view counter drained to 0 after ctx cancel, got %q", viewVal)
	}
	dlVal, _ := mr.Get("metrics:skill:sk-cancel:download")
	if dlVal != "0" {
		t.Errorf("expected download counter drained to 0 after ctx cancel, got %q", dlVal)
	}
	installVal, _ := mr.Get("metrics:skill:sk-cancel:install")
	if installVal != "0" {
		t.Errorf("expected install counter drained to 0 after ctx cancel, got %q", installVal)
	}

	members, _ := mr.Members("metrics:dirty")
	if len(members) != 0 {
		t.Errorf("expected dirty set to be empty, got %v", members)
	}
	pendingIDs, _ := mr.Members("metrics:pending")
	if len(pendingIDs) != 1 {
		t.Errorf("expected one pending delta, got %v", pendingIDs)
	}
}

// contextCancelOnUpsertRepo cancels ctx on the first UpsertCounts call and returns ctx.Err().
// This simulates: GETSET already cleared counters, then graceful shutdown fires.
type contextCancelOnUpsertRepo struct {
	mockRepo
	cancel context.CancelFunc
	t      *testing.T
}

func (r *contextCancelOnUpsertRepo) UpsertCountsOnce(ctx context.Context, flushID, resourceType, resourceID string, viewDelta, downloadDelta, installDelta int64) error {
	if r.cancel != nil {
		r.cancel()
		r.cancel = nil
	}
	// Return context error to simulate cancelled DB call
	return ctx.Err()
}
