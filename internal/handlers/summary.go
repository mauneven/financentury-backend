package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/the-financial-workspace/backend/internal/database"
	"github.com/the-financial-workspace/backend/internal/middleware"
	"github.com/the-financial-workspace/backend/internal/models"
)

// GetBudgetSummary returns a comprehensive budget summary via the RPC function.
func GetBudgetSummary(c *fiber.Ctx) error {
	userID := middleware.GetUserID(c)
	budgetID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(models.ErrorResponse{Error: "invalid budget ID"})
	}

	// Call the RPC function get_budget_summary.
	rpcPayload := map[string]string{
		"p_budget_id": budgetID.String(),
		"p_user_id":   userID.String(),
	}
	rpcBytes, _ := json.Marshal(rpcPayload)

	body, statusCode, err := database.DB.RPC("get_budget_summary", rpcBytes)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{Error: "failed to fetch budget summary"})
	}

	if statusCode != http.StatusOK {
		// RPC may return 404 or error if budget not found or not owned.
		return c.Status(fiber.StatusNotFound).JSON(models.ErrorResponse{Error: "budget not found"})
	}

	// The RPC function returns JSON directly. Pass it through as-is.
	c.Set("Content-Type", "application/json")
	return c.Send(body)
}

// GetBudgetTrends returns monthly spending trends via the RPC function.
func GetBudgetTrends(c *fiber.Ctx) error {
	userID := middleware.GetUserID(c)
	budgetID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(models.ErrorResponse{Error: "invalid budget ID"})
	}

	// Call the RPC function get_budget_trends.
	rpcPayload := map[string]string{
		"p_budget_id": budgetID.String(),
		"p_user_id":   userID.String(),
	}
	rpcBytes, _ := json.Marshal(rpcPayload)

	body, statusCode, err := database.DB.RPC("get_budget_trends", rpcBytes)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{Error: "failed to fetch budget trends"})
	}

	if statusCode != http.StatusOK {
		return c.Status(fiber.StatusNotFound).JSON(models.ErrorResponse{Error: "budget not found"})
	}

	// The RPC function returns JSON directly. Pass it through as-is.
	c.Set("Content-Type", "application/json")
	return c.Send(body)
}
