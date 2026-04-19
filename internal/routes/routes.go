package routes

import (
	"github.com/gofiber/fiber/v2"
	"github.com/the-financial-workspace/backend/internal/handlers"
	"github.com/the-financial-workspace/backend/internal/middleware"
)

// Setup registers all API routes on the Fiber app, including the WebSocket
// endpoint for real-time updates.
func Setup(app *fiber.App) {
	// WebSocket endpoint (before API group to avoid prefix conflicts).
	// Authentication is handled inside the WebSocket handler via first message.
	app.Use("/ws", handlers.WebSocketUpgrade())
	app.Get("/ws", handlers.WebSocketHandler())

	api := app.Group("/api")

	// Public auth routes with strict rate limiting to prevent brute-force.
	api.Post("/auth/google", middleware.AuthRateLimiter(), handlers.GoogleLogin)
	api.Post("/auth/google/mobile", middleware.AuthRateLimiter(), handlers.GoogleMobileLogin)

	// Public invite info (no auth needed).
	api.Get("/invites/:token", handlers.GetInviteInfo)

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

	// Protected invite routes.
	protected.Post("/invites/:token/accept", handlers.AcceptInvite)

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
}
