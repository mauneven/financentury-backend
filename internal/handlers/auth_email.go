package handlers

import (
	"encoding/json"
	"log"
	"net/http"
	"net/mail"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/the-financial-workspace/backend/internal/database"
	"github.com/the-financial-workspace/backend/internal/middleware"
	"golang.org/x/crypto/bcrypt"
)

// --- Per-email login rate limiter ---
// Prevents distributed brute-force attacks against a single account by
// tracking failed login attempts per email address in memory.

const (
	maxLoginAttemptsPerEmail = 5               // max attempts before lockout
	loginAttemptWindow       = 15 * time.Minute // sliding window duration
)

// loginAttemptRecord tracks failed login attempts for a single email.
type loginAttemptRecord struct {
	attempts  int
	expiresAt time.Time
}

var (
	loginAttempts   = make(map[string]*loginAttemptRecord)
	loginAttemptsMu sync.Mutex
)

// checkEmailRateLimit returns true if the email has exceeded the allowed
// number of login attempts within the sliding window.
func checkEmailRateLimit(email string) bool {
	loginAttemptsMu.Lock()
	defer loginAttemptsMu.Unlock()

	rec, exists := loginAttempts[email]
	if !exists {
		return false
	}
	// Window expired — reset.
	if time.Now().After(rec.expiresAt) {
		delete(loginAttempts, email)
		return false
	}
	return rec.attempts >= maxLoginAttemptsPerEmail
}

// recordFailedLogin increments the failed-attempt counter for an email.
func recordFailedLogin(email string) {
	loginAttemptsMu.Lock()
	defer loginAttemptsMu.Unlock()

	rec, exists := loginAttempts[email]
	if !exists || time.Now().After(rec.expiresAt) {
		loginAttempts[email] = &loginAttemptRecord{
			attempts:  1,
			expiresAt: time.Now().Add(loginAttemptWindow),
		}
		return
	}
	rec.attempts++
}

// clearLoginAttempts resets the counter on successful login.
func clearLoginAttempts(email string) {
	loginAttemptsMu.Lock()
	defer loginAttemptsMu.Unlock()
	delete(loginAttempts, email)
}

func init() {
	// Start a background goroutine to periodically purge expired entries
	// from the loginAttempts map, preventing unbounded memory growth.
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			loginAttemptsMu.Lock()
			now := time.Now()
			for email, rec := range loginAttempts {
				if now.After(rec.expiresAt) {
					delete(loginAttempts, email)
				}
			}
			loginAttemptsMu.Unlock()
		}
	}()
}

// bcryptCost is the bcrypt work factor. Set to 12 (above the default of 10)
// for a financial application to increase brute-force resistance.
const bcryptCost = 12

// maxPasswordBytes is the bcrypt truncation limit. Passwords longer than 72
// bytes are silently truncated by bcrypt, which could lead to collisions.
const maxPasswordBytes = 72

// RegisterRequest is the expected request body for email registration.
type RegisterRequest struct {
	Name     string `json:"name"`
	Email    string `json:"email"`
	Password string `json:"password"`
}

// LoginRequest is the expected request body for email login.
type LoginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// profileWithPassword is a local struct used to unmarshal profile rows that
// include the password_hash column, which is excluded from the public Profile
// model via json:"-".
type profileWithPassword struct {
	ID           uuid.UUID `json:"id"`
	Email        string    `json:"email"`
	FullName     string    `json:"full_name"`
	AvatarURL    string    `json:"avatar_url"`
	PasswordHash string    `json:"password_hash"`
}

