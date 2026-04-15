package handlers

import (
	"context"
	"encoding/json"
	"log"
	"strings"
	"time"

	"github.com/gofiber/contrib/websocket"
	"github.com/gofiber/fiber/v2"
	"github.com/golang-jwt/jwt/v5"
	"github.com/the-financial-workspace/backend/internal/database"
	"github.com/the-financial-workspace/backend/internal/middleware"
	"github.com/the-financial-workspace/backend/internal/ws"
)

// wsHub is the global WebSocket hub, set via InitWebSocket.
var wsHub *ws.Hub

// InitWebSocket configures the WebSocket handler with the hub instance.
func InitWebSocket(hub *ws.Hub) {
	wsHub = hub
}

// GetHub returns the global WebSocket hub for use in broadcast calls.
func GetHub() *ws.Hub {
	return wsHub
}

// WebSocketUpgrade is a Fiber middleware that checks whether the request
// is a WebSocket upgrade and allows it to proceed. Non-upgrade requests
// are rejected.
func WebSocketUpgrade() fiber.Handler {
	return func(c *fiber.Ctx) error {
		if websocket.IsWebSocketUpgrade(c) {
			return c.Next()
		}
		return fiber.ErrUpgradeRequired
	}
}

// WebSocketHandler handles WebSocket connections. The client authenticates
// by sending its JWT token as the first message after connection (type: "auth").
// This avoids exposing the token in query parameters (logged by servers/proxies).
func WebSocketHandler() fiber.Handler {
	return websocket.New(func(c *websocket.Conn) {
		// Set a short deadline for the auth message.
		_ = c.Conn.SetReadDeadline(time.Now().Add(10 * time.Second))

		// Read the first message which must be the auth payload.
		_, msg, err := c.ReadMessage()
		if err != nil {
			log.Println("[ws] connection rejected: failed to read auth message")
			_ = c.Close()
			return
		}

		var authMsg struct {
			Type  string `json:"type"`
			Token string `json:"token"`
		}
		if err := json.Unmarshal(msg, &authMsg); err != nil || authMsg.Type != "auth" || authMsg.Token == "" {
			log.Println("[ws] connection rejected: invalid auth message")
			_ = c.Close()
			return
		}

		// Parse the JWT to extract the user ID.
		userID, err := parseWSToken(authMsg.Token)
		if err != nil {
			log.Printf("[ws] connection rejected: invalid token: %v", err)
			_ = c.Close()
			return
		}

		// Limit incoming WebSocket message size to prevent memory exhaustion
		// from oversized or malicious payloads.
		c.Conn.SetReadLimit(4096)

		client := &ws.Client{
			Conn:   c.Conn,
			UserID: userID,
		}

		// Populate budget subscriptions so the hub delivers relevant messages.
		if budgetIDs, err := fetchUserBudgetIDs(userID); err == nil {
			client.BudgetIDs = budgetIDs
		}

		wsHub.Register(client)
		defer wsHub.Unregister(client)

		// Configure pong handler to reset the read deadline on each pong.
		_ = c.Conn.SetReadDeadline(time.Now().Add(ws.PongWait()))
		c.Conn.SetPongHandler(func(string) error {
			return c.Conn.SetReadDeadline(time.Now().Add(ws.PongWait()))
		})

		// Start a goroutine to send periodic pings.
		done := make(chan struct{})
		go func() {
			ticker := time.NewTicker(ws.PingInterval())
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					if err := client.WritePing(); err != nil {
						return
					}
				case <-done:
					return
				}
			}
		}()

		// Read loop: we don't expect meaningful messages from the client,
		// but we must read to detect disconnection.
		for {
			_, _, err := c.ReadMessage()
			if err != nil {
				break
			}
		}

		close(done)
	})
}

// fetchUserBudgetIDs returns a map of all budget IDs the user has access to
// (owned + collaborated). Used to populate WS client subscriptions.
func fetchUserBudgetIDs(userID string) (map[string]bool, error) {
	if database.DB == nil || database.DB.Pool == nil {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	rows, err := database.DB.Pool.Query(ctx, `
		SELECT id::text FROM budgets WHERE user_id = $1
		UNION
		SELECT budget_id::text FROM budget_collaborators WHERE user_id = $1
	`, userID)
	if err != nil {
		log.Printf("[ws] failed to fetch budget IDs for user %s: %v", userID, err)
		return nil, err
	}
	defer rows.Close()

	ids := make(map[string]bool)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err == nil {
			ids[id] = true
		}
	}
	return ids, nil
}

// parseWSToken validates a JWT token string and returns the user ID claim.
func parseWSToken(tokenStr string) (string, error) {
	claims := &middleware.Claims{}

	token, err := jwt.ParseWithClaims(tokenStr, claims, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, jwt.ErrSignatureInvalid
		}
		return middleware.JWTSecret(), nil
	}, jwt.WithIssuer("financial-workspace"))

	if err != nil || !token.Valid {
		return "", err
	}

	userID := strings.TrimSpace(claims.UserID)
	if userID == "" {
		return "", jwt.ErrTokenInvalidClaims
	}

	return userID, nil
}
