package redis

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestRedis_LiveRoundTrip exercises Set / Get / TTL expiry / Delete / Pattern
// delete against a REAL Redis instance. Skips when REDIS_URL is unset so CI
// remains happy. Run locally with:
//
//	REDIS_URL=redis://localhost:6379/0 go test ./internal/redis/... -run TestRedis_Live -v
func TestRedis_LiveRoundTrip(t *testing.T) {
	url := os.Getenv("REDIS_URL")
	if url == "" {
		t.Skip("REDIS_URL not set; skipping live integration test")
	}

	cache := New(url)
	defer cache.Close()

	ctx := context.Background()
	key := "_test:fws:" + t.Name()
	defer cache.Delete(ctx, key)

	// Miss.
	if _, ok := cache.Get(ctx, key); ok {
		t.Fatal("expected miss on fresh key")
	}

	// Set + hit.
	cache.Set(ctx, key, []byte("hello"), 5*time.Second)
	got, ok := cache.Get(ctx, key)
	if !ok || string(got) != "hello" {
		t.Fatalf("expected hit with %q, got ok=%v val=%q", "hello", ok, string(got))
	}

	// Delete.
	cache.Delete(ctx, key)
	if _, ok := cache.Get(ctx, key); ok {
		t.Fatal("expected miss after delete")
	}

	// Short-TTL expiry.
	cache.Set(ctx, key, []byte("ephemeral"), 600*time.Millisecond)
	time.Sleep(900 * time.Millisecond)
	if _, ok := cache.Get(ctx, key); ok {
		t.Fatal("expected miss after TTL expiry")
	}

	// Pattern delete.
	cache.Set(ctx, "_test:fws:pat:a", []byte("1"), 5*time.Second)
	cache.Set(ctx, "_test:fws:pat:b", []byte("2"), 5*time.Second)
	cache.DeletePattern(ctx, "_test:fws:pat:*")
	if _, ok := cache.Get(ctx, "_test:fws:pat:a"); ok {
		t.Error("expected pattern delete to drop key a")
	}
	if _, ok := cache.Get(ctx, "_test:fws:pat:b"); ok {
		t.Error("expected pattern delete to drop key b")
	}
}
