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
	MessageTypeCategoryCreated = "category_created"
	MessageTypeCategoryUpdated = "category_updated"
	MessageTypeCategoryDeleted = "category_deleted"
	MessageTypeExpenseCreated  = "expense_created"
	MessageTypeExpenseUpdated  = "expense_updated"
	MessageTypeExpenseDeleted  = "expense_deleted"
	MessageTypeCollabAdded     = "collaborator_added"
	MessageTypeCollabRemoved   = "collaborator_removed"
	MessageTypeLinkCreated     = "link_created"
	MessageTypeLinkUpdated     = "link_updated"
	MessageTypeLinkDeleted     = "link_deleted"
)

// pingIntervalDuration is how often the server sends a ping to keep the connection alive.
const pingIntervalDuration = 30 * time.Second

// pongWaitDuration is how long the server waits for a pong response before closing.
const pongWaitDuration = 60 * time.Second

// PingInterval returns the interval between server pings.
func PingInterval() time.Duration { return pingIntervalDuration }

// PongWait returns the maximum time to wait for a pong response.
func PongWait() time.Duration { return pongWaitDuration }

// sendBufSize is the capacity of each client's outbound message buffer.
// If a client falls behind by this many messages it is considered slow and
// will be disconnected to avoid blocking all other clients.
const sendBufSize = 32

// Client represents a single WebSocket connection tied to a user.
//
// SAFETY: Client state is touched from three goroutines:
//  1. The hub event loop (Run) — assigns send, indexes BudgetIDs, and closes
//     send on unregister.
//  2. The per-client writePump — reads from send and writes to Conn.
//  3. The per-client read/ping goroutines in handlers/ws.go — call
//     WritePing / SubscribeToBudget / UnsubscribeFromBudget.
//
// All writes to Conn are serialized by mu. All mutations of BudgetIDs are
// serialized by mu. The send channel is only closed by the hub goroutine,
// only after the client has been removed from h.clients and every bucket,
// so no other goroutine will attempt a send-after-close.
type Client struct {
	Conn      *websocket.Conn
	UserID    string
	BudgetIDs map[string]bool // budget IDs this client has access to (guarded by mu)
	mu        sync.Mutex      // guards Conn writes and the BudgetIDs map
	send      chan []byte     // buffered outbound message channel (assigned and closed only by the hub goroutine)
	ejecting  bool            // set once by the hub when a slow-client drop has been scheduled; only read/written from the hub goroutine (Run), so it needs no lock. A *Client is single-use — create a fresh one per connection.
}

// WriteJSON sends a JSON-encoded message to the client in a thread-safe manner.
//
// SAFETY: c.mu serializes against writePump and WritePing so the three
// potential writers cannot interleave frames on the underlying gorilla
// connection (which is documented as unsafe for concurrent writes).
func (c *Client) WriteJSON(v interface{}) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.Conn.WriteJSON(v)
}

// WritePing sends a ping control frame to the client.
//
// SAFETY: Shares c.mu with writePump / WriteJSON — see WriteJSON for why.
func (c *Client) WritePing() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.Conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(10*time.Second))
}

// writePump drains the client's send channel and writes each message to the
// WebSocket connection. It exits when the send channel is closed (on
// unregister) so the goroutine does not leak.
//
// SAFETY: We acquire c.mu around every Conn.WriteMessage so that concurrent
// WritePing / WriteJSON callers (the ping goroutine in handlers/ws.go and
// the hub's synchronous writers) cannot interleave frames on the underlying
// gorilla connection. The loop terminates naturally when the hub closes
// c.send after removing the client from every bucket; we do not need an
// explicit done channel. On a connection-level write error we return early,
// letting the read loop in handlers/ws.go notice and trigger Unregister
// through its defer. Any further broadcasts fall into the default arm of
// the non-blocking send and are dropped until the Unregister is processed.
func (c *Client) writePump() {
	for data := range c.send {
		c.mu.Lock()
		err := c.Conn.WriteMessage(websocket.TextMessage, data)
		c.mu.Unlock()
		if err != nil {
			log.Printf("[ws] writePump error for user=%s: %v", c.UserID, err)
			return
		}
	}
}

