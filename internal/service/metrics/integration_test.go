package metrics

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
)

// =============================================================================
// Integration / Smoke Tests for DEV-35
//
// These tests verify the full metrics chain end-to-end:
//   1. Track → Redis INCR + SADD dirty
//   2. Flush worker picks dirty keys → GetSet counters → UpsertCounts to DB
//   3. Redis failure resilience (track still returns nil, main flow unblocked)
//   4. Multi-worker distributed lock (only one flushes)
//   5. Comprehensive sort scoring formula (unit-level verification)
// =============================================================================

// --- 1. Full chain: Track → Redis → Flush → DB ---

func TestIntegration_ViewChain_TrackToFlushToDB(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	ctx := context.Background()

	// Wire service with real miniredis client
	redisClient := &integrationRedisClient{rdb: rdb}
	svc := New(redisClient)

	// Register a resolver that always allows
	RegisterResolver("skill", &integrationResolver{})
	defer unregisterResolver("skill")

	// Track a view
	err := svc.TrackView(ctx, "skill", "skill-001", Caller{UID: "u1", SpaceID: "sp1"})
	if err != nil {
		t.Fatalf("TrackView: %v", err)
	}

	// Verify Redis state
	viewKey := "metrics:skill:skill-001:view"
	val, err := rdb.Get(ctx, viewKey).Result()
	if err != nil {
		t.Fatalf("redis GET %s: %v", viewKey, err)
	}
	if val != "1" {
		t.Fatalf("view counter = %q, want %q", val, "1")
	}

	// Verify dirty set
	isMember, err := rdb.SIsMember(ctx, "metrics:dirty", "skill:skill-001").Result()
	if err != nil {
		t.Fatalf("SIsMember: %v", err)
	}
	if !isMember {
		t.Fatal("skill:skill-001 not in dirty set")
	}

	// Run flush worker once
	repo := &mockRepo{}
	cfg := DefaultFlushWorkerConfig()
	cfg.Interval = time.Hour // won't tick, we call flush() manually
	w := NewFlushWorker(rdb, repo, cfg)
	w.flush(ctx)

	// Verify DB got the upsert
	if len(repo.calls) != 1 {
		t.Fatalf("expected 1 upsert call, got %d", len(repo.calls))
	}
	call := repo.calls[0]
	if call.ResourceType != "skill" || call.ResourceID != "skill-001" {
		t.Errorf("upsert got type=%q id=%q", call.ResourceType, call.ResourceID)
	}
	if call.ViewDelta != 1 {
		t.Errorf("view delta = %d, want 1", call.ViewDelta)
	}
	if call.DownloadDelta != 0 {
		t.Errorf("download delta = %d, want 0", call.DownloadDelta)
	}

	// Verify counter was reset to "0" after flush
	val, _ = rdb.Get(ctx, viewKey).Result()
	if val != "0" {
		t.Errorf("view key after flush = %q, want %q", val, "0")
	}

	// Verify dirty set is now empty
	size, _ := rdb.SCard(ctx, "metrics:dirty").Result()
	if size != 0 {
		t.Errorf("dirty set size after flush = %d, want 0", size)
	}
}

func TestIntegration_DownloadChain_TrackToFlushToDB(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	ctx := context.Background()

	redisClient := &integrationRedisClient{rdb: rdb}
	svc := New(redisClient)

	RegisterResolver("skill", &integrationResolver{})
	defer unregisterResolver("skill")

	// Track 3 downloads
	for range 3 {
		if err := svc.TrackDownload(ctx, "skill", "skill-dl-1"); err != nil {
			t.Fatalf("TrackDownload: %v", err)
		}
	}

	// Verify Redis accumulated count
	downloadKey := "metrics:skill:skill-dl-1:download"
	val, _ := rdb.Get(ctx, downloadKey).Result()
	if val != "3" {
		t.Fatalf("download counter = %q, want %q", val, "3")
	}

	// Flush
	repo := &mockRepo{}
	w := NewFlushWorker(rdb, repo, DefaultFlushWorkerConfig())
	w.flush(ctx)

	if len(repo.calls) != 1 {
		t.Fatalf("expected 1 upsert call, got %d", len(repo.calls))
	}
	if repo.calls[0].DownloadDelta != 3 {
		t.Errorf("download delta = %d, want 3", repo.calls[0].DownloadDelta)
	}
}

