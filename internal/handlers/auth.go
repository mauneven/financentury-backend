package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/the-financial-workspace/backend/internal/database"
	"github.com/the-financial-workspace/backend/internal/middleware"
	"github.com/the-financial-workspace/backend/internal/models"
)

// Google OAuth credentials set at startup via InitAuth.
var googleClientID, googleClientSecret string

// allowedRedirectHosts stores the set of hosts that are permitted as OAuth
// redirect targets.
var allowedRedirectHosts []string

// oauthHTTPTimeout is the HTTP timeout for calls to Google's OAuth APIs.
const oauthHTTPTimeout = 10 * time.Second

// maxOAuthResponseSize limits OAuth response bodies to 1 MB.
const maxOAuthResponseSize = 1 << 20

// InitAuth configures the auth handler with Google OAuth credentials and
// allowed redirect origins.
func InitAuth(clientID, clientSecret string, allowedOrigins ...string) {
	googleClientID = clientID
	googleClientSecret = clientSecret
	allowedRedirectHosts = allowedOrigins
}

// isAllowedRedirectURI validates the redirect_uri against the configured
// allowlist. Only origins that were explicitly registered are accepted,
// preventing open redirect attacks.
func isAllowedRedirectURI(redirectURI string) bool {
	parsed, err := url.Parse(redirectURI)
	if err != nil {
		return false
	}
	if parsed.Scheme != "https" && !(parsed.Scheme == "http" && (parsed.Hostname() == "localhost" || parsed.Hostname() == "127.0.0.1")) {
		return false
	}
	if parsed.Path != "/auth/callback" {
		return false
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return false
	}
	origin := parsed.Scheme + "://" + parsed.Host
	for _, allowed := range allowedRedirectHosts {
		if origin == allowed {
			return true
		}
	}
	return false
}

// googleLoginRequest is the expected request body for Google login.
type googleLoginRequest struct {
	Code        string `json:"code"`
	RedirectURI string `json:"redirect_uri"`
}

// googleTokenResponse is the response from Google's token endpoint.
type googleTokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int    `json:"expires_in"`
	IDToken     string `json:"id_token"`
}

// googleUserInfo is the response from Google's userinfo endpoint.
type googleUserInfo struct {
	ID            string `json:"id"`
	Email         string `json:"email"`
	VerifiedEmail bool   `json:"verified_email"`
	Name          string `json:"name"`
	Picture       string `json:"picture"`
}

