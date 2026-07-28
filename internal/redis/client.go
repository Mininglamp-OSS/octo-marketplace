package redis

import (
	"context"
	"fmt"
	"strconv"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/logging"
	goredis "github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

// Key prefix constants for metrics.
const (
	metricsKeyPrefix = "metrics:"
	dirtySetKey      = "metrics:dirty"
)

var trackEventScript = goredis.NewScript(`
redis.call("INCRBY", KEYS[1], tonumber(ARGV[1]))
redis.call("SADD", KEYS[2], ARGV[2])
return 1
`)

// Client wraps a Redis client for metrics operations.
type Client struct {
	rdb *goredis.Client
}

// NewClient creates a new Redis metrics client.
func NewClient(rdb *goredis.Client) *Client {
	return &Client{rdb: rdb}
}

// TrackView increments the view counter for a resource and marks it dirty.
func (c *Client) TrackView(ctx context.Context, resourceType, resourceID string) error {
	return c.trackEvent(ctx, resourceType, resourceID, "view")
}

// TrackDownload increments the download counter for a resource and marks it dirty.
func (c *Client) TrackDownload(ctx context.Context, resourceType, resourceID string) error {
	return c.trackEvent(ctx, resourceType, resourceID, "download")
}

// TrackInstall increments the install counter for a resource and marks it dirty.
func (c *Client) TrackInstall(ctx context.Context, resourceType, resourceID string) error {
	return c.trackEvent(ctx, resourceType, resourceID, "install")
}

func (c *Client) trackEvent(ctx context.Context, resourceType, resourceID, eventType string) error {
	counterKey := fmt.Sprintf("%s%s:%s:%s", metricsKeyPrefix, resourceType, resourceID, eventType)
	dirtyMember := fmt.Sprintf("%s:%s", resourceType, resourceID)

	_, err := trackEventScript.Run(ctx, c.rdb, []string{counterKey, dirtySetKey}, strconv.FormatInt(1, 10), dirtyMember).Result()
	if err != nil {
		logging.Warn("metrics_redis_track_failed",
			zap.String("operation", "metrics.redis.track"),
			zap.String("event_type", eventType),
			zap.String("resource_type", resourceType),
			zap.String("resource_id", resourceID),
			logging.ErrorField(err),
		)
	}
	return err
}