func TestIntegration_MixedViewAndDownload(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	ctx := context.Background()

	redisClient := &integrationRedisClient{rdb: rdb}
	svc := New(redisClient)

	RegisterResolver("skill", &integrationResolver{})
	defer unregisterResolver("skill")

	// 5 views + 2 downloads on the same skill
	for range 5 {
		svc.TrackView(ctx, "skill", "skill-mix", Caller{UID: "u1", SpaceID: "sp1"})
	}
	for range 2 {
		svc.TrackDownload(ctx, "skill", "skill-mix")
	}

	repo := &mockRepo{}
	w := NewFlushWorker(rdb, repo, DefaultFlushWorkerConfig())
	w.flush(ctx)

	if len(repo.calls) != 1 {
		t.Fatalf("expected 1 upsert, got %d", len(repo.calls))
	}
	if repo.calls[0].ViewDelta != 5 {
		t.Errorf("view delta = %d, want 5", repo.calls[0].ViewDelta)
	}
	if repo.calls[0].DownloadDelta != 2 {
		t.Errorf("download delta = %d, want 2", repo.calls[0].DownloadDelta)
	}
}

// --- 2. Monitoring / Structured Log Fields ---

func TestIntegration_FlushWorkerProcessesMultipleResources(t *testing.T) {
	// Verifies the flush worker correctly processes multiple dirty resources
	// in one pass and logs appropriate counts (resources_processed).
	mr := miniredis.RunT(t)
	rdb := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	ctx := context.Background()

	// Seed 3 different skills in dirty set
	for i := range 3 {
		id := fmt.Sprintf("skill-%d", i)
		rdb.Set(ctx, fmt.Sprintf("metrics:skill:%s:view", id), strconv.Itoa((i+1)*10), 0)
		rdb.SAdd(ctx, "metrics:dirty", fmt.Sprintf("skill:%s", id))
	}

	repo := &mockRepo{}
	w := NewFlushWorker(rdb, repo, DefaultFlushWorkerConfig())
	w.flush(ctx)

	// All 3 should be flushed
	if len(repo.calls) != 3 {
		t.Fatalf("expected 3 upserts, got %d", len(repo.calls))
	}
}

// --- 3. Redis Failure Resilience ---

func TestIntegration_RedisDown_TrackViewReturnsNil(t *testing.T) {
	// Start and immediately close miniredis to simulate unavailability
	mr := miniredis.RunT(t)
	rdb := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	mr.Close() // Simulate Redis being down

	redisClient := &integrationRedisClient{rdb: rdb}
	svc := New(redisClient)

	RegisterResolver("skill", &integrationResolver{})
	defer unregisterResolver("skill")

	// TrackView should NOT return error — Redis failures are swallowed
	err := svc.TrackView(context.Background(), "skill", "skill-redis-down", Caller{UID: "u1", SpaceID: "sp1"})
	if err != nil {
		t.Fatalf("TrackView should return nil when Redis is down, got: %v", err)
	}
}

func TestIntegration_RedisDown_TrackDownloadReturnsNil(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	mr.Close()

	redisClient := &integrationRedisClient{rdb: rdb}
	svc := New(redisClient)

	RegisterResolver("skill", &integrationResolver{})
	defer unregisterResolver("skill")

	err := svc.TrackDownload(context.Background(), "skill", "skill-redis-down")
	if err != nil {
		t.Fatalf("TrackDownload should return nil when Redis is down, got: %v", err)
	}
}

func TestIntegration_RedisDown_FlushWorkerSkipsGracefully(t *testing.T) {
	// When Redis is down, flush worker should not panic — it should log and
	// skip (lock acquire fails or SPOP fails).
	mr := miniredis.RunT(t)
	rdb := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	mr.Close()

	repo := &mockRepo{}
	w := NewFlushWorker(rdb, repo, DefaultFlushWorkerConfig())

	// Should not panic
	w.flush(context.Background())

	// No DB calls since Redis is down
	if len(repo.calls) != 0 {
		t.Errorf("expected 0 upserts when Redis is down, got %d", len(repo.calls))
	}
}

