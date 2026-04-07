package handlers

import (
	"encoding/json"
	"io"
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

// Package-level Google OAuth credentials.
var googleClientID, googleClientSecret string

// allowedRedirectHosts stores the set of hosts that are permitted as OAuth redirect targets.
var allowedRedirectHosts []string

// InitAuth configures the auth handler with Google OAuth credentials and allowed redirect origins.
func InitAuth(clientID, clientSecret string, allowedOrigins ...string) {
	googleClientID = clientID
	googleClientSecret = clientSecret
	allowedRedirectHosts = allowedOrigins
}

// isAllowedRedirectURI validates the redirect_uri against the configured allowlist.
// Only origins that were explicitly registered are accepted. This prevents open
// redirect attacks where an attacker tricks the backend into sending the auth code
// to a domain they control.
func isAllowedRedirectURI(redirectURI string) bool {
	parsed, err := url.Parse(redirectURI)
	if err != nil {
		return false
	}
	// Must be an absolute URL with https (or http for localhost dev).
	if parsed.Scheme != "https" && !(parsed.Scheme == "http" && (parsed.Hostname() == "localhost" || parsed.Hostname() == "127.0.0.1")) {
		return false
	}
	// The path must be exactly /auth/callback -- no query, no fragment.
	if parsed.Path != "/auth/callback" {
		return false
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return false
	}
	// Check the scheme+host against the allowlist.
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

// GoogleLogin handles POST /api/auth/google.
// It exchanges a Google authorization code for user info, upserts the profile,
// and returns a backend-issued JWT.
func GoogleLogin(c *fiber.Ctx) error {
	var req googleLoginRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(models.ErrorResponse{
			Error: "invalid request body",
		})
	}

	if req.Code == "" {
		return c.Status(fiber.StatusBadRequest).JSON(models.ErrorResponse{
			Error: "code is required",
		})
	}
	if req.RedirectURI == "" {
		return c.Status(fiber.StatusBadRequest).JSON(models.ErrorResponse{
			Error: "redirect_uri is required",
		})
	}

	// Validate redirect_uri against the allowlist to prevent open redirect attacks.
	if !isAllowedRedirectURI(req.RedirectURI) {
		return c.Status(fiber.StatusBadRequest).JSON(models.ErrorResponse{
			Error: "redirect_uri is not allowed",
		})
	}

	// Exchange authorization code for tokens.
	tokenData := url.Values{
		"client_id":     {googleClientID},
		"client_secret": {googleClientSecret},
		"code":          {req.Code},
		"grant_type":    {"authorization_code"},
		"redirect_uri":  {req.RedirectURI},
	}

	// Use a timeout-protected HTTP client for the token exchange.
	tokenHTTPClient := &http.Client{Timeout: 10 * time.Second}
	tokenResp, err := tokenHTTPClient.Post(
		"https://oauth2.googleapis.com/token",
		"application/x-www-form-urlencoded",
		strings.NewReader(tokenData.Encode()),
	)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{
			Error: "failed to exchange authorization code",
		})
	}
	defer tokenResp.Body.Close()

	// Limit response body size to 1MB to prevent memory exhaustion.
	tokenBody, err := io.ReadAll(io.LimitReader(tokenResp.Body, 1<<20))
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{
			Error: "failed to read token response",
		})
	}

	if tokenResp.StatusCode != http.StatusOK {
		return c.Status(fiber.StatusUnauthorized).JSON(models.ErrorResponse{
			Error: "failed to exchange authorization code with Google",
		})
	}

	var tokenResult googleTokenResponse
	if err := json.Unmarshal(tokenBody, &tokenResult); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{
			Error: "failed to parse token response",
		})
	}

	if tokenResult.AccessToken == "" {
		return c.Status(fiber.StatusUnauthorized).JSON(models.ErrorResponse{
			Error: "no access token received from Google",
		})
	}

	// Fetch user info from Google.
	userInfoReq, err := http.NewRequest("GET", "https://www.googleapis.com/oauth2/v2/userinfo", nil)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{
			Error: "failed to create userinfo request",
		})
	}
	userInfoReq.Header.Set("Authorization", "Bearer "+tokenResult.AccessToken)

	httpClient := &http.Client{Timeout: 10 * time.Second}
	userInfoResp, err := httpClient.Do(userInfoReq)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{
			Error: "failed to fetch user info from Google",
		})
	}
	defer userInfoResp.Body.Close()

	// Limit response body size to 1MB.
	userInfoBody, err := io.ReadAll(io.LimitReader(userInfoResp.Body, 1<<20))
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{
			Error: "failed to read user info response",
		})
	}

	if userInfoResp.StatusCode != http.StatusOK {
		return c.Status(fiber.StatusUnauthorized).JSON(models.ErrorResponse{
			Error: "failed to fetch user info from Google",
		})
	}

	var userInfo googleUserInfo
	if err := json.Unmarshal(userInfoBody, &userInfo); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{
			Error: "failed to parse user info",
		})
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
	query := database.NewFilter().
		Select("id,email,full_name,avatar_url,created_at,updated_at").
		Eq("email", userInfo.Email).
		Build()

	body, statusCode, err := database.DB.Get("profiles", query)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{
			Error: "failed to query profiles",
		})
	}

	var profile models.Profile

	if statusCode == http.StatusOK {
		var profiles []models.Profile
		if err := json.Unmarshal(body, &profiles); err == nil && len(profiles) > 0 {
			// Profile exists - update full_name and avatar_url.
			profile = profiles[0]

			now := time.Now().UTC()
			updatePayload := map[string]interface{}{
				"full_name":  userInfo.Name,
				"avatar_url": userInfo.Picture,
				"updated_at": now.Format(time.RFC3339Nano),
			}
			updateBytes, err := json.Marshal(updatePayload)
			if err == nil {
				patchQuery := database.NewFilter().
					Eq("id", profile.ID.String()).
					Build()
				database.DB.Patch("profiles", patchQuery, updateBytes)
			}

			profile.FullName = userInfo.Name
			profile.AvatarURL = userInfo.Picture
		} else {
			// No profile found - create a new one.
			profile = createNewProfile(userInfo)
		}
	} else {
		// Error fetching - try to create new profile anyway.
		profile = createNewProfile(userInfo)
	}

	if profile.ID == uuid.Nil {
		return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{
			Error: "failed to create or find user profile",
		})
	}

	// Generate backend JWT.
	token, err := middleware.GenerateToken(profile.ID, profile.Email)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{
			Error: "failed to generate token",
		})
	}

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

