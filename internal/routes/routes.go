package routes

import (
	"github.com/gofiber/fiber/v2"
	"github.com/the-financial-workspace/backend/internal/handlers"
	"github.com/the-financial-workspace/backend/internal/middleware"
)

// Setup registers all API routes on the Fiber app.
func Setup(app *fiber.App) {
	api := app.Group("/api")

	// Public auth routes with strict rate limiting to prevent brute-force.
	api.Post("/auth/google", middleware.AuthRateLimiter(), handlers.GoogleLogin)

	// Public invite info (no auth needed).
	api.Get("/invites/:token", handlers.GetInviteInfo)

	// Protected routes.
	protected := api.Group("", middleware.Protected())

	// Auth routes (protected -- requires valid JWT).
	protected.Get("/auth/me", handlers.Me)

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

	// Category routes.
	budgets.Get("/:id/categories", handlers.ListCategories)
	budgets.Post("/:id/categories", handlers.CreateCategory)
	budgets.Put("/:id/categories/:catId", handlers.UpdateCategory)
	budgets.Delete("/:id/categories/:catId", handlers.DeleteCategory)

	// Subcategory routes.
	budgets.Post("/:id/categories/:catId/subcategories", handlers.CreateSubcategory)
	budgets.Put("/:id/categories/:catId/subcategories/:subId", handlers.UpdateSubcategory)
	budgets.Delete("/:id/categories/:catId/subcategories/:subId", handlers.DeleteSubcategory)

	// Expense routes.
	budgets.Get("/:id/expenses", handlers.ListExpenses)
	budgets.Post("/:id/expenses", handlers.CreateExpense)
	budgets.Put("/:id/expenses/:expenseId", handlers.UpdateExpense)
	budgets.Delete("/:id/expenses/:expenseId", handlers.DeleteExpense)

	// Summary & Trends routes.
	budgets.Get("/:id/summary", handlers.GetBudgetSummary)
	budgets.Get("/:id/trends", handlers.GetBudgetTrends)
}