// Message is the payload broadcast to WebSocket clients.
type Message struct {
	Type     string      `json:"type"`
	BudgetID string      `json:"budget_id"`
	Data     interface{} `json:"data,omitempty"`
}

// Hub manages all active WebSocket clients and broadcasts messages to
// clients that are subscribed to a given budget.
//
// PERF: Broadcast cost was O(N) per message because the event loop iterated
// every connected client and tested its BudgetIDs set. This made a single
// expense update cost N map lookups, which got painful as N grew into the
// hundreds. The hub now keeps a per-budget bucket map[string]map[*Client]struct{}
// so a broadcast walks only the clients actually subscribed to that budget
// — O(subscribers_of_budget) in the common case, with a constant per-budget
// bucket lookup. The full clients map is retained for ClientCount and for
// cleanup on disconnect.
//
// SAFETY: h.mu protects clients and budgetBuckets. The lock order is always
// client.mu -> h.mu (never the reverse), so callers that grab client.mu
// first and then interact with the hub cannot deadlock against the hub's
// Run goroutine, which uses the same order. The register / unregister /
// broadcast channels run serially on the single Run goroutine.
type Hub struct {
	clients       map[*Client]bool
	budgetBuckets map[string]map[*Client]struct{}
	mu            sync.RWMutex
	broadcast     chan broadcastRequest
	register      chan *Client
	unregister    chan *Client
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
		clients:       make(map[*Client]bool),
		budgetBuckets: make(map[string]map[*Client]struct{}),
		broadcast:     make(chan broadcastRequest, 256),
		register:      make(chan *Client),
		unregister:    make(chan *Client),
	}
}

