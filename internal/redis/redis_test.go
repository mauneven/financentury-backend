package redis

import (
	"context"
	"testing"
	"time"
)

// TestNew_NoOpWhenURLEmpty asserts the documented contract: an empty
// REDIS_URL must produce a fully-functional no-op cache so callers don't
// need to nil-check the result.
func TestNew_NoOpWhenURLEmpty(t *testing.T) {
	c := New("")
	if c == nil {
		t.Fatal("New(\"\") returned nil; expected no-op cache")
	}

	ctx := context.Background()

	// Get on an empty cache always misses.
	if v, ok := c.Get(ctx, "any-key"); ok || v != nil {
		t.Errorf("noop Get returned (%q, %v); want (nil, false)", v, ok)
	}

	// Set / Delete must not panic and must not change subsequent Get behaviour.
	c.Set(ctx, "k", []byte("v"), time.Minute)
	if _, ok := c.Get(ctx, "k"); ok {
		t.Error("noop Set should not store a value")
	}
	c.Delete(ctx, "k", "k2")
	c.DeletePattern(ctx, "k*")

	c.Close()
}

// TestNew_NoOpWhenURLInvalid asserts that a malformed URL falls back to the
// no-op cache instead of crashing the program at startup.
func TestNew_NoOpWhenURLInvalid(t *testing.T) {
	c := New("not a valid url ::::")
	if c == nil {
		t.Fatal("New invalid URL returned nil")
	}
	if _, ok := c.Get(context.Background(), "k"); ok {
		t.Error("noop fallback should miss")
	}
}

// TestNoopCache_AllMethodsSafe tickles every method of the no-op cache to
// guarantee none of them dereference a nil pointer.
func TestNoopCache_AllMethodsSafe(t *testing.T) {
	var c Cache = noopCache{}
	ctx := context.Background()

	// Get
	if _, ok := c.Get(ctx, ""); ok {
		t.Error("noop Get returned hit")
	}
	// Set with several TTL edges
	c.Set(ctx, "", nil, 0)
	c.Set(ctx, "k", []byte("v"), -1*time.Second)
	c.Set(ctx, "k", []byte("v"), time.Hour)
	// Delete with no keys, single key, multiple keys
	c.Delete(ctx)
	c.Delete(ctx, "a")
	c.Delete(ctx, "a", "b", "c")
	// Pattern
	c.DeletePattern(ctx, "")
	c.DeletePattern(ctx, "*")
	// Close (twice)
	c.Close()
	c.Close()
}

// TestRedisCache_NilReceiver_NoPanic asserts that a redisCache whose client
// is nil (e.g. produced by a partially-failed New) never panics. The
// production Cache surface is supposed to degrade silently — this test
// pins that contract so a refactor doesn't accidentally re-introduce a
// nil-pointer panic.
func TestRedisCache_NilReceiver_NoPanic(t *testing.T) {
	var rc *redisCache
	ctx := context.Background()
	if _, ok := rc.Get(ctx, "k"); ok {
		t.Error("nil receiver should miss")
	}
	rc.Set(ctx, "k", []byte("v"), time.Minute)
	rc.Delete(ctx, "k")
	rc.DeletePattern(ctx, "k*")
	rc.Close()
}

// TestNew_Optional_DoesNotBlockStartup verifies that even with a clearly
// unreachable URL we still return a cache (the no-op variant) instead of
// blocking the goroutine indefinitely. This is the property that lets
// `main.go` initialise Redis on every boot without an `if cfg.RedisURL ==
// ""` guard.
func TestNew_Optional_DoesNotBlockStartup(t *testing.T) {
	t.Parallel()
	// A localhost port we are not listening on. ParseURL will succeed,
	// the eager Ping will fail (within DialTimeout=1s).
	done := make(chan struct{})
	go func() {
		c := New("redis://127.0.0.1:1") // port 1 is privileged; nothing listens there in tests
		_ = c
		close(done)
	}()
	select {
	case <-done:
		// fine
	case <-time.After(5 * time.Second):
		t.Fatal("New blocked > 5s on unreachable Redis; should fall back to no-op fast")
	}
}

// TestCache_TableDriven_NoopBehaviour exhaustively covers the no-op
// implementation's expected return values. Table-driven so adding a new
// edge case is one line.
func TestCache_TableDriven_NoopBehaviour(t *testing.T) {
	t.Parallel()
	c := New("") // forces no-op

	cases := []struct {
		name  string
		op    func(t *testing.T)
		check func(t *testing.T)
	}{
		{
			name: "Get-empty-key",
			op:   func(t *testing.T) {},
			check: func(t *testing.T) {
				if v, ok := c.Get(context.Background(), ""); ok || v != nil {
					t.Errorf("Get(\"\") = (%v,%v), want (nil,false)", v, ok)
				}
			},
		},
		{
			name: "Set-then-Get-misses",
			op:   func(t *testing.T) { c.Set(context.Background(), "k1", []byte("v"), time.Minute) },
			check: func(t *testing.T) {
				if _, ok := c.Get(context.Background(), "k1"); ok {
					t.Error("noop Set was observable on Get")
				}
			},
		},
		{
			name: "Set-zero-ttl-noop",
			op:   func(t *testing.T) { c.Set(context.Background(), "k2", []byte("v"), 0) },
			check: func(t *testing.T) {
				if _, ok := c.Get(context.Background(), "k2"); ok {
					t.Error("noop Set with zero TTL was observable")
				}
			},
		},
		{
			name: "Delete-nothing-safe",
			op:   func(t *testing.T) { c.Delete(context.Background()) },
			check: func(t *testing.T) {
				// no-op contract: no panic, no error surface
			},
		},
		{
			name: "DeletePattern-empty-safe",
			op:   func(t *testing.T) { c.DeletePattern(context.Background(), "") },
			check: func(t *testing.T) {
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.op(t)
			tc.check(t)
		})
	}
}