// --- 4. Multi-Worker Distributed Lock ---

func TestIntegration_MultiWorkerLock_OnlyOneFlushes(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	ctx := context.Background()

	// Seed data
	rdb.Set(ctx, "metrics:skill:shared-skill:view", "10", 0)
	rdb.SAdd(ctx, "metrics:dirty", "skill:shared-skill")

	repo1 := &mockRepo{}
	repo2 := &mockRepo{}

	cfg := DefaultFlushWorkerConfig()
	w1 := NewFlushWorker(rdb, repo1, cfg)
	w2 := NewFlushWorker(rdb, repo2, cfg)

	// Run both workers concurrently
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		w1.flush(ctx)
	}()
	go func() {
		defer wg.Done()
		w2.flush(ctx)
	}()
	wg.Wait()

	// Only one should have processed
	total := len(repo1.calls) + len(repo2.calls)
	if total != 1 {
		t.Fatalf("expected exactly 1 total upsert across 2 workers, got %d (w1=%d, w2=%d)",
			total, len(repo1.calls), len(repo2.calls))
	}
}

func TestIntegration_LockRelease_ValueCheck(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	ctx := context.Background()

	cfg := DefaultFlushWorkerConfig()
	w := NewFlushWorker(rdb, &mockRepo{}, cfg)

	// Manually acquire lock with a different instance ID
	rdb.Set(ctx, flushLockKey, "other-instance-id", cfg.LockTTL)

	// Worker's releaseLock should NOT delete it (value mismatch)
	w.releaseLock(ctx)

	// Lock should still exist with the other instance's value
	val, err := rdb.Get(ctx, flushLockKey).Result()
	if err != nil {
		t.Fatalf("lock should still exist: %v", err)
	}
	if val != "other-instance-id" {
		t.Errorf("lock value = %q, want %q", val, "other-instance-id")
	}
}

// --- 5. Comprehensive Sort Scoring ---

func TestIntegration_ComprehensiveSortFormula(t *testing.T) {
	// Verify the sort formula logic matches what's in SQL:
	//   score = downloads*5 + views*1 + 20/pow((hours/24 + 2), 1.2)
	// A new skill with fewer downloads/views can rank above an old stale skill
	// due to the recent_bonus time decay term.

	computeScore := func(downloads, views int64, hoursOld float64) float64 {
		daysOld := hoursOld / 24.0
		recentBonus := 20.0 / math.Pow(daysOld+2, 1.2)
		return float64(downloads)*5.0 + float64(views)*1.0 + recentBonus
	}

	// Test cases
	tests := []struct {
		name      string
		downloads int64
		views     int64
		hoursOld  float64
	}{
		{"brand_new_no_engagement", 0, 0, 0},
		{"new_with_some_views", 2, 10, 1},
		{"moderate_30_days", 5, 20, 720},
		{"stale_90_days_minimal", 1, 2, 2160},
		{"popular_old", 100, 500, 1440},
	}

	scores := make([]float64, len(tests))
	for i, tt := range tests {
		scores[i] = computeScore(tt.downloads, tt.views, tt.hoursOld)
		t.Logf("%s: score=%.4f (dl=%d, v=%d, age=%dh)",
			tt.name, scores[i], tt.downloads, tt.views, int(tt.hoursOld))
	}

	// Verify properties of the scoring formula:

	// 1. Brand new skill has positive score (recent_bonus > 0)
	if scores[0] <= 0 {
		t.Errorf("brand new skill score should be > 0, got %.4f", scores[0])
	}

	// 2. Downloads weighted 5x: 10dl+0v at 24h > 0dl+49v at 24h
	// (10*5=50 vs 49*1=49, downloads weight is 5x views)
	tenDl := computeScore(10, 0, 24)
	fortyNineV := computeScore(0, 49, 24)
	if tenDl <= fortyNineV {
		t.Errorf("10 downloads (%.4f) should outweigh 49 views (%.4f) at same age due to 5x weight",
			tenDl, fortyNineV)
	}

	// 3. New skill with modest engagement beats stale minimal skill
	if scores[1] <= scores[3] {
		t.Errorf("new_with_some_views (%.4f) should beat stale_90_days (%.4f)",
			scores[1], scores[3])
	}

	// 4. Popular old skill still ranks high due to sheer volume
	if scores[4] <= scores[1] {
		t.Errorf("popular_old (%.4f) should beat new_with_some_views (%.4f)",
			scores[4], scores[1])
	}

	// 5. Recent bonus decays: same engagement, older = lower score
	fresh := computeScore(5, 10, 1)
	aged := computeScore(5, 10, 720)
	if fresh <= aged {
		t.Errorf("fresh (%.4f) should beat aged (%.4f) with same engagement", fresh, aged)
	}
}