// GoogleLogin handles POST /api/auth/google. It exchanges a Google
// authorization code for user info, upserts the profile, and returns a
// backend-issued JWT.
func GoogleLogin(c *fiber.Ctx) error {
	var req googleLoginRequest
	if err := c.BodyParser(&req); err != nil {
		return errBadRequest(c, "invalid request body")
	}
	if req.Code == "" {
		return errBadRequest(c, "code is required")
	}
	if req.RedirectURI == "" {
		return errBadRequest(c, "redirect_uri is required")
	}
	if !isAllowedRedirectURI(req.RedirectURI) {
		return errBadRequest(c, "redirect_uri is not allowed")
	}

	// Exchange authorization code for tokens.
	tokenData := url.Values{
		"client_id":     {googleClientID},
		"client_secret": {googleClientSecret},
		"code":          {req.Code},
		"grant_type":    {"authorization_code"},
		"redirect_uri":  {req.RedirectURI},
	}

	tokenHTTPClient := &http.Client{Timeout: oauthHTTPTimeout}
	tokenResp, err := tokenHTTPClient.Post(
		"https://oauth2.googleapis.com/token",
		"application/x-www-form-urlencoded",
		strings.NewReader(tokenData.Encode()),
	)
	if err != nil {
		return errInternal(c, "failed to exchange authorization code")
	}
	defer tokenResp.Body.Close()

	tokenBody, err := io.ReadAll(io.LimitReader(tokenResp.Body, maxOAuthResponseSize))
	if err != nil {
		return errInternal(c, "failed to read token response")
	}
	if tokenResp.StatusCode != http.StatusOK {
		return c.Status(fiber.StatusUnauthorized).JSON(models.ErrorResponse{
			Error: "failed to exchange authorization code with Google",
		})
	}

	var tokenResult googleTokenResponse
	if err := json.Unmarshal(tokenBody, &tokenResult); err != nil {
		return errInternal(c, "failed to parse token response")
	}
	if tokenResult.AccessToken == "" {
		return c.Status(fiber.StatusUnauthorized).JSON(models.ErrorResponse{
			Error: "no access token received from Google",
		})
	}

	// Fetch user info from Google.
	userInfoReq, err := http.NewRequest("GET", "https://www.googleapis.com/oauth2/v2/userinfo", nil)
	if err != nil {
		return errInternal(c, "failed to create userinfo request")
	}
	userInfoReq.Header.Set("Authorization", "Bearer "+tokenResult.AccessToken)

	httpClient := &http.Client{Timeout: oauthHTTPTimeout}
	userInfoResp, err := httpClient.Do(userInfoReq)
	if err != nil {
		return errInternal(c, "failed to fetch user info from Google")
	}
	defer userInfoResp.Body.Close()

	userInfoBody, err := io.ReadAll(io.LimitReader(userInfoResp.Body, maxOAuthResponseSize))
	if err != nil {
		return errInternal(c, "failed to read user info response")
	}
	if userInfoResp.StatusCode != http.StatusOK {
		return c.Status(fiber.StatusUnauthorized).JSON(models.ErrorResponse{
			Error: "failed to fetch user info from Google",
		})
	}

	var userInfo googleUserInfo
	if err := json.Unmarshal(userInfoBody, &userInfo); err != nil {
		return errInternal(c, "failed to parse user info")
	}
	if userInfo.Email == "" {
		return c.Status(fiber.StatusUnauthorized).JSON(models.ErrorResponse{
			Error: "no email received from Google",
		})
	}
	if !userInfo.VerifiedEmail {
		return c.Status(fiber.StatusUnauthorized).JSON(models.ErrorResponse{
			Error: "email address is not verified by Google",
		})
	}

	// Look up or create profile by email.
	profile, err := upsertProfile(userInfo)
	if err != nil {
		log.Printf("[auth] upsertProfile failed for %s: %v", userInfo.Email, err)
		return errInternal(c, "failed to create or find user profile")
	}
	if profile.ID == uuid.Nil {
		log.Printf("[auth] upsertProfile returned nil ID for %s", userInfo.Email)
		return errInternal(c, "failed to create or find user profile")
	}

	// Generate backend JWT.
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
		},
	})
}

// upsertProfile looks up a profile by email and updates it if found, or
// creates a new one.
func upsertProfile(userInfo googleUserInfo) (models.Profile, error) {
	query := database.NewFilter().
		Select("id,email,full_name,created_at,updated_at").
		Eq("email", userInfo.Email).
		Build()

	body, statusCode, err := database.DB.Get("profiles", query)
	if err != nil {
		log.Printf("[auth] GET profiles failed: %v", err)
		return models.Profile{}, fmt.Errorf("database request failed: %w", err)
	}

	if statusCode != http.StatusOK {
		log.Printf("[auth] GET profiles returned status %d: %s", statusCode, string(body))
		return models.Profile{}, fmt.Errorf("database returned status %d", statusCode)
	}

	var profiles []models.Profile
	if err := json.Unmarshal(body, &profiles); err != nil {
		log.Printf("[auth] failed to parse profiles response: %v, body: %s", err, string(body))
		return models.Profile{}, fmt.Errorf("failed to parse profiles: %w", err)
	}

	if len(profiles) > 0 {
		profile := profiles[0]

		return profile, nil
	}

	// No existing profile found — create a new one.
	return createNewProfile(userInfo)
}

