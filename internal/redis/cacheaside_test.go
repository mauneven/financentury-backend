package redis

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeCache is an in-memory Cache implementation used for unit tests.
// Records call counts so tests can assert hit/miss behaviour.
type fakeCache struct {
	mu       sync.Mutex
	store    map[string]fakeEntry
	getCalls atomic.Int64
	setCalls atomic.Int64
	delCalls atomic.Int64
	patCalls atomic.Int64
	now      func() time.Time
}

type fakeEntry struct {
	value     []byte
	expiresAt time.Time
}

func newFakeCache() *fakeCache {
	return &fakeCache{
		store: make(map[string]fakeEntry),
		now:   time.Now,
	}
}

func (f *fakeCache) Get(_ context.Context, key string) ([]byte, bool) {
	f.getCalls.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	e, ok := f.store[key]
	if !ok {
		return nil, false
	}
	if !e.expiresAt.IsZero() && f.now().After(e.expiresAt) {
		delete(f.store, key)
		return nil, false
	}
	return e.value, true
}

func (f *fakeCache) Set(_ context.Context, key string, value []byte, ttl time.Duration) {
	f.setCalls.Add(1)
	if ttl <= 0 {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.store[key] = fakeEntry{value: value, expiresAt: f.now().Add(ttl)}
}

func (f *fakeCache) Delete(_ context.Context, keys ...string) {
	f.delCalls.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, k := range keys {
		delete(f.store, k)
	}
}

func (f *fakeCache) DeletePattern(_ context.Context, _ string) {
	f.patCalls.Add(1)
	// no-op for tests; pattern semantics validated separately if needed
}

func (f *fakeCache) Close() {}

// withFake swaps the package-level Default for the duration of a test.
func withFake(t *testing.T) *fakeCache {
	t.Helper()
	prev := GetDefault()
	fc := newFakeCache()
	SetDefault(fc)
	t.Cleanup(func() { SetDefault(prev) })
	return fc
}

// TestCacheAside_MissThenHit verifies the loader is called on the first
// access and skipped on the second.
func TestCacheAside_MissThenHit(t *testing.T) {
	fc := withFake(t)
	ctx := context.Background()

	var loaderCalls atomic.Int64
	loader := func(_ context.Context) ([]byte, error) {
		loaderCalls.Add(1)
		return []byte("payload"), nil
	}

	out, err := CacheAside(ctx, "k", time.Minute, loader)
	if err != nil {
		t.Fatalf("first CacheAside err: %v", err)
	}
	if string(out) != "payload" {
		t.Errorf("first call returned %q; want payload", out)
	}

	out, err = CacheAside(ctx, "k", time.Minute, loader)
	if err != nil {
		t.Fatalf("second CacheAside err: %v", err)
	}
	if string(out) != "payload" {
		t.Errorf("second call returned %q; want payload", out)
	}

	if got := loaderCalls.Load(); got != 1 {
		t.Errorf("loader called %d times; want 1", got)
	}
	if got := fc.setCalls.Load(); got != 1 {
		t.Errorf("Set called %d times; want 1", got)
	}
}

// TestCacheAside_TTLExpiry verifies that an expired entry forces a
// reload — the cached bytes never outlive their TTL.
func TestCacheAside_TTLExpiry(t *testing.T) {
	fc := withFake(t)
	now := time.Now()
	fc.now = func() time.Time { return now }

	ctx := context.Background()
	loaderCalls := atomic.Int64{}
	loader := func(_ context.Context) ([]byte, error) {
		loaderCalls.Add(1)
		return []byte("v"), nil
	}

	if _, err := CacheAside(ctx, "k", 100*time.Millisecond, loader); err != nil {
		t.Fatalf("first call err: %v", err)
	}
	// Advance the fake clock past TTL.
	now = now.Add(200 * time.Millisecond)
	if _, err := CacheAside(ctx, "k", 100*time.Millisecond, loader); err != nil {
		t.Fatalf("second call err: %v", err)
	}
	if got := loaderCalls.Load(); got != 2 {
		t.Errorf("loader called %d; want 2 (TTL did not force reload)", got)
	}
}

// TestCacheAside_MissingKey ensures the loader path runs cleanly when no
// entry exists in the cache.
func TestCacheAside_MissingKey(t *testing.T) {
	withFake(t)
	ctx := context.Background()

	called := false
	out, err := CacheAside(ctx, "absent", time.Minute, func(context.Context) ([]byte, error) {
		called = true
		return []byte("computed"), nil
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !called {
		t.Error("loader was not called on miss")
	}
	if string(out) != "computed" {
		t.Errorf("got %q; want computed", out)
	}
}

// TestCacheAside_LoaderError surfaces loader failures, and verifies nothing
// is cached on error (a half-built payload would corrupt subsequent reads).
func TestCacheAside_LoaderError(t *testing.T) {
	fc := withFake(t)
	ctx := context.Background()
	wantErr := errors.New("boom")

	if _, err := CacheAside(ctx, "k", time.Minute, func(context.Context) ([]byte, error) {
		return nil, wantErr
	}); !errors.Is(err, wantErr) {
		t.Fatalf("err = %v; want %v", err, wantErr)
	}
	if fc.setCalls.Load() != 0 {
		t.Error("Set was called on loader error; cache should stay clean")
	}
}

// TestCacheAside_ZeroTTL_SkipsWrite — a TTL <= 0 must still serve any cached
// hit but never write the loader's bytes back. Useful for one-off bypass.
func TestCacheAside_ZeroTTL_SkipsWrite(t *testing.T) {
	fc := withFake(t)
	ctx := context.Background()

	if _, err := CacheAside(ctx, "k", 0, func(context.Context) ([]byte, error) {
		return []byte("v"), nil
	}); err != nil {
		t.Fatalf("err: %v", err)
	}
	if fc.setCalls.Load() != 0 {
		t.Errorf("Set called %d times with ttl=0; want 0", fc.setCalls.Load())
	}
}

// TestCacheAside_EmptyPayload_SkipsWrite — never store an empty body. An
// empty marshal output is almost always a bug; caching it would lock in
// the bug for the TTL window.
func TestCacheAside_EmptyPayload_SkipsWrite(t *testing.T) {
	fc := withFake(t)
	ctx := context.Background()
	if _, err := CacheAside(ctx, "k", time.Minute, func(context.Context) ([]byte, error) {
		return nil, nil
	}); err != nil {
		t.Fatalf("err: %v", err)
	}
	if fc.setCalls.Load() != 0 {
		t.Errorf("Set called %d times with empty payload; want 0", fc.setCalls.Load())
	}
}

// TestSetDefault_NilFallsBackToNoop verifies the documented contract:
// SetDefault(nil) installs a no-op so subsequent Get/Set calls never panic.
func TestSetDefault_NilFallsBackToNoop(t *testing.T) {
	prev := GetDefault()
	t.Cleanup(func() { SetDefault(prev) })

	SetDefault(nil)
	c := GetDefault()
	if c == nil {
		t.Fatal("GetDefault returned nil after SetDefault(nil)")
	}
	c.Set(context.Background(), "k", []byte("v"), time.Minute)
	if _, ok := c.Get(context.Background(), "k"); ok {
		t.Error("noop fallback should miss")
	}
}

// TestPackageHelpers_DelegateToDefault wires the package-level Get / Set /
// Delete / DeletePattern through the fake cache and asserts they reach it.
func TestPackageHelpers_DelegateToDefault(t *testing.T) {
	fc := withFake(t)
	ctx := context.Background()

	Set(ctx, "k", []byte("v"), time.Minute)
	if got, ok := Get(ctx, "k"); !ok || string(got) != "v" {
		t.Errorf("Get after Set = (%q,%v)", got, ok)
	}
	Delete(ctx, "k")
	if _, ok := Get(ctx, "k"); ok {
		t.Error("Delete had no effect")
	}
	DeletePattern(ctx, "k:*")
	if got := fc.patCalls.Load(); got != 1 {
		t.Errorf("DeletePattern not delegated; calls=%d", got)
	}
}

// TestCloseDefault_ResetsToNoop ensures CloseDefault leaves the package in
// a usable state — calling Get afterwards must not panic.
func TestCloseDefault_ResetsToNoop(t *testing.T) {
	prev := GetDefault()
	t.Cleanup(func() { SetDefault(prev) })

	SetDefault(newFakeCache())
	CloseDefault()
	if _, ok := Get(context.Background(), "any"); ok {
		t.Error("post-close cache served a hit; expected no-op")
	}
}
