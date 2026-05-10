package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v2"
)

// TestCacheControlAndETag_304OnMatch wires the middleware onto a minimal
// Fiber app, issues a GET to populate the ETag, then replays the request
// with the captured ETag in If-None-Match and asserts a 304 with an empty
// body plus preserved Cache-Control + ETag + Vary headers.
func TestCacheControlAndETag_304OnMatch(t *testing.T) {
	app := fiber.New()
	app.Use(cacheControlAndETag())
	app.Get("/api/budgets/abc/summary", func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{"income": 100, "expenses": 42})
	})

	// First request — establishes the ETag.
	req := httptest.NewRequest(http.MethodGet, "/api/budgets/abc/summary", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("initial request failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("initial status = %d, want 200", resp.StatusCode)
	}
	etag := resp.Header.Get("ETag")
	if etag == "" {
		t.Fatal("missing ETag on initial response")
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "private, max-age=10, stale-while-revalidate=30" {
		t.Errorf("Cache-Control = %q", cc)
	}
	if vary := resp.Header.Get("Vary"); vary != "Authorization, Accept-Encoding" {
		t.Errorf("Vary = %q, want both Authorization and Accept-Encoding", vary)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()

	// Replay with If-None-Match — expect 304 + empty body + headers preserved.
	req2 := httptest.NewRequest(http.MethodGet, "/api/budgets/abc/summary", nil)
	req2.Header.Set("If-None-Match", etag)
	resp2, err := app.Test(req2)
	if err != nil {
		t.Fatalf("conditional request failed: %v", err)
	}
	if resp2.StatusCode != http.StatusNotModified {
		t.Fatalf("status = %d, want 304", resp2.StatusCode)
	}
	body, _ := io.ReadAll(resp2.Body)
	_ = resp2.Body.Close()
	if len(body) != 0 {
		t.Errorf("304 body length = %d, want 0", len(body))
	}
	if got := resp2.Header.Get("ETag"); got != etag {
		t.Errorf("ETag on 304 = %q, want %q", got, etag)
	}
	if cc := resp2.Header.Get("Cache-Control"); cc != "private, max-age=10, stale-while-revalidate=30" {
		t.Errorf("Cache-Control on 304 = %q", cc)
	}
}

// TestCacheControlAndETag_SkipsWriteMethods ensures POST responses never
// receive ETag or Cache-Control, even on a whitelisted path.
func TestCacheControlAndETag_SkipsWriteMethods(t *testing.T) {
	app := fiber.New()
	app.Use(cacheControlAndETag())
	app.Post("/api/budgets/abc/expenses", func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{"ok": true})
	})

	req := httptest.NewRequest(http.MethodPost, "/api/budgets/abc/expenses", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.Header.Get("ETag") != "" {
		t.Error("POST response should not carry ETag")
	}
	if resp.Header.Get("Cache-Control") != "" {
		t.Error("POST response should not carry Cache-Control")
	}
}

// TestCacheControlAndETag_SkipsExcludedSubpaths covers the access-control
// subpaths that must always hit the DB fresh.
func TestCacheControlAndETag_SkipsExcludedSubpaths(t *testing.T) {
	app := fiber.New()
	app.Use(cacheControlAndETag())
	app.Get("/api/budgets/abc/invites", func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{"invites": []string{}})
	})
	app.Get("/api/budgets/abc/collaborators", func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{"collaborators": []string{}})
	})

	for _, path := range []string{
		"/api/budgets/abc/invites",
		"/api/budgets/abc/collaborators",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		resp, err := app.Test(req)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		_ = resp.Body.Close()
		if resp.Header.Get("ETag") != "" {
			t.Errorf("%s: expected no ETag (access-control changes rapidly)", path)
		}
		if resp.Header.Get("Cache-Control") != "" {
			t.Errorf("%s: expected no Cache-Control", path)
		}
	}
}