// createNewProfile creates a new profile in the database from Google user info.
func createNewProfile(userInfo googleUserInfo) (models.Profile, error) {
	now := time.Now().UTC()
	profileID := uuid.New()

	payload := map[string]interface{}{
		"id":            profileID.String(),
		"email":         userInfo.Email,
		"full_name":     userInfo.Name,
		"auth_provider": "google",
		"created_at":    now.Format(time.RFC3339Nano),
		"updated_at":    now.Format(time.RFC3339Nano),
	}

	payloadBytes, err := marshalJSON(payload)
	if err != nil {
		return models.Profile{}, fmt.Errorf("failed to marshal profile: %w", err)
	}

	respBody, statusCode, err := database.DB.Post("profiles", payloadBytes)
	if err != nil {
		log.Printf("[auth] POST profiles failed: %v", err)
		return models.Profile{}, fmt.Errorf("failed to create profile: %w", err)
	}

	if statusCode != http.StatusCreated {
		log.Printf("[auth] POST profiles returned status %d: %s", statusCode, string(respBody))

		// Race condition: another request may have created the profile.
		// Try to fetch by email.
		query := database.NewFilter().
			Select("id,email,full_name,created_at,updated_at").
			Eq("email", userInfo.Email).
			Build()

		body, getStatus, getErr := database.DB.Get("profiles", query)
		if getErr != nil {
			log.Printf("[auth] fallback GET profiles failed: %v", getErr)
			return models.Profile{}, fmt.Errorf("profile creation failed (status %d) and fallback lookup failed: %w", statusCode, getErr)
		}
		if getStatus != http.StatusOK {
			log.Printf("[auth] fallback GET profiles returned status %d: %s", getStatus, string(body))
			return models.Profile{}, fmt.Errorf("profile creation failed (status %d) and fallback lookup returned %d", statusCode, getStatus)
		}

		var profiles []models.Profile
		if err := json.Unmarshal(body, &profiles); err != nil || len(profiles) == 0 {
			log.Printf("[auth] fallback GET profiles returned no results or parse error: %v", err)
			return models.Profile{}, fmt.Errorf("profile creation failed (status %d) and no profile found by email", statusCode)
		}
		return profiles[0], nil
	}

	var created []models.Profile
	if err := json.Unmarshal(respBody, &created); err != nil || len(created) == 0 {
		// POST returned 201 but response couldn't be parsed — return
		// a locally constructed profile with the ID we generated.
		return models.Profile{
			ID:        profileID,
			Email:     userInfo.Email,
			FullName:  userInfo.Name,
			CreatedAt: now.Format(time.RFC3339Nano),
			UpdatedAt: now.Format(time.RFC3339Nano),
		}, nil
	}
	return created[0], nil
}

// Me returns the authenticated user's profile from the profiles table.
// This endpoint must be behind the Protected middleware.
func Me(c *fiber.Ctx) error {
	userID, ok := requireUserID(c)
	if !ok {
		return errUnauthorized(c)
	}

	query := database.NewFilter().
		Select("id,email,full_name,created_at,updated_at").
		Eq("id", userID.String()).
		Build()

	body, statusCode, err := database.DB.Get("profiles", query)
	if err != nil || statusCode != http.StatusOK {
		return errInternal(c, "failed to fetch profile")
	}

	var profiles []models.Profile
	if err := json.Unmarshal(body, &profiles); err != nil {
		return errInternal(c, "failed to parse profile")
	}

	if len(profiles) == 0 {
		return errNotFound(c, "profile not found")
	}

	return c.JSON(profiles[0])
}

// UpdateProfile updates the authenticated user's profile (currently only name).
func UpdateProfile(c *fiber.Ctx) error {
	userID, ok := requireUserID(c)
	if !ok {
		return errUnauthorized(c)
	}

	var req struct {
		FullName string `json:"full_name"`
	}
	if err := c.BodyParser(&req); err != nil {
		return errBadRequest(c, "invalid request body")
	}

	req.FullName = strings.TrimSpace(req.FullName)
	if req.FullName == "" {
		return errBadRequest(c, "name cannot be empty")
	}
	if len(req.FullName) > 100 {
		return errBadRequest(c, "name too long (max 100 characters)")
	}

	ctx := context.Background()
	_, err := database.DB.Pool.Exec(ctx,
		"UPDATE profiles SET full_name = $1, updated_at = NOW() WHERE id = $2",
		req.FullName, userID.String(),
	)
	if err != nil {
		return errInternal(c, "failed to update profile")
	}

	// Return updated profile.
	query := database.NewFilter().
		Select("id,email,full_name,created_at,updated_at").
		Eq("id", userID.String()).
		Build()

	body, statusCode, err := database.DB.Get("profiles", query)
	if err != nil || statusCode != http.StatusOK {
		return errInternal(c, "failed to fetch updated profile")
	}

	var profiles []models.Profile
	if err := json.Unmarshal(body, &profiles); err != nil || len(profiles) == 0 {
		return errInternal(c, "failed to parse updated profile")
	}

	return c.JSON(profiles[0])
}

