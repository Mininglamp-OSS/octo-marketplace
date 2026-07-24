package metrics

import (
	"context"
	"crypto/rand"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

// FlushWorkerConfig holds configuration for the flush worker.
type FlushWorkerConfig struct {
	Interval              time.Duration // How often to run flush (default 30s)
	Batch                 int64         // How many dirty keys to process per iteration (default 500)
	LockTTL               time.Duration // Distributed lock TTL (default 120s)
	FlushLedgerRetention  time.Duration // How long applied flush IDs remain idempotent (default 7d)
	FlushLedgerCleanupGap time.Duration // Minimum interval between cleanup attempts (default 1h)
}

// DefaultFlushWorkerConfig returns the default flush worker configuration.
func DefaultFlushWorkerConfig() FlushWorkerConfig {
	return FlushWorkerConfig{
		Interval:              30 * time.Second,
		Batch:                 500,
		LockTTL:               120 * time.Second,
		FlushLedgerRetention:  7 * 24 * time.Hour,
		FlushLedgerCleanupGap: time.Hour,
	}
}

// MetricsRepository is the interface for idempotently persisting metric deltas.
type MetricsRepository interface {
	UpsertCountsOnce(ctx context.Context, flushID, resourceType, resourceID string, viewDelta, downloadDelta, installDelta int64) error
}

type flushLedgerCleaner interface {
	DeleteAppliedFlushesBefore(ctx context.Context, cutoff time.Time) error
}

// FlushWorker periodically flushes Redis metric increments to the database.
type FlushWorker struct {
	rdb        *goredis.Client
	repo       MetricsRepository
	cfg        FlushWorkerConfig
	instanceID string
}

// NewFlushWorker creates a new FlushWorker.
func NewFlushWorker(rdb *goredis.Client, repo MetricsRepository, cfg FlushWorkerConfig) *FlushWorker {
	cfg = withFlushWorkerDefaults(cfg)
	return &FlushWorker{
		rdb:        rdb,
		repo:       repo,
		cfg:        cfg,
		instanceID: generateInstanceID(),
	}
}

func withFlushWorkerDefaults(cfg FlushWorkerConfig) FlushWorkerConfig {
	defaults := DefaultFlushWorkerConfig()
	if cfg.Interval <= 0 {
		cfg.Interval = defaults.Interval
	}
	if cfg.Batch <= 0 {
		cfg.Batch = defaults.Batch
	}
	if cfg.LockTTL <= 0 {
		cfg.LockTTL = defaults.LockTTL
	}
	if cfg.FlushLedgerRetention <= 0 {
		cfg.FlushLedgerRetention = defaults.FlushLedgerRetention
	}
	if cfg.FlushLedgerCleanupGap <= 0 {
		cfg.FlushLedgerCleanupGap = defaults.FlushLedgerCleanupGap
	}
	return cfg
}

// Start begins the flush loop. It blocks until ctx is cancelled.
func (w *FlushWorker) Start(ctx context.Context) {
	log.Printf("[flush-worker] started (interval=%s, batch=%d, lockTTL=%s, instance=%s)",
		w.cfg.Interval, w.cfg.Batch, w.cfg.LockTTL, w.instanceID)

	ticker := time.NewTicker(w.cfg.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Printf("[flush-worker] shutting down")
			return
		case <-ticker.C:
			w.flush(ctx)
		}
	}
}

const (
	flushLockKey     = "metrics:flush:lock"
	dirtySetKey      = "metrics:dirty"
	pendingSetKey    = "metrics:pending"
	pendingKeyPrefix = "metrics:pending:"
	keyPrefix        = "metrics:"
)

