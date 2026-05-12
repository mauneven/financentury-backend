package redis

import (
	"context"
	"sync"
	"time"
)

// Default holds the process-wide cache instance. Initialized once in main()
// via SetDefault. Reading it without setting first returns a no-op cache so
// tests and pre-init handler calls degrade gracefully.
//
// The mutex protects the swap; reads after init are lock-free because Cache
// is an interface value (a pair of word-sized fields) and Go's memory model
// guarantees a single store of an interface value is observed atomically on
// most platforms — but we use the mutex anyway to satisfy the race detector
// without resorting to atomic.Value boxing.
var (
	defaultMu  sync.RWMutex
	defaultRef Cache = noopCache{}
)

// SetDefault swaps the package-level Cache used by GetDefault and the
// cache-aside helpers below. main() calls this once at startup.
func SetDefault(c Cache) {
	if c == nil {
		c = noopCache{}
	}
	defaultMu.Lock()
	defaultRef = c
	defaultMu.Unlock()
}

// GetDefault returns the current process-wide Cache. Always non-nil.
func GetDefault() Cache {
	defaultMu.RLock()
	c := defaultRef
	defaultMu.RUnlock()
	return c
}

// CloseDefault releases any underlying connection held by the default
// cache and resets the slot to a no-op so subsequent calls remain safe.
// Call this from main()'s graceful-shutdown path.
func CloseDefault() {
	defaultMu.Lock()
	if defaultRef != nil {
		defaultRef.Close()
	}
	defaultRef = noopCache{}
	defaultMu.Unlock()
}

// Get is a thin convenience wrapper over GetDefault().Get so handlers don't
// have to thread a Cache value into every helper.
func Get(ctx context.Context, key string) ([]byte, bool) {
	return GetDefault().Get(ctx, key)
}

// Set wraps GetDefault().Set with the same semantics.
func Set(ctx context.Context, key string, value []byte, ttl time.Duration) {
	GetDefault().Set(ctx, key, value, ttl)
}

// Delete wraps GetDefault().Delete.
func Delete(ctx context.Context, keys ...string) {
	GetDefault().Delete(ctx, keys...)
}

// DeletePattern wraps GetDefault().DeletePattern.
func DeletePattern(ctx context.Context, pattern string) {
	GetDefault().DeletePattern(ctx, pattern)
}

// CacheAside runs a cache-aside pattern: try GET, on miss call loader, store
// the bytes back under key with ttl, and return them. Errors from loader
// surface to the caller. Errors from the cache itself (network blips) never
// surface — they degrade to a recompute on the next request.
//
// PERF: ttl<=0 disables write-back so handler code can skip caching for a
// specific request without another branching layer.
//
// SECURITY: callers must include the user's id in `key` for any user-scoped
// payload — this helper does not enforce isolation, it just shuttles bytes.
func CacheAside(
	ctx context.Context,
	key string,
	ttl time.Duration,
	loader func(ctx context.Context) ([]byte, error),
) ([]byte, error) {
	if cached, ok := Get(ctx, key); ok {
		return cached, nil
	}
	data, err := loader(ctx)
	if err != nil {
		return nil, err
	}
	if ttl > 0 && len(data) > 0 {
		Set(ctx, key, data, ttl)
	}
	return data, nil
}
