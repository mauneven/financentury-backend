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
	"github.com/the-financial-workspace/backend/internal/ws"
)

// --- Validation Constants ---

const (
	// maxNameLength is the upper bound for name fields (budgets, sections, categories).
	maxNameLength = 200
	// maxDescriptionLength is the upper bound for description fields.
	maxDescriptionLength = 1000
	// maxIconLength is the upper bound for icon/emoji fields.
	maxIconLength = 50
	// maxCurrencyLength is the expected length for ISO currency codes.
	maxCurrencyLength = 3
	// maxAmountValue is the ceiling for monetary values to prevent overflow.
	maxAmountValue = 1e15
	// dateFormat is the expected layout for expense dates.
	dateFormat = "2006-01-02"
)

// validBudgetModes lists the accepted budget mode strings.
var validBudgetModes = map[string]bool{
	"manual":     true,
	"balanced":   true,
	"debt-free":  true,
	"debt-payoff": true,
	"travel":     true,
	"event":      true,
}

// guidedModes lists modes that should seed template sections on creation.
var guidedModes = map[string]bool{
	"balanced":   true,
	"debt-free":  true,
	"debt-payoff": true,
	"travel":     true,
	"event":      true,
}

// --- Common Error Helpers ---

// errUnauthorized returns a 401 JSON response.
func errUnauthorized(c *fiber.Ctx) error {
	return c.Status(fiber.StatusUnauthorized).JSON(models.ErrorResponse{Error: "unauthorized"})
}

// errBadRequest returns a 400 JSON response with the given message.
func errBadRequest(c *fiber.Ctx, msg string) error {
	return c.Status(fiber.StatusBadRequest).JSON(models.ErrorResponse{Error: msg})
}

// errNotFound returns a 404 JSON response with the given message.
func errNotFound(c *fiber.Ctx, msg string) error {
	return c.Status(fiber.StatusNotFound).JSON(models.ErrorResponse{Error: msg})
}

// errInternal returns a 500 JSON response with the given message.
func errInternal(c *fiber.Ctx, msg string) error {
	return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{Error: msg})
}

// errForbidden returns a 403 JSON response with the given message.
func errForbidden(c *fiber.Ctx, msg string) error {
	return c.Status(fiber.StatusForbidden).JSON(models.ErrorResponse{Error: msg})
}

// --- Auth & ID Parsing Helpers ---

// requireUserID extracts the authenticated user ID from the context. Returns
// uuid.Nil and false if the user is not authenticated.
func requireUserID(c *fiber.Ctx) (uuid.UUID, bool) {
	uid := middleware.GetUserID(c)
	return uid, uid != uuid.Nil
}

// parseUUIDParam parses a named route parameter as a UUID. Returns uuid.Nil
// and false on failure.
func parseUUIDParam(c *fiber.Ctx, name string) (uuid.UUID, bool) {
	id, err := uuid.Parse(c.Params(name))
	if err != nil {
		return uuid.Nil, false
	}
	return id, true
}

// --- Date Validation ---

// isValidDate checks whether s matches YYYY-MM-DD format.
func isValidDate(s string) bool {
	_, err := time.Parse(dateFormat, s)
	return err == nil
}

// --- Broadcast Helper ---

// broadcast sends a WebSocket message to all clients watching a budget.
// It is a no-op if the hub has not been initialized (e.g. in tests).
func broadcast(budgetID string, msgType string, data interface{}) {
	hub := GetHub()
	if hub == nil {
		return
	}
	hub.BroadcastToBudget(budgetID, ws.Message{
		Type: msgType,
		Data: data,
	})
}

// --- DB Access Helpers ---

// verifyBudgetOwnership checks that the authenticated user owns the budget
// by querying the budgets table in Supabase. Returns a non-nil error if the
// user is not the owner.
func verifyBudgetOwnership(budgetID, userID uuid.UUID) error {
	query := database.NewFilter().
		Select("id").
		Eq("id", budgetID.String()).
		Eq("user_id", userID.String()).
		Build()

	body, statusCode, err := database.DB.Get("budgets", query)
	if err != nil || statusCode != http.StatusOK {
		return fiber.ErrNotFound
	}

	var found []struct{ ID string `json:"id"` }
	if err := json.Unmarshal(body, &found); err != nil || len(found) == 0 {
		return fiber.ErrNotFound
	}
	return nil
}

// verifyBudgetAccess checks that the user is the budget owner or a
// collaborator. Returns a non-nil error when neither condition holds.
func verifyBudgetAccess(budgetID, userID uuid.UUID) error {
	if err := verifyBudgetOwnership(budgetID, userID); err == nil {
		return nil
	}

	query := database.NewFilter().
		Select("id").
		Eq("budget_id", budgetID.String()).
		Eq("user_id", userID.String()).
		Build()

	body, statusCode, err := database.DB.Get("budget_collaborators", query)
	if err != nil || statusCode != http.StatusOK {
		return fiber.ErrNotFound
	}

	var found []struct{ ID string `json:"id"` }
	if err := json.Unmarshal(body, &found); err != nil || len(found) == 0 {
		return fiber.ErrNotFound
	}
	return nil
}

// verifySectionOwnership checks that the section belongs to the budget and
// the user has access. Returns a non-nil error on failure.
func verifySectionOwnership(budgetID, sectionID, userID uuid.UUID) error {
	if err := verifyBudgetAccess(budgetID, userID); err != nil {
		return err
	}

	query := database.NewFilter().
		Select("id").
		Eq("id", sectionID.String()).
		Eq("budget_id", budgetID.String()).
		Build()

	body, statusCode, err := database.DB.Get("budget_categories", query)
	if err != nil || statusCode != http.StatusOK {
		return fiber.ErrNotFound
	}

	var found []struct{ ID string `json:"id"` }
	if err := json.Unmarshal(body, &found); err != nil || len(found) == 0 {
		return fiber.ErrNotFound
	}
	return nil
}

// verifyCategoryBelongsToBudget verifies that a category (by its ID)
// ultimately belongs to the given budget by checking its parent section.
func verifyCategoryBelongsToBudget(categoryID, budgetID uuid.UUID) error {
	catQuery := database.NewFilter().
		Select("id,category_id").
		Eq("id", categoryID.String()).
		Build()

	catBody, statusCode, err := database.DB.Get("budget_subcategories", catQuery)
	if err != nil || statusCode != http.StatusOK {
		return fiber.ErrNotFound
	}

	var catResults []struct {
		ID         string `json:"id"`
		CategoryID string `json:"category_id"`
	}
	if err := json.Unmarshal(catBody, &catResults); err != nil || len(catResults) == 0 {
		return fiber.ErrNotFound
	}

	sectionCheckQuery := database.NewFilter().
		Select("id").
		Eq("id", catResults[0].CategoryID).
		Eq("budget_id", budgetID.String()).
		Build()

	sectionBody, statusCode, err := database.DB.Get("budget_categories", sectionCheckQuery)
	if err != nil || statusCode != http.StatusOK {
		return fiber.ErrNotFound
	}

	var sectionFound []struct{ ID string `json:"id"` }
	if err := json.Unmarshal(sectionBody, &sectionFound); err != nil || len(sectionFound) == 0 {
		return fiber.ErrNotFound
	}
	return nil
}

// marshalJSON marshals v to JSON bytes, returning an error response on failure.
func marshalJSON(v interface{}) ([]byte, error) {
	return json.Marshal(v)
}