var drainToPendingScript = goredis.NewScript(`
local view = redis.call("GETSET", KEYS[1], "0") or "0"
local download = redis.call("GETSET", KEYS[2], "0") or "0"
local install = redis.call("GETSET", KEYS[3], "0") or "0"
redis.call("SREM", KEYS[5], ARGV[1])

local view_num = tonumber(view) or 0
local download_num = tonumber(download) or 0
local install_num = tonumber(install) or 0
if view_num == 0 and download_num == 0 and install_num == 0 then
	return {view, download, install, "0"}
end

redis.call("HSET", KEYS[4],
	"member", ARGV[1],
	"resource_type", ARGV[2],
	"resource_id", ARGV[3],
	"view", view,
	"download", download,
	"install", install)
redis.call("SADD", KEYS[6], ARGV[4])
return {view, download, install, "1"}
`)

var ackPendingScript = goredis.NewScript(`
redis.call("DEL", KEYS[1])
redis.call("SREM", KEYS[2], ARGV[1])
return 1
`)

type pendingDelta struct {
	FlushID       string
	Member        string
	ResourceType  string
	ResourceID    string
	ViewDelta     int64
	DownloadDelta int64
	InstallDelta  int64
}

func (w *FlushWorker) flush(ctx context.Context) {
	start := time.Now()

	acquired, err := w.acquireLock(ctx)
	if err != nil {
		log.Printf("[flush-worker] lock acquire error: %v", err)
		return
	}
	if !acquired {
		log.Printf("[flush-worker] lock held by another instance, skipping")
		return
	}
	defer func() {
		releaseCtx, releaseCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer releaseCancel()
		w.releaseLock(releaseCtx)
	}()

	dirtySize, _ := w.rdb.SCard(ctx, dirtySetKey).Result()
	pendingSize, _ := w.rdb.SCard(ctx, pendingSetKey).Result()
	log.Printf("[flush-worker] starting flush, dirty_set_size=%d, pending_set_size=%d", dirtySize, pendingSize)
	w.cleanupFlushLedger(ctx)

	var totalProcessed int64
	var totalDBFails int64

	w.processPending(ctx, &totalProcessed, &totalDBFails)
	if totalDBFails > 0 {
		w.logFlushResult(start, totalProcessed, totalDBFails)
		return
	}

	for {
		if ctx.Err() != nil {
			break
		}

		members, err := w.rdb.SRandMemberN(ctx, dirtySetKey, w.cfg.Batch).Result()
		if err != nil {
			log.Printf("[flush-worker] dirty sample error: %v", err)
			break
		}
		if len(members) == 0 {
			break
		}

		var madeProgress bool
		for _, member := range members {
			if ctx.Err() != nil {
				break
			}
			if w.processDirtyMember(ctx, member, &totalProcessed, &totalDBFails) {
				madeProgress = true
			}
		}
		if totalDBFails > 0 || !madeProgress {
			break
		}
	}

	w.logFlushResult(start, totalProcessed, totalDBFails)
}

func (w *FlushWorker) logFlushResult(start time.Time, totalProcessed, totalDBFails int64) {
	duration := time.Since(start)
	result := "success"
	if totalDBFails > 0 {
		result = "partial_failure"
	}
	log.Printf("[flush-worker] flush complete: result=%s, resources_processed=%d, db_failures=%d, duration=%s",
		result, totalProcessed, totalDBFails, duration)
}

func (w *FlushWorker) cleanupFlushLedger(ctx context.Context) {
	if w.cfg.FlushLedgerRetention <= 0 {
		return
	}
	cleaner, ok := w.repo.(flushLedgerCleaner)
	if !ok {
		return
	}
	pendingSize, err := w.rdb.SCard(ctx, pendingSetKey).Result()
	if err != nil {
		log.Printf("[flush-worker] WARN: ledger cleanup pending check failed: %v", err)
		return
	}
	if pendingSize > 0 {
		return
	}

	gap := w.cfg.FlushLedgerCleanupGap
	if gap <= 0 {
		gap = time.Hour
	}
	cleanupKey := flushLockKey + ":ledger_cleanup"
	acquired, err := w.rdb.SetNX(ctx, cleanupKey, w.instanceID, gap).Result()
	if err != nil {
		log.Printf("[flush-worker] WARN: ledger cleanup throttle failed: %v", err)
		return
	}
	if !acquired {
		return
	}

	cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cutoff := time.Now().Add(-w.cfg.FlushLedgerRetention)
	if err := cleaner.DeleteAppliedFlushesBefore(cleanupCtx, cutoff); err != nil {
		log.Printf("[flush-worker] WARN: ledger cleanup failed: %v", err)
	}
}

