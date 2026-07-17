package redis

import (
	"context"
	"fmt"
	"log"

	goredis "github.com/redis/go-redis/v9"
)

// Key prefix constants for metrics.
const (
	metricsKeyPrefix = "metrics:"
	dirtySetKey      = "metrics:dirty"
)

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

	pipe := c.rdb.Pipeline()
	pipe.Incr(ctx, counterKey)
	pipe.SAdd(ctx, dirtySetKey, dirtyMember)
	_, err := pipe.Exec(ctx)
	if err != nil {
		log.Printf("[metrics-redis] WARN: failed to track %s for %s/%s: %v", eventType, resourceType, resourceID, err)
	}
	return err
}