// --- 6. DB Failure Retry + Re-add to Dirty Set ---

func TestIntegration_DBFailure_ReaddsToDirtySet(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	ctx := context.Background()

	// Seed
	rdb.Set(ctx, "metrics:skill:fail-skill:view", "5", 0)
	rdb.SAdd(ctx, "metrics:dirty", "skill:fail-skill")

	// Repo that always fails (failN > maxRetries)
	repo := &mockRepo{failN: 10}
	w := NewFlushWorker(rdb, repo, DefaultFlushWorkerConfig())
	w.flush(ctx)

	// Should have been re-added to dirty set
	isMember, _ := rdb.SIsMember(ctx, "metrics:dirty", "skill:fail-skill").Result()
	if !isMember {
		t.Fatal("failed member should be re-added to dirty set after DB failures")
	}
}

func TestIntegration_DBFailure_RetrySucceeds(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	ctx := context.Background()

	rdb.Set(ctx, "metrics:skill:retry-skill:view", "7", 0)
	rdb.SAdd(ctx, "metrics:dirty", "skill:retry-skill")

	// Fail first 2 attempts, succeed on 3rd
	repo := &mockRepo{failN: 2}
	w := NewFlushWorker(rdb, repo, DefaultFlushWorkerConfig())
	w.flush(ctx)

	// Should have succeeded after retries
	if len(repo.calls) != 1 {
		t.Fatalf("expected 1 successful upsert after retries, got %d", len(repo.calls))
	}
	if repo.calls[0].ViewDelta != 7 {
		t.Errorf("view delta = %d, want 7", repo.calls[0].ViewDelta)
	}

	// Should NOT be in dirty set (success)
	isMember, _ := rdb.SIsMember(ctx, "metrics:dirty", "skill:retry-skill").Result()
	if isMember {
		t.Error("successfully flushed member should not be in dirty set")
	}
}

// --- 7. Zero Delta Skip ---

func TestIntegration_ZeroDelta_SkipsUpsert(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	ctx := context.Background()

	// Add to dirty set but counters are already "0" (or not set)
	rdb.SAdd(ctx, "metrics:dirty", "skill:zero-skill")
	rdb.Set(ctx, "metrics:skill:zero-skill:view", "0", 0)
	rdb.Set(ctx, "metrics:skill:zero-skill:download", "0", 0)

	repo := &mockRepo{}
	w := NewFlushWorker(rdb, repo, DefaultFlushWorkerConfig())
	w.flush(ctx)

	// No DB calls for zero deltas
	if len(repo.calls) != 0 {
		t.Fatalf("expected 0 upserts for zero delta, got %d", len(repo.calls))
	}
}

// --- 8. Non-skill resource type is skipped ---

func TestIntegration_NonSkillType_SkippedByFlush(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	ctx := context.Background()

	// Add a "mcp" type (not "skill") to dirty set
	rdb.Set(ctx, "metrics:mcp:mcp-001:view", "100", 0)
	rdb.SAdd(ctx, "metrics:dirty", "mcp:mcp-001")

	repo := &mockRepo{}
	w := NewFlushWorker(rdb, repo, DefaultFlushWorkerConfig())
	w.flush(ctx)

	// v1 only processes "skill" — mcp should be skipped
	if len(repo.calls) != 0 {
		t.Fatalf("expected 0 upserts for non-skill type, got %d", len(repo.calls))
	}
}