func (w *FlushWorker) processDirtyMember(ctx context.Context, member string, totalProcessed, totalDBFails *int64) bool {
	parts := strings.SplitN(member, ":", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		log.Printf("[flush-worker] WARN: invalid dirty key %q, removing", member)
		if err := w.rdb.SRem(ctx, dirtySetKey, member).Err(); err != nil {
			log.Printf("[flush-worker] WARN: failed to remove invalid dirty key %q: %v", member, err)
		}
		return true
	}
	resourceType := parts[0]
	resourceID := parts[1]

	if resourceType != "skill" && resourceType != "mcp" {
		log.Printf("[flush-worker] WARN: unsupported dirty resource type %q for member %q, leaving dirty", resourceType, member)
		return false
	}

	pending, hasDelta, err := w.drainToPending(ctx, member, resourceType, resourceID)
	if err != nil {
		log.Printf("[flush-worker] ERROR: drain counters for %s/%s: %v", resourceType, resourceID, err)
		*totalDBFails++
		return false
	}
	if !hasDelta {
		return true
	}
	return w.processPendingDelta(ctx, pending, totalProcessed, totalDBFails)
}

func (w *FlushWorker) processPending(ctx context.Context, totalProcessed, totalDBFails *int64) {
	for {
		if ctx.Err() != nil {
			return
		}
		flushIDs, err := w.rdb.SRandMemberN(ctx, pendingSetKey, w.cfg.Batch).Result()
		if err != nil {
			log.Printf("[flush-worker] pending sample error: %v", err)
			*totalDBFails++
			return
		}
		if len(flushIDs) == 0 {
			return
		}

		for _, flushID := range flushIDs {
			if ctx.Err() != nil {
				return
			}
			pending, err := w.loadPendingDelta(ctx, flushID)
			if err != nil {
				log.Printf("[flush-worker] ERROR: load pending delta %s: %v", flushID, err)
				*totalDBFails++
				return
			}
			if pending == nil {
				continue
			}
			if !w.processPendingDelta(ctx, *pending, totalProcessed, totalDBFails) {
				return
			}
		}
	}
}

func (w *FlushWorker) drainToPending(ctx context.Context, member, resourceType, resourceID string) (pendingDelta, bool, error) {
	flushID := w.newFlushID()
	viewKey := fmt.Sprintf("%s%s:%s:view", keyPrefix, resourceType, resourceID)
	downloadKey := fmt.Sprintf("%s%s:%s:download", keyPrefix, resourceType, resourceID)
	installKey := fmt.Sprintf("%s%s:%s:install", keyPrefix, resourceType, resourceID)

	raw, err := drainToPendingScript.Run(ctx, w.rdb,
		[]string{viewKey, downloadKey, installKey, pendingKey(flushID), dirtySetKey, pendingSetKey},
		member, resourceType, resourceID, flushID,
	).Result()
	if err != nil && err != goredis.Nil {
		return pendingDelta{}, false, fmt.Errorf("drain counters: %w", err)
	}
	values, ok := raw.([]interface{})
	if !ok || len(values) != 4 {
		return pendingDelta{}, false, fmt.Errorf("drain counters: unexpected script result %T", raw)
	}

	pending := pendingDelta{
		FlushID:       flushID,
		Member:        member,
		ResourceType:  resourceType,
		ResourceID:    resourceID,
		ViewDelta:     parseCounterValue(values[0]),
		DownloadDelta: parseCounterValue(values[1]),
		InstallDelta:  parseCounterValue(values[2]),
	}
	return pending, parseCounterValue(values[3]) == 1, nil
}

