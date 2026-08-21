package adapters

import (
	"sync"
	"time"

	"golang.org/x/time/rate"

	"mind-the-time/internal/domain"
)

type TTLRouteCache struct {
	mu         sync.Mutex
	entries    map[string]routeCacheEntry
	maxEntries int
	ttl        time.Duration
	now        func() time.Time
}

type routeCacheEntry struct {
	routes    []domain.Itinerary
	expiresAt time.Time
}

func NewTTLRouteCache(maxEntries int, ttl time.Duration) *TTLRouteCache {
	return &TTLRouteCache{
		entries:    make(map[string]routeCacheEntry),
		maxEntries: maxEntries,
		ttl:        ttl,
		now:        time.Now,
	}
}

func (c *TTLRouteCache) Get(key string) ([]domain.Itinerary, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, exists := c.entries[key]
	if !exists {
		return nil, false
	}
	if !c.now().Before(entry.expiresAt) {
		delete(c.entries, key)
		return nil, false
	}
	return copyRoutes(entry.routes), true
}

func (c *TTLRouteCache) Set(key string, routes []domain.Itinerary) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.maxEntries <= 0 || c.ttl <= 0 {
		return
	}
	c.evictExpired()
	if _, exists := c.entries[key]; !exists && len(c.entries) >= c.maxEntries {
		c.evictOldest()
	}
	c.entries[key] = routeCacheEntry{routes: copyRoutes(routes), expiresAt: c.now().Add(c.ttl)}
}

func (c *TTLRouteCache) evictExpired() {
	now := c.now()
	for key, entry := range c.entries {
		if !now.Before(entry.expiresAt) {
			delete(c.entries, key)
		}
	}
}

func (c *TTLRouteCache) evictOldest() {
	var oldestKey string
	var oldest time.Time
	for key, entry := range c.entries {
		if oldestKey == "" || entry.expiresAt.Before(oldest) {
			oldestKey, oldest = key, entry.expiresAt
		}
	}
	if oldestKey != "" {
		delete(c.entries, oldestKey)
	}
}

func copyRoutes(routes []domain.Itinerary) []domain.Itinerary {
	return append([]domain.Itinerary(nil), routes...)
}

type RateMissLimiter struct {
	limiter *rate.Limiter
}

func NewRateMissLimiter(limit rate.Limit, burst int) *RateMissLimiter {
	return &RateMissLimiter{limiter: rate.NewLimiter(limit, burst)}
}

func (l *RateMissLimiter) Allow() bool {
	return l.limiter.Allow()
}
