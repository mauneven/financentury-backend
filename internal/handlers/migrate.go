package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/the-financial-workspace/backend/internal/database"
	"github.com/the-financial-workspace/backend/internal/middleware"
	"github.com/the-financial-workspace/backend/internal/models"
)

// --- Migration Request Types ---

// MigrateRequest is the top-level migration payload.
type MigrateRequest struct {
	Budgets []MigrateBudget `json:"budgets"`
}

// MigrateBudget represents a budget to migrate.
type MigrateBudget struct {
	Name                string            `json:"name"`
	MonthlyIncome       float64           `json:"monthly_income"`
	Currency            string            `json:"currency"`
	BillingPeriodMonths int               `json:"billing_period_months"`
	Mode                string            `json:"mode"`
	Categories          []MigrateCategory `json:"categories"`
	Expenses            []MigrateExpense  `json:"expenses"`
}

// MigrateCategory represents a category to migrate.
type MigrateCategory struct {
	Name              string               `json:"name"`
	AllocationPercent float64              `json:"allocation_percent"`
	Icon              string               `json:"icon"`
	SortOrder         int                  `json:"sort_order"`
	LocalID           string               `json:"local_id"`
	Subcategories     []MigrateSubcategory `json:"subcategories"`
}

// MigrateSubcategory represents a subcategory to migrate.
type MigrateSubcategory struct {
	Name              string  `json:"name"`
	AllocationPercent float64 `json:"allocation_percent"`
	Icon              string  `json:"icon"`
	SortOrder         int     `json:"sort_order"`
	LocalID           string  `json:"local_id"`
}

// MigrateExpense represents an expense to migrate.
type MigrateExpense struct {
	LocalSubcategoryID string  `json:"local_subcategory_id"`
	Amount             float64 `json:"amount"`
	Description        string  `json:"description"`
	ExpenseDate        string  `json:"expense_date"`
}

// Migrate handles POST /api/migrate (protected).
// It imports budgets, categories, subcategories, and expenses from local data.
func Migrate(c *fiber.Ctx) error {
	userID := middleware.GetUserID(c)
	if userID == uuid.Nil {
		return c.Status(fiber.StatusUnauthorized).JSON(models.ErrorResponse{Error: "unauthorized"})
	}

	var req MigrateRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(models.ErrorResponse{
			Error: "invalid request body",
		})
	}

	if len(req.Budgets) == 0 {
		return c.Status(fiber.StatusBadRequest).JSON(models.ErrorResponse{
			Error: "at least one budget is required",
		})
	}

	var createdBudgets []models.Budget

	for _, mb := range req.Budgets {
		now := time.Now().UTC()
		budgetID := uuid.New()

		// Default values.
		if mb.Currency == "" {
			mb.Currency = "COP"
		}
		if mb.BillingPeriodMonths <= 0 {
			mb.BillingPeriodMonths = 1
		}
		if mb.Mode == "" {
			mb.Mode = "manual"
		}

		// Create the budget.
		budgetPayload := map[string]interface{}{
			"id":                    budgetID.String(),
			"user_id":               userID.String(),
			"name":                  mb.Name,
			"monthly_income":        mb.MonthlyIncome,
			"currency":              mb.Currency,
			"billing_period_months": mb.BillingPeriodMonths,
			"mode":                  mb.Mode,
			"created_at":            now.Format(time.RFC3339Nano),
			"updated_at":            now.Format(time.RFC3339Nano),
		}

		budgetBytes, err := json.Marshal(budgetPayload)
		if err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{
				Error: "failed to serialize budget",
			})
		}

		_, statusCode, err := database.DB.Post("budgets", budgetBytes)
		if err != nil || statusCode != http.StatusCreated {
			return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{
				Error: fmt.Sprintf("failed to create budget: %s", mb.Name),
			})
		}

		// Build localID -> real UUID map for subcategories.
		subLocalIDMap := make(map[string]uuid.UUID)

		// Create categories and subcategories.
		for _, mc := range mb.Categories {
			catID := uuid.New()

			catPayload := map[string]interface{}{
				"id":                 catID.String(),
				"budget_id":          budgetID.String(),
				"name":               mc.Name,
				"allocation_percent": mc.AllocationPercent,
				"icon":               mc.Icon,
				"sort_order":         mc.SortOrder,
				"created_at":         now.Format(time.RFC3339Nano),
			}

			catBytes, err := json.Marshal(catPayload)
			if err != nil {
				return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{
					Error: "failed to serialize category",
				})
			}

			_, statusCode, err := database.DB.Post("budget_categories", catBytes)
			if err != nil || statusCode != http.StatusCreated {
				return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{
					Error: fmt.Sprintf("failed to create category: %s", mc.Name),
				})
			}

			// Create subcategories.
			for _, ms := range mc.Subcategories {
				subID := uuid.New()

				// Map the local ID to the real UUID.
				if ms.LocalID != "" {
					subLocalIDMap[ms.LocalID] = subID
				}

				subPayload := map[string]interface{}{
					"id":                 subID.String(),
					"category_id":        catID.String(),
					"name":               ms.Name,
					"allocation_percent": ms.AllocationPercent,
					"icon":               ms.Icon,
					"sort_order":         ms.SortOrder,
					"created_at":         now.Format(time.RFC3339Nano),
				}

				subBytes, err := json.Marshal(subPayload)
				if err != nil {
					return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{
						Error: "failed to serialize subcategory",
					})
				}

				_, statusCode, err := database.DB.Post("budget_subcategories", subBytes)
				if err != nil || statusCode != http.StatusCreated {
					return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{
						Error: fmt.Sprintf("failed to create subcategory: %s", ms.Name),
					})
				}
			}
		}

		// Create expenses using the localID -> real UUID map.
		for _, me := range mb.Expenses {
			realSubID, ok := subLocalIDMap[me.LocalSubcategoryID]
			if !ok {
				// Skip expenses with unknown subcategory references.
				continue
			}

			expenseID := uuid.New()
			expenseDate := me.ExpenseDate
			if expenseDate == "" {
				expenseDate = now.Format("2006-01-02")
			}

			expPayload := map[string]interface{}{
				"id":             expenseID.String(),
				"budget_id":      budgetID.String(),
				"subcategory_id": realSubID.String(),
				"amount":         me.Amount,
				"description":    me.Description,
				"expense_date":   expenseDate,
				"created_at":     now.Format(time.RFC3339Nano),
			}

			expBytes, err := json.Marshal(expPayload)
			if err != nil {
				return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{
					Error: "failed to serialize expense",
				})
			}

			_, statusCode, err := database.DB.Post("budget_expenses", expBytes)
			if err != nil || statusCode != http.StatusCreated {
				return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{
					Error: fmt.Sprintf("failed to create expense: %s", me.Description),
				})
			}
		}

		createdBudgets = append(createdBudgets, models.Budget{
			ID:                  budgetID,
			UserID:              userID,
			Name:                mb.Name,
			MonthlyIncome:       mb.MonthlyIncome,
			Currency:            mb.Currency,
			BillingPeriodMonths: mb.BillingPeriodMonths,
			Mode:                mb.Mode,
			CreatedAt:           now,
			UpdatedAt:           now,
		})
	}

	return c.Status(fiber.StatusCreated).JSON(fiber.Map{
		"budgets": createdBudgets,
	})
}