func (w *FlushWorker) loadPendingDelta(ctx context.Context, flushID string) (*pendingDelta, error) {
	values, err := w.rdb.HGetAll(ctx, pendingKey(flushID)).Result()
	if err != nil {
		return nil, err
	}
	if len(values) == 0 {
		if err := w.rdb.SRem(ctx, pendingSetKey, flushID).Err(); err != nil {
			return nil, err
		}
		return nil, nil
	}

	pending := &pendingDelta{
		FlushID:       flushID,
		Member:        values["member"],
		ResourceType:  values["resource_type"],
		ResourceID:    values["resource_id"],
		ViewDelta:     parseCounterValue(values["view"]),
		DownloadDelta: parseCounterValue(values["download"]),
		InstallDelta:  parseCounterValue(values["install"]),
	}
	if pending.Member == "" || pending.ResourceType == "" || pending.ResourceID == "" {
		return nil, fmt.Errorf("pending delta %s missing required fields", flushID)
	}
	return pending, nil
}

func (w *FlushWorker) processPendingDelta(ctx context.Context, pending pendingDelta, totalProcessed, totalDBFails *int64) bool {
	if pending.ViewDelta == 0 && pending.DownloadDelta == 0 && pending.InstallDelta == 0 {
		if err := w.ackPending(ctx, pending.FlushID); err != nil {
			log.Printf("[flush-worker] ERROR: ack zero pending %s: %v", pending.FlushID, err)
			*totalDBFails++
			return false
		}
		return true
	}

	if err := w.upsertWithRetry(ctx, pending); err != nil {
		log.Printf("[flush-worker] ERROR: db upsert failed for %s/%s flush=%s after retries: %v",
			pending.ResourceType, pending.ResourceID, pending.FlushID, err)
		*totalDBFails++
		return false
	}
	if err := w.ackPending(ctx, pending.FlushID); err != nil {
		log.Printf("[flush-worker] ERROR: ack pending %s: %v", pending.FlushID, err)
		*totalDBFails++
		return false
	}
	*totalProcessed++
	return true
}

func parseCounterValue(raw interface{}) int64 {
	if raw == nil {
		return 0
	}
	val := fmt.Sprint(raw)
	n, err := strconv.ParseInt(val, 10, 64)
	if err != nil {
		return 0
	}
	return n
}

func (w *FlushWorker) upsertWithRetry(ctx context.Context, pending pendingDelta) error {
	const maxRetries = 3
	const retryInterval = 100 * time.Millisecond

	var lastErr error
	for i := 0; i < maxRetries; i++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		err := w.repo.UpsertCountsOnce(ctx, pending.FlushID, pending.ResourceType, pending.ResourceID,
			pending.ViewDelta, pending.DownloadDelta, pending.InstallDelta)
		if err == nil {
			return nil
		}
		lastErr = err
		if i < maxRetries-1 {
			time.Sleep(retryInterval)
		}
	}
	return lastErr
}

func (w *FlushWorker) ackPending(ctx context.Context, flushID string) error {
	_, err := ackPendingScript.Run(ctx, w.rdb, []string{pendingKey(flushID), pendingSetKey}, flushID).Result()
	return err
}

func (w *FlushWorker) acquireLock(ctx context.Context) (bool, error) {
	ok, err := w.rdb.SetNX(ctx, flushLockKey, w.instanceID, w.cfg.LockTTL).Result()
	if err != nil {
		return false, fmt.Errorf("setnx lock: %w", err)
	}
	return ok, nil
}

func (w *FlushWorker) releaseLock(ctx context.Context) {
	const script = `if redis.call("GET", KEYS[1]) == ARGV[1] then return redis.call("DEL", KEYS[1]) else return 0 end`
	err := w.rdb.Eval(ctx, script, []string{flushLockKey}, w.instanceID).Err()
	if err != nil && err != goredis.Nil {
		log.Printf("[flush-worker] WARN: lock release failed: %v", err)
	}
}

func (w *FlushWorker) newFlushID() string {
	return fmt.Sprintf("%s:%s", w.instanceID, generateInstanceID())
}

func pendingKey(flushID string) string {
	return pendingKeyPrefix + flushID
}

func generateInstanceID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%x", b)
}