// Register handles POST /api/auth/register. It creates a new email/password
// account, hashes the password with bcrypt, stores the profile, and returns
// a backend-issued JWT.
func Register(c *fiber.Ctx) error {
	var req RegisterRequest
	if err := c.BodyParser(&req); err != nil {
		return errBadRequest(c, "invalid request body")
	}

	// Validate fields.
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		return errBadRequest(c, "name is required")
	}
	if len(req.Name) > maxNameLength {
		return errBadRequest(c, "name is too long")
	}

	req.Email = strings.TrimSpace(strings.ToLower(req.Email))
	if _, err := mail.ParseAddress(req.Email); err != nil {
		return errBadRequest(c, "invalid email address")
	}

	if len(req.Password) < 8 {
		return errBadRequest(c, "password must be at least 8 characters")
	}
	// Security: reject passwords exceeding bcrypt's 72-byte input limit to
	// prevent silent truncation and potential hash collisions.
	if len([]byte(req.Password)) > maxPasswordBytes {
		return errBadRequest(c, "password must not exceed 72 bytes")
	}
	// Password complexity: require at least one uppercase letter, one
	// lowercase letter, and one digit for a financial application.
	var hasUpper, hasLower, hasDigit bool
	for _, r := range req.Password {
		switch {
		case unicode.IsUpper(r):
			hasUpper = true
		case unicode.IsLower(r):
			hasLower = true
		case unicode.IsDigit(r):
			hasDigit = true
		}
	}
	if !hasUpper || !hasLower || !hasDigit {
		return errBadRequest(c, "password must contain at least one uppercase letter, one lowercase letter, and one digit")
	}

	// Check if email already exists.
	checkQuery := database.NewFilter().
		Select("id").
		Eq("email", req.Email).
		Build()

	body, statusCode, err := database.DB.Get("profiles", checkQuery)
	if err != nil {
		log.Printf("[auth-email] GET profiles failed: %v", err)
		return errInternal(c, "failed to check existing account")
	}
	if statusCode != http.StatusOK {
		log.Printf("[auth-email] GET profiles returned status %d: %s", statusCode, string(body))
		return errInternal(c, "failed to check existing account")
	}

	var existing []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &existing); err != nil {
		log.Printf("[auth-email] failed to parse existing profiles: %v", err)
		return errInternal(c, "failed to check existing account")
	}
	if len(existing) > 0 {
		return c.Status(fiber.StatusConflict).JSON(fiber.Map{
			"error": "email already registered",
		})
	}

	// Hash the password.
	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcryptCost)
	if err != nil {
		log.Printf("[auth-email] bcrypt hash failed: %v", err)
		return errInternal(c, "failed to process password")
	}

	// Create profile.
	now := time.Now().UTC()
	profileID := uuid.New()

	payload := map[string]interface{}{
		"id":            profileID.String(),
		"email":         req.Email,
		"full_name":     req.Name,
		"password_hash": string(hash),
		"auth_provider": "email",
		"avatar_url":    "",
		"created_at":    now.Format(time.RFC3339Nano),
		"updated_at":    now.Format(time.RFC3339Nano),
	}

	payloadBytes, err := marshalJSON(payload)
	if err != nil {
		return errInternal(c, "failed to marshal profile")
	}

	respBody, respStatus, err := database.DB.Post("profiles", payloadBytes)
	if err != nil {
		log.Printf("[auth-email] POST profiles failed: %v", err)
		return errInternal(c, "failed to create account")
	}
	if respStatus != http.StatusCreated {
		log.Printf("[auth-email] POST profiles returned status %d: %s", respStatus, string(respBody))
		return errInternal(c, "failed to create account")
	}

	// Generate JWT.
	token, err := middleware.GenerateToken(profileID, req.Email)
	if err != nil {
		return errInternal(c, "failed to generate token")
	}

	CreateSession(profileID, token, c)

	return c.Status(fiber.StatusCreated).JSON(fiber.Map{
		"token": token,
		"user": fiber.Map{
			"id":         profileID,
			"email":      req.Email,
			"full_name":  req.Name,
			"avatar_url": "",
		},
	})
}

// Login handles POST /api/auth/login. It authenticates an existing
// email/password user and returns a backend-issued JWT.
func Login(c *fiber.Ctx) error {
	var req LoginRequest
	if err := c.BodyParser(&req); err != nil {
		return errBadRequest(c, "invalid request body")
	}

	req.Email = strings.TrimSpace(strings.ToLower(req.Email))
	if req.Email == "" {
		return errBadRequest(c, "email is required")
	}
	if req.Password == "" {
		return errBadRequest(c, "password is required")
	}

	// Security: per-email rate limit to prevent distributed brute-force attacks.
	if checkEmailRateLimit(req.Email) {
		return c.Status(fiber.StatusTooManyRequests).JSON(fiber.Map{
			"error": "too many login attempts, please try again later",
		})
	}

	// Fetch profile with password hash.
	query := database.NewFilter().
		Select("id,email,full_name,avatar_url,password_hash").
		Eq("email", req.Email).
		Build()

	body, statusCode, err := database.DB.Get("profiles", query)
	if err != nil {
		log.Printf("[auth-email] GET profiles failed: %v", err)
		return errInternal(c, "failed to authenticate")
	}
	if statusCode != http.StatusOK {
		log.Printf("[auth-email] GET profiles returned status %d: %s", statusCode, string(body))
		return errInternal(c, "failed to authenticate")
	}

	var profiles []profileWithPassword
	if err := json.Unmarshal(body, &profiles); err != nil {
		log.Printf("[auth-email] failed to parse profiles: %v", err)
		return errInternal(c, "failed to authenticate")
	}

	if len(profiles) == 0 {
		recordFailedLogin(req.Email)
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
			"error": "invalid email or password",
		})
	}

	profile := profiles[0]

	// If password_hash is empty, the user signed up via Google only.
	if profile.PasswordHash == "" {
		recordFailedLogin(req.Email)
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
			"error": "invalid email or password",
		})
	}

	// Verify password.
	if err := bcrypt.CompareHashAndPassword([]byte(profile.PasswordHash), []byte(req.Password)); err != nil {
		recordFailedLogin(req.Email)
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
			"error": "invalid email or password",
		})
	}

	// Successful login — clear failed attempt counter.
	clearLoginAttempts(req.Email)

	// Generate JWT.
	token, err := middleware.GenerateToken(profile.ID, profile.Email)
	if err != nil {
		return errInternal(c, "failed to generate token")
	}

	CreateSession(profile.ID, token, c)

	return c.JSON(fiber.Map{
		"token": token,
		"user": fiber.Map{
			"id":         profile.ID,
			"email":      profile.Email,
			"full_name":  profile.FullName,
			"avatar_url": profile.AvatarURL,
		},
	})
}
