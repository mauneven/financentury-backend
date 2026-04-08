// Package ws provides a WebSocket hub for broadcasting real-time updates
// to connected clients. Each client is associated with a user and may be
// subscribed to one or more budget IDs so that only relevant messages are
// delivered.
package ws

import (
	"encoding/json"
	"log"
	"sync"
	"time"

	"github.com/fasthttp/websocket"
)

// MessageType constants define the types of real-time events pushed to clients.
const (
	MessageTypeBudgetCreated   = "budget_created"
	MessageTypeBudgetUpdated   = "budget_updated"
	MessageTypeBudgetDeleted   = "budget_deleted"
	MessageTypeSectionCreated  = "section_created"
	MessageTypeSectionUpdated  = "section_updated"
	MessageTypeSectionDeleted  = "section_deleted"
	MessageTypeCategoryCreated = "category_created"
	MessageTypeCategoryUpdated = "category_updated"
	MessageTypeCategoryDeleted = "category_deleted"
	MessageTypeExpenseCreated  = "expense_created"
	MessageTypeExpenseUpdated  = "expense_updated"
	MessageTypeExpenseDeleted  = "expense_deleted"
	MessageTypeCollabAdded     = "collaborator_added"
	MessageTypeCollabRemoved   = "collaborator_removed"
)

// pingIntervalDuration is how often the server sends a ping to keep the connection alive.
const pingIntervalDuration = 30 * time.Second

// pongWaitDuration is how long the server waits for a pong response before closing.
const pongWaitDuration = 60 * time.Second

// PingInterval returns the interval between server pings.
func PingInterval() time.Duration { return pingIntervalDuration }

// PongWait returns the maximum time to wait for a pong response.
func PongWait() time.Duration { return pongWaitDuration }

// Client represents a single WebSocket connection tied to a user.
type Client struct {
	Conn      *websocket.Conn
	UserID    string
	BudgetIDs map[string]bool // budget IDs this client has access to
	mu        sync.Mutex      // guards writes to the connection
}

// WriteJSON sends a JSON-encoded message to the client in a thread-safe manner.
func (c *Client) WriteJSON(v interface{}) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.Conn.WriteJSON(v)
}

// WritePing sends a ping control frame to the client.
func (c *Client) WritePing() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.Conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(10*time.Second))
}

// Message is the payload broadcast to WebSocket clients.
type Message struct {
	Type     string      `json:"type"`
	BudgetID string      `json:"budget_id"`
	Data     interface{} `json:"data,omitempty"`
}

// Hub manages all active WebSocket clients and broadcasts messages to
// clients that are subscribed to a given budget.
type Hub struct {
	clients    map[*Client]bool
	mu         sync.RWMutex
	broadcast  chan broadcastRequest
	register   chan *Client
	unregister chan *Client
}

// broadcastRequest pairs a message with the target budget ID.
type broadcastRequest struct {
	budgetID string
	msg      Message
}

// NewHub creates and returns a new Hub instance. Call Run in a goroutine
// to start processing events.
func NewHub() *Hub {
	return &Hub{
		clients:    make(map[*Client]bool),
		broadcast:  make(chan broadcastRequest, 256),
		register:   make(chan *Client),
		unregister: make(chan *Client),
	}
}

// Run starts the hub event loop. It must be called in a goroutine.
func (h *Hub) Run() {
	for {
		select {
		case client := <-h.register:
			h.mu.Lock()
			h.clients[client] = true
			h.mu.Unlock()
			log.Printf("[ws] client registered: user=%s", client.UserID)

		case client := <-h.unregister:
			h.mu.Lock()
			if _, ok := h.clients[client]; ok {
				delete(h.clients, client)
				_ = client.Conn.Close()
			}
			h.mu.Unlock()
			log.Printf("[ws] client unregistered: user=%s", client.UserID)

		case req := <-h.broadcast:
			h.mu.RLock()
			data, err := json.Marshal(req.msg)
			if err != nil {
				h.mu.RUnlock()
				continue
			}
			for client := range h.clients {
				// Only send to clients that have access to this budget.
				if !client.BudgetIDs[req.budgetID] {
					continue
				}
				func() {
					client.mu.Lock()
					defer client.mu.Unlock()
					if err := client.Conn.WriteMessage(websocket.TextMessage, data); err != nil {
						log.Printf("[ws] write error for user=%s: %v", client.UserID, err)
					}
				}()
			}
			h.mu.RUnlock()
		}
	}
}

// Register adds a client to the hub.
func (h *Hub) Register(c *Client) {
	h.register <- c
}

// Unregister removes a client from the hub.
func (h *Hub) Unregister(c *Client) {
	h.unregister <- c
}

// BroadcastToBudget sends a message to all connected clients. The budget_id
// is included in the message so that frontend clients can filter locally.
func (h *Hub) BroadcastToBudget(budgetID string, msg Message) {
	msg.BudgetID = budgetID
	h.broadcast <- broadcastRequest{budgetID: budgetID, msg: msg}
}

// SubscribeToBudget adds a budget ID to the client's subscription set so it
// receives broadcasts for that budget.
func (h *Hub) SubscribeToBudget(client *Client, budgetID string) {
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.BudgetIDs == nil {
		client.BudgetIDs = make(map[string]bool)
	}
	client.BudgetIDs[budgetID] = true
}

// ClientCount returns the number of currently connected clients. Useful
// for health checks and monitoring.
func (h *Hub) ClientCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}
