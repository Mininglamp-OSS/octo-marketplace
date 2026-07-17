package metrics

import (
	"context"
	"errors"
	"log"

	metricsredis "github.com/Mininglamp-OSS/octo-marketplace/internal/redis"
)

// Errors returned by the metrics service.
var (
	ErrInvalidParam       = errors.New("invalid parameter")
	ErrUnsupportedType    = errors.New("unsupported resource type")
	ErrUnsupportedEvent   = errors.New("unsupported event type")
	ErrResourceNotVisible = errors.New("resource not visible")
)

// MetricsRedis defines the interface for Redis operations needed by the service.
type MetricsRedis interface {
	TrackView(ctx context.Context, resourceType, resourceID string) error
	TrackDownload(ctx context.Context, resourceType, resourceID string) error
	TrackInstall(ctx context.Context, resourceType, resourceID string) error
}

// Service is the metrics tracking service.
type Service struct {
	redis MetricsRedis
}

// New creates a new metrics Service.
// If redis is nil, a no-op implementation is used (all tracking silently succeeds).
func New(redis MetricsRedis) *Service {
	if redis == nil {
		redis = &noopRedis{}
	}
	return &Service{redis: redis}
}

// NewWithRedisClient is a convenience constructor that wraps a *redis.Client.
func NewWithRedisClient(client *metricsredis.Client) *Service {
	return New(client)
}

// TrackView validates the request and increments the view counter.
// Redis failures are logged but do not propagate to the caller.
func (s *Service) TrackView(ctx context.Context, resourceType, resourceID string, caller Caller) error {
	if err := validateParams(resourceType, resourceID); err != nil {
		return err
	}

	resolver, ok := GetResolver(resourceType)
	if !ok {
		return ErrUnsupportedType
	}

	canView, err := resolver.CanView(ctx, resourceID, caller)
	if err != nil {
		return err
	}
	if !canView {
		return ErrResourceNotVisible
	}

	if err := s.redis.TrackView(ctx, resourceType, resourceID); err != nil {
		log.Printf("[metrics] WARN: redis TrackView failed for %s/%s: %v", resourceType, resourceID, err)
	}
	return nil
}

// TrackDownload increments the download counter. Internal use — no caller validation.
// Redis failures are logged but do not propagate.
func (s *Service) TrackDownload(ctx context.Context, resourceType, resourceID string) error {
	if err := validateParams(resourceType, resourceID); err != nil {
		return err
	}

	resolver, ok := GetResolver(resourceType)
	if !ok {
		return ErrUnsupportedType
	}
	_ = resolver // resolver found means type is valid

	if err := s.redis.TrackDownload(ctx, resourceType, resourceID); err != nil {
		log.Printf("[metrics] WARN: redis TrackDownload failed for %s/%s: %v", resourceType, resourceID, err)
	}
	return nil
}

// TrackInstall increments the install counter. Internal use — no caller validation.
// Redis failures are logged but do not propagate.
func (s *Service) TrackInstall(ctx context.Context, resourceType, resourceID string) error {
	if err := validateParams(resourceType, resourceID); err != nil {
		return err
	}

	resolver, ok := GetResolver(resourceType)
	if !ok {
		return ErrUnsupportedType
	}
	_ = resolver

	if err := s.redis.TrackInstall(ctx, resourceType, resourceID); err != nil {
		log.Printf("[metrics] WARN: redis TrackInstall failed for %s/%s: %v", resourceType, resourceID, err)
	}
	return nil
}

func validateParams(resourceType, resourceID string) error {
	if resourceType == "" || resourceID == "" {
		return ErrInvalidParam
	}
	if len(resourceType) > 32 {
		return ErrInvalidParam
	}
	if len(resourceID) > 64 {
		return ErrInvalidParam
	}
	return nil
}

// noopRedis is a no-op implementation of MetricsRedis for when Redis is not configured.
type noopRedis struct{}

func (n *noopRedis) TrackView(context.Context, string, string) error     { return nil }
func (n *noopRedis) TrackDownload(context.Context, string, string) error { return nil }
func (n *noopRedis) TrackInstall(context.Context, string, string) error  { return nil }
