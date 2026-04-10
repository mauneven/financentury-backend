package middleware

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/gofiber/fiber/v2"
)

// ==================== AuthRateLimiter ====================

func TestAuthRateLimiter_AllowsRequestsUnderLimit(t *testing.T) {
	app := fiber.New()
	app.Use(AuthRateLimiter())
	app.Get("/auth", func(c *fiber.Ctx) error {
		return c.SendString("OK")
	})

	// AuthRateLimiter allows 10 requests per minute.
	for i := 0; i < 10; i++ {
		req := httptest.NewRequest(http.MethodGet, "/auth", nil)
		resp, err := app.Test(req)
		if err != nil {
			t.Fatalf("request %d failed: %v", i, err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Errorf("request %d: expected 200, got %d", i, resp.StatusCode)
		}
	}
}

func TestAuthRateLimiter_BlocksAfterExceedingLimit(t *testing.T) {
	app := fiber.New()
	app.Use(AuthRateLimiter())
	app.Get("/auth", func(c *fiber.Ctx) error {
		return c.SendString("OK")
	})

	// Exhaust the 10-request limit.
	for i := 0; i < 10; i++ {
		req := httptest.NewRequest(http.MethodGet, "/auth", nil)
		app.Test(req)
	}

	// The 11th request should be rate limited.
	req := httptest.NewRequest(http.MethodGet, "/auth", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Errorf("expected 429, got %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	if len(body) == 0 {
		t.Error("rate limit response body should not be empty")
	}
}

// ==================== APIRateLimiter ====================

func TestAPIRateLimiter_AllowsRequestsUnderLimit(t *testing.T) {
	app := fiber.New()
	app.Use(APIRateLimiter())
	app.Get("/api", func(c *fiber.Ctx) error {
		return c.SendString("OK")
	})

	// APIRateLimiter allows 100 requests per minute; test a subset.
	for i := 0; i < 50; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api", nil)
		resp, err := app.Test(req)
		if err != nil {
			t.Fatalf("request %d failed: %v", i, err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Errorf("request %d: expected 200, got %d", i, resp.StatusCode)
		}
	}
}

func TestAPIRateLimiter_BlocksAfterExceedingLimit(t *testing.T) {
	app := fiber.New()
	app.Use(APIRateLimiter())
	app.Get("/api", func(c *fiber.Ctx) error {
		return c.SendString("OK")
	})

	// Exhaust the 100-request limit.
	for i := 0; i < 100; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api", nil)
		app.Test(req)
	}

	// The 101st request should be rate limited.
	req := httptest.NewRequest(http.MethodGet, "/api", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Errorf("expected 429, got %d", resp.StatusCode)
	}
}

// ==================== MigrateRateLimiter ====================

func TestMigrateRateLimiter_AllowsRequestsUnderLimit(t *testing.T) {
	app := fiber.New()
	app.Use(MigrateRateLimiter())
	app.Post("/migrate", func(c *fiber.Ctx) error {
		return c.SendString("OK")
	})

	// MigrateRateLimiter allows 5 requests per minute.
	for i := 0; i < 5; i++ {
		req := httptest.NewRequest(http.MethodPost, "/migrate", nil)
		resp, err := app.Test(req)
		if err != nil {
			t.Fatalf("request %d failed: %v", i, err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Errorf("request %d: expected 200, got %d", i, resp.StatusCode)
		}
	}
}

func TestMigrateRateLimiter_BlocksAfterExceedingLimit(t *testing.T) {
	app := fiber.New()
	app.Use(MigrateRateLimiter())
	app.Post("/migrate", func(c *fiber.Ctx) error {
		return c.SendString("OK")
	})

	// Exhaust the 5-request limit.
	for i := 0; i < 5; i++ {
		req := httptest.NewRequest(http.MethodPost, "/migrate", nil)
		app.Test(req)
	}

	// The 6th request should be rate limited.
	req := httptest.NewRequest(http.MethodPost, "/migrate", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Errorf("expected 429, got %d", resp.StatusCode)
	}
}

// ==================== Different Limiters Have Different Limits ====================

func TestRateLimiters_HaveDifferentLimits(t *testing.T) {
	// Verify that Migrate (5) < Auth (10) < API (100) by checking
	// that Migrate blocks at 6 while Auth still allows.

	migrateApp := fiber.New()
	migrateApp.Use(MigrateRateLimiter())
	migrateApp.Get("/m", func(c *fiber.Ctx) error { return c.SendString("OK") })

	authApp := fiber.New()
	authApp.Use(AuthRateLimiter())
	authApp.Get("/a", func(c *fiber.Ctx) error { return c.SendString("OK") })

	// Send 6 requests to each.
	for i := 0; i < 6; i++ {
		req := httptest.NewRequest(http.MethodGet, "/m", nil)
		migrateApp.Test(req)
		req2 := httptest.NewRequest(http.MethodGet, "/a", nil)
		authApp.Test(req2)
	}

	// Migrate should be blocked on the 7th.
	mReq := httptest.NewRequest(http.MethodGet, "/m", nil)
	mResp, _ := migrateApp.Test(mReq)

	// Auth should still allow the 7th (limit is 10).
	aReq := httptest.NewRequest(http.MethodGet, "/a", nil)
	aResp, _ := authApp.Test(aReq)

	if mResp.StatusCode != http.StatusTooManyRequests {
		t.Errorf("migrate should be blocked after 6 requests, got %d", mResp.StatusCode)
	}
	if aResp.StatusCode != http.StatusOK {
		t.Errorf("auth should still allow after 6 requests, got %d", aResp.StatusCode)
	}
}

// ==================== Concurrent Access Safety ====================

func TestAuthRateLimiter_ConcurrentAccess(t *testing.T) {
	app := fiber.New()
	app.Use(AuthRateLimiter())
	app.Get("/concurrent", func(c *fiber.Ctx) error {
		return c.SendString("OK")
	})

	// Fire 20 concurrent requests. No panics should occur.
	var wg sync.WaitGroup
	results := make([]int, 20)

	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodGet, "/concurrent", nil)
			resp, err := app.Test(req)
			if err != nil {
				results[idx] = -1
				return
			}
			results[idx] = resp.StatusCode
		}(i)
	}

	wg.Wait()

	okCount := 0
	rateLimitedCount := 0
	for _, code := range results {
		switch code {
		case http.StatusOK:
			okCount++
		case http.StatusTooManyRequests:
			rateLimitedCount++
		case -1:
			t.Error("a concurrent request returned an error")
		}
	}

	// With 20 requests and a limit of 10, we expect some to succeed and some
	// to be rate limited.
	if okCount == 0 {
		t.Error("expected at least some requests to succeed")
	}
	if okCount > 10 {
		t.Errorf("expected at most 10 OK responses, got %d", okCount)
	}
	t.Logf("concurrent test: %d OK, %d rate-limited", okCount, rateLimitedCount)
}