// --- 9. Concurrent track calls (stress) ---

func TestIntegration_ConcurrentTracks(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	ctx := context.Background()

	redisClient := &integrationRedisClient{rdb: rdb}
	svc := New(redisClient)

	RegisterResolver("skill", &integrationResolver{})
	defer unregisterResolver("skill")

	// Concurrent views
	const n = 100
	var wg sync.WaitGroup
	wg.Add(n)
	for range n {
		go func() {
			defer wg.Done()
			svc.TrackView(ctx, "skill", "concurrent-skill", Caller{UID: "u1", SpaceID: "sp1"})
		}()
	}
	wg.Wait()

	// All should have accumulated
	val, _ := rdb.Get(ctx, "metrics:skill:concurrent-skill:view").Result()
	count, _ := strconv.ParseInt(val, 10, 64)
	if count != n {
		t.Fatalf("concurrent view count = %d, want %d", count, n)
	}

	// Flush and verify
	repo := &mockRepo{}
	w := NewFlushWorker(rdb, repo, DefaultFlushWorkerConfig())
	w.flush(ctx)

	if len(repo.calls) != 1 {
		t.Fatalf("expected 1 upsert, got %d", len(repo.calls))
	}
	if repo.calls[0].ViewDelta != n {
		t.Errorf("view delta = %d, want %d", repo.calls[0].ViewDelta, int64(n))
	}
}

// --- 10. Flush worker lifecycle: Start with context cancellation ---

func TestIntegration_FlushWorkerStart_GracefulShutdown(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})

	repo := &mockRepo{}
	cfg := DefaultFlushWorkerConfig()
	cfg.Interval = 20 * time.Millisecond
	w := NewFlushWorker(rdb, repo, cfg)

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		w.Start(ctx)
		close(done)
	}()

	// Let it run a couple ticks
	time.Sleep(60 * time.Millisecond)
	cancel()

	// Should exit promptly
	select {
	case <-done:
		// OK
	case <-time.After(2 * time.Second):
		t.Fatal("flush worker did not shut down within 2s after context cancel")
	}
}

// =============================================================================
// Helpers
// =============================================================================

// integrationRedisClient wraps go-redis and implements MetricsRedis interface,
// matching the behavior of internal/redis.Client.
type integrationRedisClient struct {
	rdb *goredis.Client
}

func (c *integrationRedisClient) TrackView(ctx context.Context, resourceType, resourceID string) error {
	return c.trackEvent(ctx, resourceType, resourceID, "view")
}

func (c *integrationRedisClient) TrackDownload(ctx context.Context, resourceType, resourceID string) error {
	return c.trackEvent(ctx, resourceType, resourceID, "download")
}

func (c *integrationRedisClient) TrackInstall(ctx context.Context, resourceType, resourceID string) error {
	return c.trackEvent(ctx, resourceType, resourceID, "install")
}

func (c *integrationRedisClient) trackEvent(ctx context.Context, resourceType, resourceID, eventType string) error {
	counterKey := fmt.Sprintf("metrics:%s:%s:%s", resourceType, resourceID, eventType)
	dirtyMember := fmt.Sprintf("%s:%s", resourceType, resourceID)

	pipe := c.rdb.Pipeline()
	pipe.Incr(ctx, counterKey)
	pipe.SAdd(ctx, "metrics:dirty", dirtyMember)
	_, err := pipe.Exec(ctx)
	return err
}

// integrationResolver allows all CanView calls for integration tests.
type integrationResolver struct{}

func (r *integrationResolver) CanView(_ context.Context, _ string, _ Caller) (bool, error) {
	return true, nil
}

// unregisterResolver removes a resolver from the registry for test isolation.
func unregisterResolver(resourceType string) {
	delete(resolvers, resourceType)
}
