package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/the-financial-workspace/backend/internal/database"
	"github.com/the-financial-workspace/backend/internal/models"
)

// HashToken returns the SHA-256 hex digest of a JWT token string.
func HashToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

// CreateSession inserts a new session row for the given user and token.
// Call this after generating a JWT on login/register.
func CreateSession(userID uuid.UUID, token string, c *fiber.Ctx) {
	tokenHash := HashToken(token)
	ip := c.IP()
	ua := c.Get("User-Agent")
	deviceType, browser, osName := parseUserAgent(ua)

	expiresAt := time.Now().Add(7 * 24 * time.Hour)

	ctx := context.Background()
	if _, err := database.DB.Pool.Exec(ctx,
		`INSERT INTO user_sessions (id, user_id, token_hash, ip_address, device_type, browser, os, created_at, last_active_at, expires_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, NOW(), NOW(), $8)`,
		uuid.New().String(), userID.String(), tokenHash, ip, deviceType, browser, osName, expiresAt,
	); err != nil {
		log.Printf("[sessions] failed to create session: %v", err)
	}
}

// ListSessions returns all active (non-revoked, non-expired) sessions for
// the authenticated user. The current session is marked with is_current=true.
func ListSessions(c *fiber.Ctx) error {
	userID, ok := requireUserID(c)
	if !ok {
		return errUnauthorized(c)
	}

	tokenHash, _ := c.Locals("token_hash").(string)

	ctx := context.Background()
	rows, err := database.DB.Pool.Query(ctx,
		`SELECT id, ip_address, device_type, browser, os, token_hash, created_at, last_active_at
		 FROM user_sessions
		 WHERE user_id = $1 AND revoked_at IS NULL AND expires_at > NOW()
		 ORDER BY last_active_at DESC`,
		userID.String(),
	)
	if err != nil {
		return errInternal(c, "failed to fetch sessions")
	}
	defer rows.Close()

	sessions := make([]models.Session, 0)
	for rows.Next() {
		var s models.Session
		var rowTokenHash string
		if err := rows.Scan(&s.ID, &s.IPAddress, &s.DeviceType, &s.Browser, &s.OS, &rowTokenHash, &s.CreatedAt, &s.LastActiveAt); err != nil {
			continue
		}
		s.IsCurrent = rowTokenHash == tokenHash
		sessions = append(sessions, s)
	}

	return c.JSON(sessions)
}

// RevokeSession revokes a specific session by marking it with revoked_at.
// Users can only revoke their own sessions, not the current one.
func RevokeSession(c *fiber.Ctx) error {
	userID, ok := requireUserID(c)
	if !ok {
		return errUnauthorized(c)
	}

	sessionID, ok := parseUUIDParam(c, "sessionId")
	if !ok {
		return errBadRequest(c, "invalid session ID")
	}

	tokenHash, _ := c.Locals("token_hash").(string)

	ctx := context.Background()

	// Check that the session belongs to this user and is not the current one.
	var rowTokenHash string
	err := database.DB.Pool.QueryRow(ctx,
		`SELECT token_hash FROM user_sessions WHERE id = $1 AND user_id = $2 AND revoked_at IS NULL`,
		sessionID.String(), userID.String(),
	).Scan(&rowTokenHash)

	if err != nil {
		return errNotFound(c, "session not found")
	}

	if rowTokenHash == tokenHash {
		return errBadRequest(c, "cannot revoke current session")
	}

	_, err = database.DB.Pool.Exec(ctx,
		`UPDATE user_sessions SET revoked_at = NOW() WHERE id = $1`,
		sessionID.String(),
	)
	if err != nil {
		return errInternal(c, "failed to revoke session")
	}

	return c.SendStatus(fiber.StatusNoContent)
}

// SignOut revokes the current session.
func SignOut(c *fiber.Ctx) error {
	tokenHash, _ := c.Locals("token_hash").(string)
	if tokenHash == "" {
		return c.SendStatus(fiber.StatusNoContent)
	}

	ctx := context.Background()
	if _, err := database.DB.Pool.Exec(ctx,
		`UPDATE user_sessions SET revoked_at = NOW() WHERE token_hash = $1`,
		tokenHash,
	); err != nil {
		log.Printf("[sessions] failed to revoke session on sign-out: %v", err)
	}

	return c.SendStatus(fiber.StatusNoContent)
}

// parseUserAgent extracts device type, browser, and OS from a User-Agent string.
func parseUserAgent(ua string) (deviceType, browser, osName string) {
	lower := strings.ToLower(ua)

	// Device type
	switch {
	case strings.Contains(lower, "ipad") || strings.Contains(lower, "tablet"):
		deviceType = "tablet"
	case strings.Contains(lower, "mobile") || strings.Contains(lower, "iphone") || (strings.Contains(lower, "android") && !strings.Contains(lower, "tablet")):
		deviceType = "mobile"
	default:
		deviceType = "desktop"
	}

	// Browser (order matters: check specific before generic)
	switch {
	case strings.Contains(lower, "edg/") || strings.Contains(lower, "edga/") || strings.Contains(lower, "edgios/"):
		browser = "Edge"
	case strings.Contains(lower, "opr/") || strings.Contains(lower, "opera"):
		browser = "Opera"
	case strings.Contains(lower, "firefox/") || strings.Contains(lower, "fxios/"):
		browser = "Firefox"
	case strings.Contains(lower, "crios/"):
		browser = "Chrome"
	case strings.Contains(lower, "chrome/") && !strings.Contains(lower, "edg/") && !strings.Contains(lower, "opr/"):
		browser = "Chrome"
	case strings.Contains(lower, "safari/") && !strings.Contains(lower, "chrome/") && !strings.Contains(lower, "crios/"):
		browser = "Safari"
	default:
		browser = "Unknown"
	}

	// OS
	switch {
	case strings.Contains(lower, "iphone") || strings.Contains(lower, "ipad") || strings.Contains(lower, "ipod"):
		osName = "iOS"
	case strings.Contains(lower, "android"):
		osName = "Android"
	case strings.Contains(lower, "windows"):
		osName = "Windows"
	case strings.Contains(lower, "macintosh") || strings.Contains(lower, "mac os"):
		osName = "macOS"
	case strings.Contains(lower, "cros"):
		osName = "ChromeOS"
	case strings.Contains(lower, "linux"):
		osName = "Linux"
	default:
		osName = "Unknown"
	}

	return
}
