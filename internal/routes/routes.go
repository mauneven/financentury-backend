package routes

import (
	"github.com/gofiber/fiber/v2"

	"github.com/the-financial-workspace/backend/internal/captcha"
	"github.com/the-financial-workspace/backend/internal/handlers"
	"github.com/the-financial-workspace/backend/internal/middleware"
)

// SetupConfig wires per-route captcha verifiers + future configuration
// without forcing callers to plumb every knob through Setup's signature.
type SetupConfig struct {
	// Captcha is the platform-aware verifier used by /api/auth/* and the
	// public invite routes. nil → no-op (dev / staging without secrets).
	Captcha captcha.Verifier
}

// Setup is the legacy entry point. Defaults all knobs to no-op behaviour
// so existing tests keep compiling against a one-arg API.
func Setup(app *fiber.App) {
	SetupWith(app, SetupConfig{})
}

// SetupWith registers all API routes on the Fiber app with explicit
// captcha + (future) per-feature configuration.
func SetupWith(app *fiber.App, cfg SetupConfig) {
	// WebSocket endpoint (before API group to avoid prefix conflicts).
	// Authentication is handled inside the WebSocket handler via first message.
	app.Use("/ws", handlers.WebSocketUpgrade())
	app.Get("/ws", handlers.WebSocketHandler())

	api := app.Group("/api")

	// SECURITY: captcha gates the brute-force / abuse surface. The
	// middleware short-circuits with 403 on a failed verification, so it
	// must run BEFORE the rate limiter (otherwise bots burn through the
	// 10/min/IP budget before captcha gets a say) and BEFORE the actual
	// handler (which performs the expensive bcrypt / OAuth round-trip).
	authCaptcha := middleware.Captcha(middleware.CaptchaConfig{
		Verifier: cfg.Captcha,
		Action:   "auth",
	})
	inviteCaptcha := middleware.Captcha(middleware.CaptchaConfig{
		Verifier: cfg.Captcha,
		Action:   "invite",
	})

	// Public auth routes with strict rate limiting to prevent brute-force.
	api.Post("/auth/google", authCaptcha, middleware.AuthRateLimiter(), handlers.GoogleLogin)
	api.Post("/auth/google/mobile", authCaptcha, middleware.AuthRateLimiter(), handlers.GoogleMobileLogin)
	// Email login/register handlers exist (auth_email.go) but aren't
	// mounted yet — captcha is wired so when those routes go live no
	// follow-up change is required.

	// Bootstrap endpoints for App Attest. No captcha (this IS the
	// bootstrap). Rate-limit at the proxy layer.
	api.Get("/attest/challenge", middleware.AuthRateLimiter(), handlers.AttestChallenge)
	api.Post("/attest/register", middleware.AuthRateLimiter(), handlers.AttestRegister)

	// Public invite info (no auth needed) — captcha guards against
	// token-enumeration attacks.
	api.Get("/invites/:token", inviteCaptcha, handlers.GetInviteInfo)

	// Protected routes.
	protected := api.Group("", middleware.Protected(), middleware.APIRateLimiter())

	// Auth routes (protected -- requires valid JWT).
	protected.Get("/auth/me", handlers.Me)
	protected.Patch("/auth/profile", handlers.UpdateProfile)
	protected.Delete("/auth/account", handlers.DeleteAccount)

	// Session routes.
	protected.Get("/auth/sessions", handlers.ListSessions)
	protected.Delete("/auth/sessions/:sessionId", handlers.RevokeSession)
	protected.Post("/auth/sign-out", handlers.SignOut)

	// Migration route with strict rate limiting since it is a heavy operation.
	protected.Post("/migrate", middleware.MigrateRateLimiter(), handlers.Migrate)

	// Protected invite routes. Even though the request is auth'd, captcha
	// adds an attest signal that defends against replayed invite tokens
	// pulled from leaked JWTs.
	protected.Post("/invites/:token/accept", inviteCaptcha, handlers.AcceptInvite)

	// Display order route (save only — read is bundled in /auth/me).
	protected.Put("/display-orders", handlers.SaveDisplayOrder)

	// Budget routes.
	budgets := protected.Group("/budgets")
	budgets.Get("/", handlers.ListBudgets)
	budgets.Post("/", handlers.CreateBudget)
	budgets.Get("/:id", handlers.GetBudget)
	budgets.Put("/:id", handlers.UpdateBudget)
	budgets.Delete("/:id", handlers.DeleteBudget)

	// Invite and collaborator routes.
	budgets.Get("/:id/invites", handlers.ListInvites)
	budgets.Post("/:id/invite", handlers.CreateInvite)
	budgets.Get("/:id/collaborators", handlers.ListCollaborators)
	budgets.Delete("/:id/collaborators/:userId", handlers.RemoveCollaborator)

	// Category routes (flat: Budget -> Category, max 50 per budget).
	budgets.Get("/:id/categories", handlers.ListCategories)
	budgets.Post("/:id/categories", handlers.CreateCategory)
	budgets.Patch("/:id/categories/:catId", handlers.UpdateCategory)
	budgets.Delete("/:id/categories/:catId", handlers.DeleteCategory)

	// Expense routes.
	budgets.Get("/:id/expenses", handlers.ListExpenses)
	budgets.Post("/:id/expenses", handlers.CreateExpense)
	budgets.Put("/:id/expenses/:expenseId", handlers.UpdateExpense)
	budgets.Delete("/:id/expenses/:expenseId", handlers.DeleteExpense)

	// Budget link routes.
	budgets.Get("/:id/links", handlers.ListLinks)
	budgets.Post("/:id/links", handlers.CreateLink)
	budgets.Patch("/:id/links/:linkId", handlers.UpdateLink)
	budgets.Delete("/:id/links/:linkId", handlers.DeleteLink)
	budgets.Get("/:id/linkable", handlers.GetLinkableBudgets)

	// Summary, Trends & Budget Resume routes.
	budgets.Get("/:id/summary", handlers.GetBudgetSummary)
	budgets.Get("/:id/trends", handlers.GetBudgetTrends)
	budgets.Get("/:id/budget-resume", handlers.GetBudgetResume)

	// PERF: aggregate dashboard endpoint. Returns summary+expenses+trends
	// +resume in one envelope so clients save 3 RTTs on every dashboard
	// mount. Each sub-builder still uses the in-process cache + singleflight,
	// so a hot dashboard mount is O(1) DB queries.
	budgets.Get("/:id/dashboard", handlers.GetBudgetDashboard)
}
