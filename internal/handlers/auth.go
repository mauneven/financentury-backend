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
	rediscache "github.com/the-financial-workspace/backend/internal/redis"
)

// authMeCacheTTL bounds the staleness window of /auth/me payloads in Redis.
// Profile name + display orders rarely change for a given user; a 60-second
// TTL absorbs the burst of /auth/me calls every dashboard mount fires.
// Mutations (UpdateProfile, DeleteAccount) explicitly invalidate so the next
// call rebuilds from the DB regardless of TTL.
const authMeCacheTTL = 60 * time.Second

// authMeCacheKey returns the Redis key for a user's /auth/me payload. Scoped
// per-user so a stale entry can never leak across accounts.
func authMeCacheKey(userID uuid.UUID) string {
	return "auth:me:" + userID.String()
}

// profileCacheTTL is longer because we cache the raw profile row used by
// summary's spending_by_user join, and that row mutates rarely.
const profileCacheTTL = 5 * time.Minute

// profileCacheKey is the Redis key for a single profile row. Used by the
// cross-handler profile loader so summary/trends/etc. can deduplicate a
// scattering of identical profile lookups.
func profileCacheKey(userID uuid.UUID) string {
	return "profile:" + userID.String()
}

// maskEmail returns a masked version of an email address for safe logging,
// showing only the first 2 characters of the local part plus the domain.
func maskEmail(email string) string {
	parts := strings.SplitN(email, "@", 2)
	if len(parts) != 2 || len(parts[0]) < 2 {
		return "***"
	}
	return parts[0][:2] + "***@" + parts[1]
}

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
	isHTTPS := parsed.Scheme == "https"
	isLocalHTTP := parsed.Scheme == "http" &&
		(parsed.Hostname() == "localhost" || parsed.Hostname() == "127.0.0.1")
	if !isHTTPS && !isLocalHTTP {
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
	defer func() { _ = tokenResp.Body.Close() }()

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
	defer func() { _ = userInfoResp.Body.Close() }()

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
		log.Printf("[auth] upsertProfile failed for %s: %v", maskEmail(userInfo.Email), err)
		return errInternal(c, "failed to create or find user profile")
	}
	if profile.ID == uuid.Nil {
		log.Printf("[auth] upsertProfile returned nil ID for %s", maskEmail(userInfo.Email))
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
			"id":        profile.ID,
			"email":     profile.Email,
			"full_name": profile.FullName,
		},
	})
}

// googleMobileLoginRequest is the expected body for mobile Google login.
type googleMobileLoginRequest struct {
	IDToken string `json:"id_token"`
}

// googleTokenInfo is the response from Google's tokeninfo endpoint.
type googleTokenInfo struct {
	Email         string `json:"email"`
	EmailVerified string `json:"email_verified"`
	Name          string `json:"name"`
	Picture       string `json:"picture"`
	Sub           string `json:"sub"`
	Aud           string `json:"aud"`
}

