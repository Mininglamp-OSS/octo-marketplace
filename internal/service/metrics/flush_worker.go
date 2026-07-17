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
	Interval time.Duration // How often to run flush (default 30s)
	Batch    int64         // How many dirty keys to pop per iteration (default 500)
	LockTTL  time.Duration // Distributed lock TTL (default 120s)
}

// DefaultFlushWorkerConfig returns the default flush worker configuration.
func DefaultFlushWorkerConfig() FlushWorkerConfig {
	return FlushWorkerConfig{
		Interval: 30 * time.Second,
		Batch:    500,
		LockTTL:  120 * time.Second,
	}
}

// MetricsRepository is the interface for persisting metric deltas to the database.
type MetricsRepository interface {
	UpsertCounts(ctx context.Context, resourceType, resourceID string, viewDelta, downloadDelta, installDelta int64) error
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
	return &FlushWorker{
		rdb:        rdb,
		repo:       repo,
		cfg:        cfg,
		instanceID: generateInstanceID(),
	}
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
	flushLockKey = "metrics:flush:lock"
	dirtySetKey  = "metrics:dirty"
	keyPrefix    = "metrics:"
)

func (w *FlushWorker) flush(ctx context.Context) {
	start := time.Now()

	// 1. Acquire distributed lock
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
		// Use an independent context for lock release so that even if the flush
		// context is cancelled (e.g. graceful shutdown), we still release our lock
		// instead of blocking other instances for the full TTL.
		releaseCtx, releaseCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer releaseCancel()
		w.releaseLock(releaseCtx)
	}()

	// Log dirty set size
	dirtySize, _ := w.rdb.SCard(ctx, dirtySetKey).Result()
	log.Printf("[flush-worker] starting flush, dirty_set_size=%d", dirtySize)

	var totalProcessed int64
	var totalDBFails int64
	var failedMembers []string

	// 2. Loop SPOP batch from dirty set
	for {
		if ctx.Err() != nil {
			break
		}

		members, err := w.rdb.SPopN(ctx, dirtySetKey, w.cfg.Batch).Result()
		if err != nil {
			log.Printf("[flush-worker] SPOP error: %v", err)
			break
		}
		if len(members) == 0 {
			break
		}

		for _, member := range members {
			if ctx.Err() != nil {
				break
			}
			w.processMember(ctx, member, &totalProcessed, &totalDBFails, &failedMembers)
		}
	}

	// 3. Re-add failed members to dirty set (after SPOP loop to avoid re-popping)
	if len(failedMembers) > 0 {
		ifaces := make([]interface{}, len(failedMembers))
		for i, m := range failedMembers {
			ifaces[i] = m
		}
		w.rdb.SAdd(ctx, dirtySetKey, ifaces...)
	}

	duration := time.Since(start)
	result := "success"
	if totalDBFails > 0 {
		result = "partial_failure"
	}
	log.Printf("[flush-worker] flush complete: result=%s, resources_processed=%d, db_failures=%d, duration=%s",
		result, totalProcessed, totalDBFails, duration)
}

func (w *FlushWorker) processMember(ctx context.Context, member string, totalProcessed, totalDBFails *int64, failedMembers *[]string) {
	// Parse "resourceType:resourceID" — SplitN with limit 2 to handle resourceID containing ":"
	parts := strings.SplitN(member, ":", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		log.Printf("[flush-worker] WARN: invalid dirty key %q, skipping", member)
		return
	}
	resourceType := parts[0]
	resourceID := parts[1]

	// v1: only process "skill" type
	if resourceType != "skill" {
		return
	}

	// Atomically get and reset counters
	viewDelta, downloadDelta, err := w.getAndResetCounters(ctx, resourceType, resourceID)
	if err != nil {
		log.Printf("[flush-worker] ERROR: get counters for %s/%s: %v", resourceType, resourceID, err)
		// Collect for re-add to dirty set after the SPOP loop
		*failedMembers = append(*failedMembers, member)
		return
	}

	// Skip if all deltas are zero
	if viewDelta == 0 && downloadDelta == 0 {
		return
	}

	// UPSERT to database with retries
	if err := w.upsertWithRetry(ctx, resourceType, resourceID, viewDelta, downloadDelta); err != nil {
		log.Printf("[flush-worker] ERROR: db upsert failed for %s/%s after retries: %v", resourceType, resourceID, err)
		// Collect for re-add to dirty set after the SPOP loop
		*failedMembers = append(*failedMembers, member)
		*totalDBFails++
		return
	}

	*totalProcessed++
}

func (w *FlushWorker) getAndResetCounters(ctx context.Context, resourceType, resourceID string) (viewDelta, downloadDelta int64, err error) {
	viewKey := fmt.Sprintf("%s%s:%s:view", keyPrefix, resourceType, resourceID)
	downloadKey := fmt.Sprintf("%s%s:%s:download", keyPrefix, resourceType, resourceID)

	// Use pipeline to atomically GetSet (GetSet replaces value with "0" and returns old value)
	pipe := w.rdb.Pipeline()
	viewCmd := pipe.GetSet(ctx, viewKey, "0")
	downloadCmd := pipe.GetSet(ctx, downloadKey, "0")
	_, err = pipe.Exec(ctx)
	if err != nil && err != goredis.Nil {
		return 0, 0, fmt.Errorf("pipeline exec: %w", err)
	}

	viewDelta = parseCounterResult(viewCmd)
	downloadDelta = parseCounterResult(downloadCmd)
	return viewDelta, downloadDelta, nil
}

func parseCounterResult(cmd *goredis.StringCmd) int64 {
	val, err := cmd.Result()
	if err != nil || val == "" {
		return 0
	}
	n, err := strconv.ParseInt(val, 10, 64)
	if err != nil {
		return 0
	}
	return n
}

func (w *FlushWorker) upsertWithRetry(ctx context.Context, resourceType, resourceID string, viewDelta, downloadDelta int64) error {
	const maxRetries = 3
	const retryInterval = 100 * time.Millisecond

	var lastErr error
	for i := 0; i < maxRetries; i++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		err := w.repo.UpsertCounts(ctx, resourceType, resourceID, viewDelta, downloadDelta, 0)
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

func (w *FlushWorker) acquireLock(ctx context.Context) (bool, error) {
	ok, err := w.rdb.SetNX(ctx, flushLockKey, w.instanceID, w.cfg.LockTTL).Result()
	if err != nil {
		return false, fmt.Errorf("setnx lock: %w", err)
	}
	return ok, nil
}

func (w *FlushWorker) releaseLock(ctx context.Context) {
	// Only delete if we still own the lock (Lua script for atomicity)
	const script = `if redis.call("GET", KEYS[1]) == ARGV[1] then return redis.call("DEL", KEYS[1]) else return 0 end`
	err := w.rdb.Eval(ctx, script, []string{flushLockKey}, w.instanceID).Err()
	if err != nil && err != goredis.Nil {
		log.Printf("[flush-worker] WARN: lock release failed: %v", err)
	}
}

func generateInstanceID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%x", b)
}