// DeleteAccount permanently removes the authenticated user and all their data.
// This includes all owned budgets, sections, categories, expenses, invites,
// collaborator records, and the profile itself. Executes in a single transaction.
func DeleteAccount(c *fiber.Ctx) error {
	userID, ok := requireUserID(c)
	if !ok {
		return errUnauthorized(c)
	}

	uid := userID.String()
	ctx := context.Background()

	tx, err := database.DB.Pool.Begin(ctx)
	if err != nil {
		return errInternal(c, "failed to start transaction")
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// Collect all budget IDs owned by this user.
	rows, err := tx.Query(ctx, "SELECT id FROM budgets WHERE user_id = $1", uid)
	if err != nil {
		return errInternal(c, "failed to fetch budgets")
	}
	var budgetIDs []string
	for rows.Next() {
		var id string
		if scanErr := rows.Scan(&id); scanErr == nil {
			budgetIDs = append(budgetIDs, id)
		}
	}
	rows.Close()

	if len(budgetIDs) > 0 {
		// Delete expenses for all owned budgets.
		if _, err = tx.Exec(ctx, "DELETE FROM budget_expenses WHERE budget_id = ANY($1::uuid[])", budgetIDs); err != nil {
			return errInternal(c, "failed to delete expenses")
		}

		// Collect section IDs to delete nested subcategories.
		secRows, err := tx.Query(ctx, "SELECT id FROM budget_categories WHERE budget_id = ANY($1::uuid[])", budgetIDs)
		if err != nil {
			return errInternal(c, "failed to fetch sections")
		}
		var sectionIDs []string
		for secRows.Next() {
			var id string
			if scanErr := secRows.Scan(&id); scanErr == nil {
				sectionIDs = append(sectionIDs, id)
			}
		}
		secRows.Close()

		if len(sectionIDs) > 0 {
			if _, err = tx.Exec(ctx, "DELETE FROM budget_subcategories WHERE category_id = ANY($1::uuid[])", sectionIDs); err != nil {
				return errInternal(c, "failed to delete subcategories")
			}
		}

		if _, err = tx.Exec(ctx, "DELETE FROM budget_categories WHERE budget_id = ANY($1::uuid[])", budgetIDs); err != nil {
			return errInternal(c, "failed to delete sections")
		}

		if _, err = tx.Exec(ctx, "DELETE FROM budget_invites WHERE budget_id = ANY($1::uuid[])", budgetIDs); err != nil {
			return errInternal(c, "failed to delete budget invites")
		}

		if _, err = tx.Exec(ctx, "DELETE FROM budget_collaborators WHERE budget_id = ANY($1::uuid[])", budgetIDs); err != nil {
			return errInternal(c, "failed to delete budget collaborators")
		}

		if _, err = tx.Exec(ctx, "DELETE FROM budgets WHERE user_id = $1", uid); err != nil {
			return errInternal(c, "failed to delete budgets")
		}
	}

	// Remove user as collaborator on other people's budgets.
	if _, err = tx.Exec(ctx, "DELETE FROM budget_collaborators WHERE user_id = $1", uid); err != nil {
		return errInternal(c, "failed to remove collaborations")
	}

	// Delete any invites the user created on budgets they don't own.
	if _, err = tx.Exec(ctx, "DELETE FROM budget_invites WHERE created_by = $1", uid); err != nil {
		return errInternal(c, "failed to delete invites")
	}

	// Delete sessions.
	if _, err = tx.Exec(ctx, "DELETE FROM user_sessions WHERE user_id = $1", uid); err != nil {
		return errInternal(c, "failed to delete sessions")
	}

	// Finally, delete the profile itself.
	if _, err = tx.Exec(ctx, "DELETE FROM profiles WHERE id = $1", uid); err != nil {
		return errInternal(c, "failed to delete profile")
	}

	if err := tx.Commit(ctx); err != nil {
		return errInternal(c, "failed to commit account deletion")
	}

	return c.SendStatus(fiber.StatusNoContent)
}
