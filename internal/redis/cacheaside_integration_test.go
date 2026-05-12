package redis

import (
	"context"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

// TestCacheAside_LiveDeduplicates verifies the cache-aside helper against a
// real Redis: the first call invokes the loader, the second hits the cache
// and skips it entirely. Skips when REDIS_URL is unset.
//
// This is the contract every cached handler (/auth/me, /budgets list, public
// invite info, profile rows) depends on. If this breaks, every cache-aside
// site silently doubles its DB load.
func TestCacheAside_LiveDeduplicates(t *testing.T) {
	url := os.Getenv("REDIS_URL")
	if url == "" {
		t.Skip("REDIS_URL not set; skipping live integration test")
	}

	cache := New(url)
	defer cache.Close()

	SetDefault(cache)
	defer SetDefault(nil)

	ctx := context.Background()
	key := "_test:fws:cacheaside:" + t.Name()
	defer cache.Delete(ctx, key)

	var loaderCalls int32

	load := func(_ context.Context) ([]byte, error) {
		atomic.AddInt32(&loaderCalls, 1)
		return []byte(`{"name":"financentury"}`), nil
	}

	// First call — miss, loader runs.
	got1, err := CacheAside(ctx, key, 5*time.Second, load)
	if err != nil {
		t.Fatalf("first CacheAside: %v", err)
	}
	if string(got1) != `{"name":"financentury"}` {
		t.Fatalf("first body: got %q", string(got1))
	}
	if atomic.LoadInt32(&loaderCalls) != 1 {
		t.Fatalf("expected 1 loader call after first miss, got %d", loaderCalls)
	}

	// Second call — hit, loader MUST NOT run.
	got2, err := CacheAside(ctx, key, 5*time.Second, load)
	if err != nil {
		t.Fatalf("second CacheAside: %v", err)
	}
	if string(got2) != string(got1) {
		t.Fatalf("second body differs from first: %q vs %q", got2, got1)
	}
	if atomic.LoadInt32(&loaderCalls) != 1 {
		t.Fatalf("expected loader to stay at 1 (cache hit), got %d", loaderCalls)
	}

	// Invalidate, third call must re-run loader.
	Delete(ctx, key)
	got3, err := CacheAside(ctx, key, 5*time.Second, load)
	if err != nil {
		t.Fatalf("third CacheAside: %v", err)
	}
	if string(got3) != string(got1) {
		t.Fatalf("third body differs: %q vs %q", got3, got1)
	}
	if atomic.LoadInt32(&loaderCalls) != 2 {
		t.Fatalf("expected loader to be called again after Delete, total=%d", loaderCalls)
	}

	// Verify the raw key actually exists in Redis.
	raw, ok := cache.Get(ctx, key)
	if !ok {
		t.Fatal("raw Redis Get expected to hit after CacheAside populated it")
	}
	if string(raw) != string(got1) {
		t.Fatalf("raw Redis body differs from CacheAside body: %q vs %q", raw, got1)
	}
}