// GoogleMobileLogin handles POST /api/auth/google/mobile. It verifies a Google
// ID token (obtained by the mobile SDK) directly with Google, upserts the
// profile, and returns a backend-issued JWT.
func GoogleMobileLogin(c *fiber.Ctx) error {
	var req googleMobileLoginRequest
	if err := c.BodyParser(&req); err != nil {
		return errBadRequest(c, "invalid request body")
	}
	if req.IDToken == "" {
		return errBadRequest(c, "id_token is required")
	}

	// Verify the ID token with Google's tokeninfo endpoint.
	httpClient := &http.Client{Timeout: oauthHTTPTimeout}
	resp, err := httpClient.Get("https://oauth2.googleapis.com/tokeninfo?id_token=" + url.QueryEscape(req.IDToken))
	if err != nil {
		return errInternal(c, "failed to verify id_token with Google")
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxOAuthResponseSize))
	if err != nil {
		return errInternal(c, "failed to read token verification response")
	}
	if resp.StatusCode != http.StatusOK {
		return c.Status(fiber.StatusUnauthorized).JSON(models.ErrorResponse{
			Error: "invalid or expired id_token",
		})
	}

	var tokenInfo googleTokenInfo
	if err := json.Unmarshal(body, &tokenInfo); err != nil {
		return errInternal(c, "failed to parse token verification response")
	}

	// Validate the audience matches our client ID.
	if tokenInfo.Aud != googleClientID {
		return c.Status(fiber.StatusUnauthorized).JSON(models.ErrorResponse{
			Error: "id_token audience mismatch",
		})
	}

	if tokenInfo.Email == "" {
		return c.Status(fiber.StatusUnauthorized).JSON(models.ErrorResponse{
			Error: "no email in id_token",
		})
	}
	if tokenInfo.EmailVerified != "true" {
		return c.Status(fiber.StatusUnauthorized).JSON(models.ErrorResponse{
			Error: "email address is not verified by Google",
		})
	}

	// Reuse the same upsert logic as web login.
	userInfo := googleUserInfo{
		ID:            tokenInfo.Sub,
		Email:         tokenInfo.Email,
		VerifiedEmail: true,
		Name:          tokenInfo.Name,
		Picture:       tokenInfo.Picture,
	}

	profile, err := upsertProfile(userInfo)
	if err != nil {
		log.Printf("[auth] upsertProfile failed for %s: %v", maskEmail(userInfo.Email), err)
		return errInternal(c, "failed to create or find user profile")
	}
	if profile.ID == uuid.Nil {
		log.Printf("[auth] upsertProfile returned nil ID for %s", maskEmail(userInfo.Email))
		return errInternal(c, "failed to create or find user profile")
	}

	token, err := middleware.GenerateToken(profile.ID, profile.Email)
	if err != nil {
		return errInternal(c, "failed to generate token")
	}

	CreateSession(profile.ID, token, c)

	return c.JSON(fiber.Map{
		"token": token,
		"user": fiber.Map{
			"id":        profile.ID,
			"email":     profile.Email,
			"full_name": profile.FullName,
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
	parseErr := json.Unmarshal(respBody, &created)
	if parseErr != nil || len(created) == 0 {
		// POST returned 201 but response couldn't be parsed — log the parse
		// error then return a locally constructed profile with the ID we
		// generated. Falling back to a synthetic profile is intentional: the
		// row exists (status 201) and we would rather continue the sign-in
		// than fail on a serialization edge case.
		if parseErr != nil {
			log.Printf("[auth] post-insert profile parse failed (ignored, using fallback): %v", parseErr)
		}
		return models.Profile{
			ID:        profileID,
			Email:     userInfo.Email,
			FullName:  userInfo.Name,
			CreatedAt: now.Format(time.RFC3339Nano),
			UpdatedAt: now.Format(time.RFC3339Nano),
		}, nil //nolint:nilerr // intentional fallback — see comment above
	}
	return created[0], nil
}

// Me returns the authenticated user's profile from the profiles table,
// including their display order preferences to avoid a separate GET.
// This endpoint must be behind the Protected middleware.
//
// PERF: Profile lookup and display_orders aggregation are now folded into a
// single query. The previous path ran two sequential round-trips (profile
// SELECT + display_orders SELECT). The rewritten query uses a correlated
// sub-SELECT with json_agg so the result arrives as one row on one
// round-trip. /auth/me is called on every cold-start and token refresh, so
// this saves 1 DB query per call on the hottest authenticated endpoint.
//
// PERF: Redis cache-aside (auth:me:<userid>, 60s TTL) deflects repeat
// /auth/me hits from many devices / tabs into a single DB read. Mutations
// (UpdateProfile, DeleteAccount) invalidate the entry so the next read
// reflects the new state immediately.
func Me(c *fiber.Ctx) error {
	userID, ok := requireUserID(c)
	if !ok {
		return errUnauthorized(c)
	}

	reqCtx := c.Context()
	cacheKey := authMeCacheKey(userID)

	payload, err := rediscache.CacheAside(reqCtx, cacheKey, authMeCacheTTL,
		func(ctx context.Context) ([]byte, error) {
			var p models.Profile
			var createdAt, updatedAt time.Time
			var ordersJSON []byte
			// PERF: single query — profile columns + aggregated display_orders as
			// JSON. COALESCE keeps the result shape stable when the user has no
			// saved orders (returns []). The sub-SELECT is cheaper than a LEFT JOIN
			// LATERAL because we never need the full cross-product.
			if dbErr := database.DB.Pool.QueryRow(ctx,
				`SELECT p.id, p.email, p.full_name, p.created_at, p.updated_at,
				        COALESCE((
				          SELECT json_agg(json_build_object(
				            'scope_key', d.scope_key,
				            'ordered_ids', d.ordered_ids
				          ))
				          FROM display_orders d
				          WHERE d.user_id = p.id
				        ), '[]'::json) AS display_orders
				 FROM profiles p
				 WHERE p.id = $1`, userID,
			).Scan(&p.ID, &p.Email, &p.FullName, &createdAt, &updatedAt, &ordersJSON); dbErr != nil {
				return nil, dbErr
			}
			p.CreatedAt = createdAt.Format(time.RFC3339Nano)
			p.UpdatedAt = updatedAt.Format(time.RFC3339Nano)

			ordersMsg := json.RawMessage(ordersJSON)
			if len(ordersMsg) == 0 {
				ordersMsg = json.RawMessage("[]")
			}

			return json.Marshal(fiber.Map{
				"id":             p.ID,
				"email":          p.Email,
				"full_name":      p.FullName,
				"display_orders": ordersMsg,
			})
		},
	)
	if err != nil {
		log.Printf("Me: profile lookup failed for user %s: %v", userID, err)
		return errNotFound(c, "profile not found")
	}

	c.Set("Content-Type", "application/json")
	return c.Send(payload)
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

	// UPDATE ... RETURNING folds the post-update fetch into a single
	// round-trip, so we don't need a follow-up GET.
	var p models.Profile
	var createdAt, updatedAt time.Time
	err := database.DB.Pool.QueryRow(c.Context(),
		`UPDATE profiles SET full_name = $1, updated_at = NOW()
		 WHERE id = $2
		 RETURNING id, email, full_name, created_at, updated_at`,
		req.FullName, userID,
	).Scan(&p.ID, &p.Email, &p.FullName, &createdAt, &updatedAt)
	if err != nil {
		log.Printf("UpdateProfile: failed for user %s: %v", userID, err)
		return errInternal(c, "failed to update profile")
	}
	p.CreatedAt = createdAt.Format(time.RFC3339Nano)
	p.UpdatedAt = updatedAt.Format(time.RFC3339Nano)

	// PERF: drop cached /auth/me + profile entries so the next read reflects
	// the new full_name. TTL would otherwise paper over the change for ~60s.
	rediscache.Delete(c.Context(), authMeCacheKey(userID), profileCacheKey(userID))

	return c.JSON(p)
}

// DeleteAccount permanently removes the authenticated user and all their data.
// This includes all owned budgets, categories, expenses, invites,
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

		// Delete flat categories for all owned budgets.
		if _, err = tx.Exec(ctx, "DELETE FROM budget_categories WHERE budget_id = ANY($1::uuid[])", budgetIDs); err != nil {
			return errInternal(c, "failed to delete categories")
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

	// Null out foreign-key references from other users' data so the profile
	// delete below does not violate the non-CASCADE references. Without this,
	// deleting a profile fails if the user accepted an invite on another
	// budget or authored an expense as a collaborator on someone else's budget.
	if _, err = tx.Exec(ctx, "UPDATE budget_invites SET used_by = NULL WHERE used_by = $1", uid); err != nil {
		return errInternal(c, "failed to clear invite references")
	}
	if _, err = tx.Exec(ctx, "UPDATE budget_expenses SET created_by = NULL WHERE created_by = $1", uid); err != nil {
		return errInternal(c, "failed to clear expense references")
	}
	if _, err = tx.Exec(ctx, "UPDATE budget_links SET created_by = NULL WHERE created_by = $1", uid); err != nil {
		return errInternal(c, "failed to clear link references")
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

	// SECURITY: after the transaction commits, nuke ALL cached session
	// entries for this user so every outstanding token is rejected on its
	// next request. The user's session rows have been DELETEd, so the DB
	// path would also reject them (no matching row → 401 semantics depend
	// on the middleware's fallthrough, but the cache path is the hot path
	// to guard). Invalidating after Commit avoids a race where a concurrent
	// request could repopulate the cache from still-extant rows between
	// cache-delete and tx-commit.
	middleware.InvalidateUserSessionCache(uid)

	// PERF/SECURITY: drop the user's cached /auth/me + profile + budget
	// list entries so a stale read can never serve data for a deleted
	// account. The DB rows are gone — these caches must follow.
	rediscache.Delete(c.Context(),
		authMeCacheKey(userID),
		profileCacheKey(userID),
		budgetsListCacheKey(userID),
	)

	return c.SendStatus(fiber.StatusNoContent)
}