// Run starts the hub event loop. It must be called in a goroutine.
//
// SAFETY: All three branches (register / unregister / broadcast) execute on
// this single goroutine, so they are mutually exclusive without any lock —
// the only lock we need is h.mu to serialize against external readers
// (ClientCount, BudgetSubscriberCount) and against Subscribe / Unsubscribe
// callers that mutate the bucket index from other goroutines.
func (h *Hub) Run() {
	for {
		select {
		case client := <-h.register:
			client.send = make(chan []byte, sendBufSize)
			go client.writePump()
			// SAFETY: Snapshot BudgetIDs under client.mu so we do not race
			// with a concurrent SubscribeToBudget / UnsubscribeFromBudget
			// call. Lock order is always client.mu -> h.mu, matching the
			// Subscribe / Unsubscribe paths, so there is no deadlock risk.
			client.mu.Lock()
			snap := make([]string, 0, len(client.BudgetIDs))
			for bid := range client.BudgetIDs {
				snap = append(snap, bid)
			}
			client.mu.Unlock()

			h.mu.Lock()
			h.clients[client] = true
			// PERF: Index the client into each of its subscribed budget
			// buckets. The set semantics (map key only) make add / remove
			// O(1) and allow a single client to be in many buckets.
			for _, bid := range snap {
				bucket := h.budgetBuckets[bid]
				if bucket == nil {
					bucket = make(map[*Client]struct{})
					h.budgetBuckets[bid] = bucket
				}
				bucket[client] = struct{}{}
			}
			h.mu.Unlock()
			log.Printf("[ws] client registered: user=%s", client.UserID)

		case client := <-h.unregister:
			// SAFETY: Never acquire client.mu here. Unregister must complete
			// even if the client's own read/write goroutines are stuck
			// holding client.mu (e.g. blocked on a slow Conn.Write). By
			// keeping this branch strictly h.mu-only we keep the hub loop
			// responsive and rule out a client.mu -> h.mu inversion.
			h.mu.Lock()
			if _, ok := h.clients[client]; ok {
				delete(h.clients, client)
				// SAFETY: Walk every bucket instead of reading
				// client.BudgetIDs. This is robust against a stale or
				// partially-mutated BudgetIDs map and avoids taking
				// client.mu inside h.mu. Missing-key delete on a
				// map[*Client]struct{} is O(1), so the total cost is
				// O(num_budgets_with_subscribers), which is bounded.
				for bid, bucket := range h.budgetBuckets {
					if _, inBucket := bucket[client]; inBucket {
						delete(bucket, client)
						if len(bucket) == 0 {
							delete(h.budgetBuckets, bid)
						}
					}
				}
				// SAFETY: close(client.send) is only ever called here,
				// only once, and only after the client has been removed
				// from every bucket. After this point no other goroutine
				// can reach `client.send <- data` through the hub because
				// the broadcast path walks bucket membership. That rules
				// out send-on-closed-channel panics.
				close(client.send)
				_ = client.Conn.Close()
			}
			h.mu.Unlock()
			log.Printf("[ws] client unregistered: user=%s", client.UserID)

		case req := <-h.broadcast:
			data, err := json.Marshal(req.msg)
			if err != nil {
				continue
			}
			// PERF: O(subscribers_of_budget) rather than O(total_clients).
			// SAFETY: We hold only the read lock for the bucket walk, and
			// the send on client.send is non-blocking (default arm). Slow
			// clients are ejected asynchronously via h.Unregister to avoid
			// re-entering h.mu as a writer while we still hold the read
			// lock. The `ejecting` flag dedupes repeated drops so a single
			// stalled client cannot spawn an Unregister goroutine per
			// broadcast before the unregister channel drains.
			h.mu.RLock()
			bucket := h.budgetBuckets[req.budgetID]
			for client := range bucket {
				select {
				case client.send <- data:
				default:
					if !client.ejecting {
						client.ejecting = true
						log.Printf("[ws] slow client user=%s, dropping", client.UserID)
						go h.Unregister(client)
					}
				}
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
//
// PERF: The client's BudgetIDs map is still the source of truth for the
// "which budgets can this client see" question. When a subscription is
// added after the client has already been registered we also index it into
// the hub's per-budget bucket so subsequent broadcasts find it without a
// full client walk.
//
// SAFETY: Lock order is client.mu -> h.mu, matching the Run register branch
// so we never risk a deadlock. If Register is racing with this call and
// wins h.mu first, Run will observe BudgetIDs[budgetID]==true when it
// snapshots under client.mu and will index the bucket itself; if we win
// h.mu first we see the client is not yet in h.clients and skip the bucket
// insert, leaving it to Run. Either path leaves the bucket consistent with
// BudgetIDs once both operations have settled.
func (h *Hub) SubscribeToBudget(client *Client, budgetID string) {
	client.mu.Lock()
	if client.BudgetIDs == nil {
		client.BudgetIDs = make(map[string]bool)
	}
	client.BudgetIDs[budgetID] = true
	client.mu.Unlock()

	h.mu.Lock()
	// Only index the client if it's actually registered with the hub —
	// otherwise Register will pick up the subscriptions in its own pass.
	if _, ok := h.clients[client]; ok {
		bucket := h.budgetBuckets[budgetID]
		if bucket == nil {
			bucket = make(map[*Client]struct{})
			h.budgetBuckets[budgetID] = bucket
		}
		bucket[client] = struct{}{}
	}
	h.mu.Unlock()
}

// UnsubscribeFromBudget removes a budget ID from the client's subscription
// set and from the per-budget bucket.
//
// PERF: This is the other half of budget-switch support. When a client
// navigates away from a budget the frontend may send an unsubscribe so
// further broadcasts are skipped even if the client is still online.
//
// SAFETY: Same client.mu -> h.mu ordering as SubscribeToBudget. The bucket
// delete is a no-op if the client was never indexed (e.g. a double
// unsubscribe, or an unsubscribe after the hub already cleaned up on
// unregister).
func (h *Hub) UnsubscribeFromBudget(client *Client, budgetID string) {
	client.mu.Lock()
	if client.BudgetIDs != nil {
		delete(client.BudgetIDs, budgetID)
	}
	client.mu.Unlock()

	h.mu.Lock()
	if bucket, ok := h.budgetBuckets[budgetID]; ok {
		delete(bucket, client)
		if len(bucket) == 0 {
			delete(h.budgetBuckets, budgetID)
		}
	}
	h.mu.Unlock()
}

// ClientCount returns the number of currently connected clients. Useful
// for health checks and monitoring.
func (h *Hub) ClientCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}

// BudgetSubscriberCount returns the number of clients subscribed to a given
// budget. Handy for debugging broadcast reach.
func (h *Hub) BudgetSubscriberCount(budgetID string) int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.budgetBuckets[budgetID])
}
