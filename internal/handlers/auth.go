package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/gofiber/fiber/v2"
	"github.com/the-financial-workspace/backend/internal/database"
	"github.com/the-financial-workspace/backend/internal/middleware"
	"github.com/the-financial-workspace/backend/internal/models"
)

// Me returns the authenticated user's profile from the profiles table.
// This endpoint must be behind the Protected middleware.
func Me(c *fiber.Ctx) error {
	userID := middleware.GetUserID(c)

	query := database.NewFilter().
		Select("id,email,full_name,avatar_url,created_at,updated_at").
		Eq("id", userID.String()).
		Build()

	body, statusCode, err := database.DB.Get("profiles", query)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{
			Error: "failed to fetch profile",
		})
	}
	if statusCode != http.StatusOK {
		return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{
			Error: "failed to fetch profile",
		})
	}

	var profiles []models.Profile
	if err := json.Unmarshal(body, &profiles); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{
			Error: "failed to parse profile",
		})
	}

	if len(profiles) == 0 {
		return c.Status(fiber.StatusNotFound).JSON(models.ErrorResponse{
			Error: "profile not found",
		})
	}

	return c.JSON(profiles[0])
}
