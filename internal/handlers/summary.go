package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/gofiber/fiber/v2"
	"github.com/the-financial-workspace/backend/internal/database"
	"github.com/the-financial-workspace/backend/internal/models"
)

// GetBudgetSummary returns a comprehensive budget summary via the Supabase
// RPC function get_budget_summary. The result is passed through as raw JSON.
func GetBudgetSummary(c *fiber.Ctx) error {
	userID, ok := requireUserID(c)
	if !ok {
		return errUnauthorized(c)
	}

	budgetID, ok := parseUUIDParam(c, "id")
	if !ok {
		return errBadRequest(c, "invalid budget ID")
	}

	rpcPayload := map[string]string{
		"p_budget_id": budgetID.String(),
		"p_user_id":   userID.String(),
	}
	rpcBytes, err := json.Marshal(rpcPayload)
	if err != nil {
		return errInternal(c, "failed to serialize request")
	}

	body, statusCode, err := database.DB.RPC("get_budget_summary", rpcBytes)
	if err != nil {
		return errInternal(c, "failed to fetch budget summary")
	}

	if statusCode != http.StatusOK {
		return errNotFound(c, "budget not found")
	}

	c.Set("Content-Type", "application/json")
	return c.Send(body)
}

// GetBudgetTrends returns monthly spending trends via the Supabase RPC
// function get_budget_trends. The result is passed through as raw JSON.
func GetBudgetTrends(c *fiber.Ctx) error {
	userID, ok := requireUserID(c)
	if !ok {
		return errUnauthorized(c)
	}

	budgetID, ok := parseUUIDParam(c, "id")
	if !ok {
		return errBadRequest(c, "invalid budget ID")
	}

	rpcPayload := map[string]string{
		"p_budget_id": budgetID.String(),
		"p_user_id":   userID.String(),
	}
	rpcBytes, err := json.Marshal(rpcPayload)
	if err != nil {
		return errInternal(c, "failed to serialize request")
	}

	body, statusCode, err := database.DB.RPC("get_budget_trends", rpcBytes)
	if err != nil {
		return errInternal(c, "failed to fetch budget trends")
	}

	if statusCode != http.StatusOK {
		return errNotFound(c, "budget not found")
	}

	c.Set("Content-Type", "application/json")
	return c.Send(body)
}

// Ensure models.ErrorResponse is used (prevents unused-import lint in some
// configurations while keeping the import list clean).
var _ = models.ErrorResponse{}
