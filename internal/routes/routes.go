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
	api.Post("/auth/register", middleware.AuthRateLimiter(), handlers.Register)
	api.Post("/auth/login", middleware.AuthRateLimiter(), handlers.Login)

	// Public invite info (no auth needed).
	api.Get("/invites/:token", handlers.GetInviteInfo)

	// Protected routes.
	protected := api.Group("", middleware.Protected(), middleware.APIRateLimiter())

	// Auth routes (protected -- requires valid JWT).
	protected.Get("/auth/me", handlers.Me)
	protected.Delete("/auth/account", handlers.DeleteAccount)

	// Migration route with strict rate limiting since it is a heavy operation.
	protected.Post("/migrate", middleware.MigrateRateLimiter(), handlers.Migrate)

	// Protected invite routes.
	protected.Post("/invites/:token/accept", handlers.AcceptInvite)

	// Budget routes.
	budgets := protected.Group("/budgets")
	budgets.Get("/", handlers.ListBudgets)
	budgets.Post("/", handlers.CreateBudget)
	budgets.Get("/:id", handlers.GetBudget)
	budgets.Put("/:id", handlers.UpdateBudget)
	budgets.Delete("/:id", handlers.DeleteBudget)

	// Invite and collaborator routes.
	budgets.Post("/:id/invite", handlers.CreateInvite)
	budgets.Get("/:id/collaborators", handlers.ListCollaborators)
	budgets.Delete("/:id/collaborators/:userId", handlers.RemoveCollaborator)

	// Section routes.
	budgets.Get("/:id/sections", handlers.ListSections)
	budgets.Post("/:id/sections", handlers.CreateSection)
	budgets.Put("/:id/sections/:sectionId", handlers.UpdateSection)
	budgets.Delete("/:id/sections/:sectionId", handlers.DeleteSection)

	// Category routes (nested under sections).
	budgets.Post("/:id/sections/:sectionId/categories", handlers.CreateCategory)
	budgets.Put("/:id/sections/:sectionId/categories/:catId", handlers.UpdateCategory)
	budgets.Delete("/:id/sections/:sectionId/categories/:catId", handlers.DeleteCategory)

	// Expense routes.
	budgets.Get("/:id/expenses", handlers.ListExpenses)
	budgets.Post("/:id/expenses", handlers.CreateExpense)
	budgets.Put("/:id/expenses/:expenseId", handlers.UpdateExpense)
	budgets.Delete("/:id/expenses/:expenseId", handlers.DeleteExpense)

	// Summary & Trends routes.
	budgets.Get("/:id/summary", handlers.GetBudgetSummary)
	budgets.Get("/:id/trends", handlers.GetBudgetTrends)
}