// createNewProfile creates a new profile in the database from Google user info.
func createNewProfile(userInfo googleUserInfo) models.Profile {
	now := time.Now().UTC()
	profileID := uuid.New()

	payload := map[string]interface{}{
		"id":         profileID.String(),
		"email":      userInfo.Email,
		"full_name":  userInfo.Name,
		"avatar_url": userInfo.Picture,
		"created_at": now.Format(time.RFC3339Nano),
		"updated_at": now.Format(time.RFC3339Nano),
	}

	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return models.Profile{}
	}

	respBody, statusCode, err := database.DB.Post("profiles", payloadBytes)
	if err != nil || statusCode != http.StatusCreated {
		// If insertion fails (e.g. race condition), try to fetch by email.
		query := database.NewFilter().
			Select("id,email,full_name,avatar_url,created_at,updated_at").
			Eq("email", userInfo.Email).
			Build()

		body, statusCode, err := database.DB.Get("profiles", query)
		if err != nil || statusCode != http.StatusOK {
			return models.Profile{}
		}

		var profiles []models.Profile
		if err := json.Unmarshal(body, &profiles); err != nil || len(profiles) == 0 {
			return models.Profile{}
		}
		return profiles[0]
	}

	var created []models.Profile
	if err := json.Unmarshal(respBody, &created); err != nil || len(created) == 0 {
		return models.Profile{
			ID:        profileID,
			Email:     userInfo.Email,
			FullName:  userInfo.Name,
			AvatarURL: userInfo.Picture,
			CreatedAt: now.Format(time.RFC3339Nano),
			UpdatedAt: now.Format(time.RFC3339Nano),
		}
	}
	return created[0]
}

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
