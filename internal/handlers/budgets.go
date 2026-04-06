package handlers

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/the-financial-workspace/backend/internal/database"
	"github.com/the-financial-workspace/backend/internal/middleware"
	"github.com/the-financial-workspace/backend/internal/models"
)

// guidedCategory defines a category for the 50/30/20 guided mode.
type guidedCategory struct {
	Name          string
	Percent       float64
	Icon          string
	SortOrder     int
	Subcategories []guidedSubcategory
}

// guidedSubcategory defines a subcategory within a guided category.
type guidedSubcategory struct {
	Name      string
	Percent   float64
	Icon      string
	SortOrder int
}

// getGuidedCategories returns the 50/30/20 guided template.
func getGuidedCategories() []guidedCategory {
	return []guidedCategory{
		{
			Name: "Necesidades", Percent: 50, Icon: "🏠", SortOrder: 1,
			Subcategories: []guidedSubcategory{
				{Name: "Vivienda", Percent: 28, Icon: "🏡", SortOrder: 1},
				{Name: "Comida", Percent: 12, Icon: "🍽️", SortOrder: 2},
				{Name: "Transporte", Percent: 6, Icon: "🚗", SortOrder: 3},
				{Name: "Servicios", Percent: 4, Icon: "💡", SortOrder: 4},
			},
		},
		{
			Name: "Deseos", Percent: 30, Icon: "🎉", SortOrder: 2,
			Subcategories: []guidedSubcategory{
				{Name: "Salidas", Percent: 10, Icon: "🍻", SortOrder: 1},
				{Name: "Entretenimiento", Percent: 5, Icon: "🎬", SortOrder: 2},
				{Name: "Ropa", Percent: 7, Icon: "👕", SortOrder: 3},
				{Name: "Viajes", Percent: 8, Icon: "✈️", SortOrder: 4},
			},
		},
		{
			Name: "Ahorro", Percent: 20, Icon: "💰", SortOrder: 3,
			Subcategories: []guidedSubcategory{
				{Name: "Fondo de emergencia", Percent: 8, Icon: "🛡️", SortOrder: 1},
				{Name: "Inversión", Percent: 12, Icon: "📈", SortOrder: 2},
			},
		},
	}
}

// ListBudgets returns all budgets for the authenticated user.
func ListBudgets(c *fiber.Ctx) error {
	userID := middleware.GetUserID(c)

	query := database.NewFilter().
		Select("*").
		Eq("user_id", userID.String()).
		Order("created_at", "desc").
		Build()

	body, statusCode, err := database.DB.Get("budgets", query)
	if err != nil || statusCode != http.StatusOK {
		return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{
			Error: "failed to fetch budgets",
		})
	}

	var budgets []models.Budget
	if err := json.Unmarshal(body, &budgets); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{
			Error: "failed to parse budgets",
		})
	}

	if budgets == nil {
		budgets = make([]models.Budget, 0)
	}

	return c.JSON(budgets)
}

