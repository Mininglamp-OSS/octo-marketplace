package auth

import (
	"context"
	"sync"
	"time"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

type cacheEntry struct {
	identity  model.Identity
	expiresAt time.Time
}

type CachedResolver struct {
	inner   Resolver
	ttl     time.Duration
	maxSize int
	mu      sync.Mutex
	entries map[string]cacheEntry
}

func NewCachedResolver(inner Resolver, ttl time.Duration, maxSize int) *CachedResolver {
	return &CachedResolver{
		inner:   inner,
		ttl:     ttl,
		maxSize: maxSize,
		entries: make(map[string]cacheEntry),
	}
}

func (r *CachedResolver) Resolve(ctx context.Context, token string) (model.Identity, error) {
	now := time.Now()
	r.mu.Lock()
	if entry, ok := r.entries[token]; ok && now.Before(entry.expiresAt) {
		r.mu.Unlock()
		return entry.identity, nil
	}
	delete(r.entries, token)
	r.mu.Unlock()

	identity, err := r.inner.Resolve(ctx, token)
	if err != nil || identity.UID == "" {
		return identity, err
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.maxSize > 0 && len(r.entries) >= r.maxSize {
		for key, entry := range r.entries {
			if now.After(entry.expiresAt) || len(r.entries) >= r.maxSize {
				delete(r.entries, key)
			}
		}
	}
	r.entries[token] = cacheEntry{identity: identity, expiresAt: now.Add(r.ttl)}
	return identity, nil
}
