package handlers

import (
	"encoding/json"
	"net/http"
	"net/mail"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/the-financial-workspace/backend/internal/database"
	"github.com/the-financial-workspace/backend/internal/middleware"
	"github.com/the-financial-workspace/backend/internal/models"
	"golang.org/x/crypto/bcrypt"
)

// dbUser is used for unmarshaling user rows from Supabase (includes password_hash).
type dbUser struct {
	ID           uuid.UUID `json:"id"`
	Email        string    `json:"email"`
	PasswordHash string    `json:"password_hash"`
	FirstName    string    `json:"first_name"`
	LastName     string    `json:"last_name"`
	CreatedAt    time.Time `json:"created_at"`
}

// Register creates a new user account.
func Register(c *fiber.Ctx) error {
	var req models.RegisterRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(models.ErrorResponse{
			Error: "invalid request body",
		})
	}

	// Validate required fields.
	req.Email = strings.TrimSpace(strings.ToLower(req.Email))
	req.FirstName = strings.TrimSpace(req.FirstName)

	if req.Email == "" || req.Password == "" || req.FirstName == "" {
		return c.Status(fiber.StatusBadRequest).JSON(models.ErrorResponse{
			Error: "email, password, and first_name are required",
		})
	}

	if _, err := mail.ParseAddress(req.Email); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(models.ErrorResponse{
			Error: "invalid email address",
		})
	}

	if len(req.Password) < 8 {
		return c.Status(fiber.StatusBadRequest).JSON(models.ErrorResponse{
			Error: "password must be at least 8 characters",
		})
	}

	// Check if email already exists.
	query := database.NewFilter().Select("id").Eq("email", req.Email).Build()
	body, statusCode, err := database.DB.Get("users", query)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{
			Error: "database error",
		})
	}
	if statusCode != http.StatusOK {
		return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{
			Error: "database error",
		})
	}

	var existing []struct{ ID string `json:"id"` }
	if err := json.Unmarshal(body, &existing); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{
			Error: "database error",
		})
	}
	if len(existing) > 0 {
		return c.Status(fiber.StatusConflict).JSON(models.ErrorResponse{
			Error: "email already registered",
		})
	}

	// Hash password.
	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{
			Error: "failed to hash password",
		})
	}

	// Build insert payload.
	now := time.Now().UTC()
	userID := uuid.New()
	insertPayload := map[string]interface{}{
		"id":            userID.String(),
		"email":         req.Email,
		"password_hash": string(hash),
		"first_name":    req.FirstName,
		"last_name":     strings.TrimSpace(req.LastName),
		"created_at":    now.Format(time.RFC3339Nano),
	}

	payloadBytes, err := json.Marshal(insertPayload)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{
			Error: "failed to marshal user data",
		})
	}

	respBody, statusCode, err := database.DB.Post("users", payloadBytes)
	if err != nil || statusCode != http.StatusCreated {
		_ = respBody
		return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{
			Error: "failed to create user",
		})
	}

	user := models.User{
		ID:        userID,
		Email:     req.Email,
		FirstName: req.FirstName,
		LastName:  strings.TrimSpace(req.LastName),
		CreatedAt: now,
	}

	// Generate JWT.
	token, err := middleware.GenerateToken(user.ID, user.Email)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{
			Error: "failed to generate token",
		})
	}

	return c.Status(fiber.StatusCreated).JSON(models.AuthResponse{
		Token: token,
		User:  user,
	})
}

// Login authenticates a user and returns a JWT.
func Login(c *fiber.Ctx) error {
	var req models.LoginRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(models.ErrorResponse{
			Error: "invalid request body",
		})
	}

	req.Email = strings.TrimSpace(strings.ToLower(req.Email))

	if req.Email == "" || req.Password == "" {
		return c.Status(fiber.StatusBadRequest).JSON(models.ErrorResponse{
			Error: "email and password are required",
		})
	}

	// Fetch user by email (includes password_hash).
	query := database.NewFilter().Select("id,email,password_hash,first_name,last_name,created_at").Eq("email", req.Email).Build()
	body, statusCode, err := database.DB.Get("users", query)
	if err != nil || statusCode != http.StatusOK {
		return c.Status(fiber.StatusUnauthorized).JSON(models.ErrorResponse{
			Error: "invalid email or password",
		})
	}

	var users []dbUser
	if err := json.Unmarshal(body, &users); err != nil || len(users) == 0 {
		return c.Status(fiber.StatusUnauthorized).JSON(models.ErrorResponse{
			Error: "invalid email or password",
		})
	}

	u := users[0]

	if err := bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(req.Password)); err != nil {
		return c.Status(fiber.StatusUnauthorized).JSON(models.ErrorResponse{
			Error: "invalid email or password",
		})
	}

	token, err := middleware.GenerateToken(u.ID, u.Email)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{
			Error: "failed to generate token",
		})
	}

	user := models.User{
		ID:        u.ID,
		Email:     u.Email,
		FirstName: u.FirstName,
		LastName:  u.LastName,
		CreatedAt: u.CreatedAt,
	}

	return c.JSON(models.AuthResponse{
		Token: token,
		User:  user,
	})
}