// CreateBudget creates a new budget and optionally seeds guided categories.
func CreateBudget(c *fiber.Ctx) error {
	userID := middleware.GetUserID(c)

	var req models.CreateBudgetRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(models.ErrorResponse{
			Error: "invalid request body",
		})
	}

	if req.Name == "" {
		return c.Status(fiber.StatusBadRequest).JSON(models.ErrorResponse{
			Error: "name is required",
		})
	}
	if req.MonthlyIncome <= 0 {
		return c.Status(fiber.StatusBadRequest).JSON(models.ErrorResponse{
			Error: "monthly_income must be positive",
		})
	}
	if req.Currency == "" {
		req.Currency = "COP"
	}
	if req.BillingPeriodMonths <= 0 {
		req.BillingPeriodMonths = 1
	}
	if req.Mode == "" {
		req.Mode = "manual"
	}

	now := time.Now().UTC()
	budgetID := uuid.New()

	budget := models.Budget{
		ID:                  budgetID,
		UserID:              userID,
		Name:                req.Name,
		MonthlyIncome:       req.MonthlyIncome,
		Currency:            req.Currency,
		BillingPeriodMonths: req.BillingPeriodMonths,
		Mode:                req.Mode,
		CreatedAt:           now,
		UpdatedAt:           now,
	}

	// Insert budget.
	budgetPayload := map[string]interface{}{
		"id":                    budget.ID.String(),
		"user_id":               budget.UserID.String(),
		"name":                  budget.Name,
		"monthly_income":        budget.MonthlyIncome,
		"currency":              budget.Currency,
		"billing_period_months": budget.BillingPeriodMonths,
		"mode":                  budget.Mode,
		"created_at":            now.Format(time.RFC3339Nano),
		"updated_at":            now.Format(time.RFC3339Nano),
	}

	payloadBytes, _ := json.Marshal(budgetPayload)
	_, statusCode, err := database.DB.Post("budgets", payloadBytes)
	if err != nil || statusCode != http.StatusCreated {
		return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{
			Error: "failed to create budget",
		})
	}

	// Seed guided categories if mode is "guided".
	if budget.Mode == "guided" {
		for _, gc := range getGuidedCategories() {
			catID := uuid.New()
			catPayload := map[string]interface{}{
				"id":                catID.String(),
				"budget_id":         budget.ID.String(),
				"name":              gc.Name,
				"allocation_percent": gc.Percent,
				"icon":              gc.Icon,
				"sort_order":        gc.SortOrder,
				"created_at":        now.Format(time.RFC3339Nano),
			}
			catBytes, _ := json.Marshal(catPayload)
			_, statusCode, err := database.DB.Post("budget_categories", catBytes)
			if err != nil || statusCode != http.StatusCreated {
				return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{
					Error: "failed to create guided category",
				})
			}

			for _, gs := range gc.Subcategories {
				subID := uuid.New()
				subPayload := map[string]interface{}{
					"id":                subID.String(),
					"category_id":       catID.String(),
					"name":              gs.Name,
					"allocation_percent": gs.Percent,
					"icon":              gs.Icon,
					"sort_order":        gs.SortOrder,
					"created_at":        now.Format(time.RFC3339Nano),
				}
				subBytes, _ := json.Marshal(subPayload)
				_, statusCode, err := database.DB.Post("budget_subcategories", subBytes)
				if err != nil || statusCode != http.StatusCreated {
					return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{
						Error: "failed to create guided subcategory",
					})
				}
			}
		}
	}

	return c.Status(fiber.StatusCreated).JSON(budget)
}

// GetBudget returns a single budget by ID.
func GetBudget(c *fiber.Ctx) error {
	userID := middleware.GetUserID(c)
	budgetID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(models.ErrorResponse{
			Error: "invalid budget ID",
		})
	}

	query := database.NewFilter().
		Select("*").
		Eq("id", budgetID.String()).
		Eq("user_id", userID.String()).
		Build()

	body, statusCode, err := database.DB.Get("budgets", query)
	if err != nil || statusCode != http.StatusOK {
		return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{
			Error: "failed to fetch budget",
		})
	}

	var budgets []models.Budget
	if err := json.Unmarshal(body, &budgets); err != nil || len(budgets) == 0 {
		return c.Status(fiber.StatusNotFound).JSON(models.ErrorResponse{
			Error: "budget not found",
		})
	}

	return c.JSON(budgets[0])
}

