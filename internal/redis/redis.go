// Package redis provides an optional cache-aside layer backed by Redis.
//
// The package is opt-in: when REDIS_URL is empty (or unparseable) New returns
// a no-op Cache so the rest of the codebase can call Get/Set/Delete without
// branching on "is redis configured?". Every method tolerates a nil client
// gracefully — a transient Redis outage degrades to a cache miss instead of
// an error path.
//
// PERF: This layer sits in FRONT of Postgres for read-mostly endpoints whose
// values tolerate ~30s-5min staleness OR are explicitly invalidated on write
// (auth/me, profiles, /budgets list, /budgets/:id/linkable, public invite
// info). Endpoints with WS-driven invalidation (summary/trends/resume) keep
// using the existing in-process cache because adding a Redis hop there only
// adds latency.
//
// SECURITY: cached blobs are scoped per-user-id in their key so one user's
// cached payload can never leak to another. Keys never embed bearer tokens
// or request bodies.
package redis

import (
	"context"
	"errors"
	"log"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

// Cache is the minimal surface every handler interacts with. Returning the
// interface (rather than the concrete struct) keeps tests easy: a fake
// implementation can stand in without spinning up Redis.
type Cache interface {
	Get(ctx context.Context, key string) ([]byte, bool)
	Set(ctx context.Context, key string, value []byte, ttl time.Duration)
	Delete(ctx context.Context, keys ...string)
	DeletePattern(ctx context.Context, pattern string)
	Close()
}

// New returns a Cache backed by Redis. When redisURL is empty (deployment
// without a Redis server, or local dev) a no-op cache is returned so callers
// don't need to nil-check the result.
//
// Connection failures during construction also fall back to the no-op cache
// rather than aborting startup — Redis is a performance optimisation, not
// a correctness dependency.
func New(redisURL string) Cache {
	if redisURL == "" {
		log.Println("[redis] REDIS_URL not set; cache-aside layer disabled (no-op)")
		return noopCache{}
	}
	opts, err := goredis.ParseURL(redisURL)
	if err != nil {
		log.Printf("[redis] failed to parse REDIS_URL (cache-aside disabled): %v", err)
		return noopCache{}
	}
	// Tight timeouts: a slow Redis must NEVER stall a handler. Default
	// dial+read are ~5s which is way too generous when the caller's user
	// is staring at a spinner.
	opts.DialTimeout = 1 * time.Second
	opts.ReadTimeout = 200 * time.Millisecond
	opts.WriteTimeout = 200 * time.Millisecond
	opts.MaxRetries = 1

	client := goredis.NewClient(opts)

	// Validate the connection eagerly so misconfiguration surfaces at
	// startup instead of silently degrading every request.
	pingCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := client.Ping(pingCtx).Err(); err != nil {
		log.Printf("[redis] PING failed (cache-aside disabled): %v", err)
		_ = client.Close()
		return noopCache{}
	}
	log.Printf("[redis] connected; cache-aside layer enabled")
	return &redisCache{client: client}
}

// redisCache is the live, network-backed implementation.
type redisCache struct {
	client *goredis.Client
}

// Get returns (bytes, true) on a hit, (nil, false) on miss / error. Errors
// are logged at debug level only — a Redis outage must degrade silently.
func (c *redisCache) Get(ctx context.Context, key string) ([]byte, bool) {
	if c == nil || c.client == nil {
		return nil, false
	}
	b, err := c.client.Get(ctx, key).Bytes()
	if err != nil {
		if !errors.Is(err, goredis.Nil) {
			// PERF: don't log every miss — only real errors. goredis.Nil is
			// the wire-level "key not found" sentinel.
			log.Printf("[redis] GET %s: %v", key, err)
		}
		return nil, false
	}
	return b, true
}

// Set writes value under key with ttl. ttl<=0 is treated as a no-op so
// callers can disable caching for a request without branching.
func (c *redisCache) Set(ctx context.Context, key string, value []byte, ttl time.Duration) {
	if c == nil || c.client == nil || ttl <= 0 {
		return
	}
	if err := c.client.Set(ctx, key, value, ttl).Err(); err != nil {
		log.Printf("[redis] SET %s: %v", key, err)
	}
}

// Delete removes one or more keys. Variadic to match the natural call sites
// in mutation handlers (which often invalidate 2-4 keys at once).
func (c *redisCache) Delete(ctx context.Context, keys ...string) {
	if c == nil || c.client == nil || len(keys) == 0 {
		return
	}
	if err := c.client.Del(ctx, keys...).Err(); err != nil {
		log.Printf("[redis] DEL: %v", err)
	}
}

// DeletePattern removes every key matching pattern. Implemented via SCAN +
// pipelined DEL so a large match set never blocks Redis with a single huge
// KEYS call. Pattern syntax is Redis-native (foo:* style globs).
//
// PERF: SCAN COUNT is set to 256 — small enough to keep individual ops
// snappy, large enough to keep the round-trip count down for typical
// (~hundreds of keys) match sets.
func (c *redisCache) DeletePattern(ctx context.Context, pattern string) {
	if c == nil || c.client == nil || pattern == "" {
		return
	}
	iter := c.client.Scan(ctx, 0, pattern, 256).Iterator()
	batch := make([]string, 0, 64)
	for iter.Next(ctx) {
		batch = append(batch, iter.Val())
		// Flush in batches so a pathologically large match set doesn't
		// hold a giant slice in memory before the first DEL fires.
		if len(batch) >= 256 {
			if err := c.client.Del(ctx, batch...).Err(); err != nil {
				log.Printf("[redis] DEL batch: %v", err)
			}
			batch = batch[:0]
		}
	}
	if err := iter.Err(); err != nil {
		log.Printf("[redis] SCAN %s: %v", pattern, err)
	}
	if len(batch) > 0 {
		if err := c.client.Del(ctx, batch...).Err(); err != nil {
			log.Printf("[redis] DEL final: %v", err)
		}
	}
}

// Close releases the underlying client connection. Safe to call multiple
// times.
func (c *redisCache) Close() {
	if c == nil || c.client == nil {
		return
	}
	_ = c.client.Close()
}

// noopCache is returned when Redis is not configured. Every method is a
// safe no-op so handler code stays branchless.
type noopCache struct{}

func (noopCache) Get(context.Context, string) ([]byte, bool)         { return nil, false }
func (noopCache) Set(context.Context, string, []byte, time.Duration) {}
func (noopCache) Delete(context.Context, ...string)                  {}
func (noopCache) DeletePattern(context.Context, string)              {}
func (noopCache) Close()                                             {}