// UpdateBudget updates an existing budget.
func UpdateBudget(c *fiber.Ctx) error {
	userID := middleware.GetUserID(c)
	budgetID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(models.ErrorResponse{
			Error: "invalid budget ID",
		})
	}

	var req models.UpdateBudgetRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(models.ErrorResponse{
			Error: "invalid request body",
		})
	}

	// Fetch existing budget to verify ownership.
	getQuery := database.NewFilter().
		Select("*").
		Eq("id", budgetID.String()).
		Eq("user_id", userID.String()).
		Build()

	body, statusCode, err := database.DB.Get("budgets", getQuery)
	if err != nil || statusCode != http.StatusOK {
		return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{
			Error: "failed to fetch budget",
		})
	}

	var budgets []models.Budget
	if err := json.Unmarshal(body, &budgets); err != nil || len(budgets) == 0 {
		return c.Status(fiber.StatusNotFound).JSON(models.ErrorResponse{
			Error: "budget not found",
		})
	}

	b := budgets[0]

	// Apply partial updates.
	if req.Name != nil {
		b.Name = *req.Name
	}
	if req.MonthlyIncome != nil {
		b.MonthlyIncome = *req.MonthlyIncome
	}
	if req.Currency != nil {
		b.Currency = *req.Currency
	}
	if req.BillingPeriodMonths != nil {
		b.BillingPeriodMonths = *req.BillingPeriodMonths
	}
	if req.Mode != nil {
		b.Mode = *req.Mode
	}
	b.UpdatedAt = time.Now().UTC()

	// Build update payload.
	updatePayload := map[string]interface{}{
		"name":                  b.Name,
		"monthly_income":        b.MonthlyIncome,
		"currency":              b.Currency,
		"billing_period_months": b.BillingPeriodMonths,
		"mode":                  b.Mode,
		"updated_at":            b.UpdatedAt.Format(time.RFC3339Nano),
	}
	updateBytes, _ := json.Marshal(updatePayload)

	patchQuery := database.NewFilter().
		Eq("id", budgetID.String()).
		Eq("user_id", userID.String()).
		Build()

	_, statusCode, err = database.DB.Patch("budgets", patchQuery, updateBytes)
	if err != nil || statusCode != http.StatusOK {
		return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{
			Error: "failed to update budget",
		})
	}

	return c.JSON(b)
}

// DeleteBudget deletes a budget and all associated data.
func DeleteBudget(c *fiber.Ctx) error {
	userID := middleware.GetUserID(c)
	budgetID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(models.ErrorResponse{
			Error: "invalid budget ID",
		})
	}

	// Verify ownership first.
	getQuery := database.NewFilter().
		Select("id").
		Eq("id", budgetID.String()).
		Eq("user_id", userID.String()).
		Build()

	body, statusCode, err := database.DB.Get("budgets", getQuery)
	if err != nil || statusCode != http.StatusOK {
		return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{
			Error: "failed to verify budget ownership",
		})
	}

	var found []struct{ ID string `json:"id"` }
	if err := json.Unmarshal(body, &found); err != nil || len(found) == 0 {
		return c.Status(fiber.StatusNotFound).JSON(models.ErrorResponse{
			Error: "budget not found",
		})
	}

	// 1. Delete expenses for this budget.
	expQuery := database.NewFilter().Eq("budget_id", budgetID.String()).Build()
	_, _, _ = database.DB.Delete("budget_expenses", expQuery)

	// 2. Get category IDs to delete subcategories.
	catQuery := database.NewFilter().Select("id").Eq("budget_id", budgetID.String()).Build()
	catBody, _, _ := database.DB.Get("budget_categories", catQuery)

	var cats []struct{ ID string `json:"id"` }
	if err := json.Unmarshal(catBody, &cats); err == nil && len(cats) > 0 {
		catIDs := make([]string, len(cats))
		for i, cat := range cats {
			catIDs[i] = cat.ID
		}
		// Delete subcategories for all categories.
		subQuery := database.NewFilter().In("category_id", catIDs).Build()
		_, _, _ = database.DB.Delete("budget_subcategories", subQuery)
	}

	// 3. Delete categories.
	delCatQuery := database.NewFilter().Eq("budget_id", budgetID.String()).Build()
	_, _, _ = database.DB.Delete("budget_categories", delCatQuery)

	// 4. Delete the budget itself.
	delBudgetQuery := database.NewFilter().
		Eq("id", budgetID.String()).
		Eq("user_id", userID.String()).
		Build()
	_, _, _ = database.DB.Delete("budgets", delBudgetQuery)

	return c.SendStatus(fiber.StatusNoContent)
}
