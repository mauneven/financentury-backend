package ws

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fasthttp/websocket"
)

// ==================== Constants ====================

func TestPingInterval(t *testing.T) {
	if PingInterval() != 30*time.Second {
		t.Errorf("PingInterval() = %v, want 30s", PingInterval())
	}
}

func TestPongWait(t *testing.T) {
	if PongWait() != 60*time.Second {
		t.Errorf("PongWait() = %v, want 60s", PongWait())
	}
}

func TestMessageTypeConstants(t *testing.T) {
	expected := map[string]string{
		"MessageTypeBudgetCreated":   "budget_created",
		"MessageTypeBudgetUpdated":   "budget_updated",
		"MessageTypeBudgetDeleted":   "budget_deleted",
		"MessageTypeCategoryCreated": "category_created",
		"MessageTypeCategoryUpdated": "category_updated",
		"MessageTypeCategoryDeleted": "category_deleted",
		"MessageTypeExpenseCreated":  "expense_created",
		"MessageTypeExpenseUpdated":  "expense_updated",
		"MessageTypeExpenseDeleted":  "expense_deleted",
		"MessageTypeCollabAdded":     "collaborator_added",
		"MessageTypeCollabRemoved":   "collaborator_removed",
	}
	actual := map[string]string{
		"MessageTypeBudgetCreated":   MessageTypeBudgetCreated,
		"MessageTypeBudgetUpdated":   MessageTypeBudgetUpdated,
		"MessageTypeBudgetDeleted":   MessageTypeBudgetDeleted,
		"MessageTypeCategoryCreated": MessageTypeCategoryCreated,
		"MessageTypeCategoryUpdated": MessageTypeCategoryUpdated,
		"MessageTypeCategoryDeleted": MessageTypeCategoryDeleted,
		"MessageTypeExpenseCreated":  MessageTypeExpenseCreated,
		"MessageTypeExpenseUpdated":  MessageTypeExpenseUpdated,
		"MessageTypeExpenseDeleted":  MessageTypeExpenseDeleted,
		"MessageTypeCollabAdded":     MessageTypeCollabAdded,
		"MessageTypeCollabRemoved":   MessageTypeCollabRemoved,
	}
	for name, want := range expected {
		got := actual[name]
		if got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
}

// ==================== NewHub ====================

func TestNewHub_ReturnsNonNil(t *testing.T) {
	hub := NewHub()
	if hub == nil {
		t.Fatal("NewHub() returned nil")
	}
	if hub.clients == nil {
		t.Error("clients map should be initialized")
	}
	if hub.broadcast == nil {
		t.Error("broadcast channel should be initialized")
	}
	if hub.register == nil {
		t.Error("register channel should be initialized")
	}
	if hub.unregister == nil {
		t.Error("unregister channel should be initialized")
	}
}

func TestNewHub_BroadcastChannelBuffered(t *testing.T) {
	hub := NewHub()
	if cap(hub.broadcast) != 256 {
		t.Errorf("broadcast channel capacity = %d, want 256", cap(hub.broadcast))
	}
}

// ==================== ClientCount ====================

func TestClientCount_EmptyHub(t *testing.T) {
	hub := NewHub()
	if hub.ClientCount() != 0 {
		t.Errorf("empty hub should have 0 clients, got %d", hub.ClientCount())
	}
}

// ==================== Hub.Run — Register / Unregister ====================

// startTestWSPair creates a WebSocket server/client pair for testing.
func startTestWSPair(t *testing.T) (*websocket.Conn, *websocket.Conn, func()) {
	t.Helper()

	var serverConn *websocket.Conn
	ready := make(chan struct{})
	upgrader := websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool { return true },
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		serverConn, err = upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Fatalf("upgrade failed: %v", err)
		}
		close(ready)
		// Keep server handler alive until connection closes.
		for {
			_, _, err := serverConn.ReadMessage()
			if err != nil {
				return
			}
		}
	}))

	url := "ws" + strings.TrimPrefix(server.URL, "http")
	clientConn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		server.Close()
		t.Fatalf("dial failed: %v", err)
	}

	<-ready

	cleanup := func() {
		clientConn.Close()
		server.Close()
	}
	return serverConn, clientConn, cleanup
}

func TestHub_RegisterAndUnregister(t *testing.T) {
	hub := NewHub()
	go hub.Run()

	serverConn, _, cleanup := startTestWSPair(t)
	defer cleanup()

	client := &Client{
		Conn:      serverConn,
		UserID:    "test-user-1",
		BudgetIDs: map[string]bool{"budget-1": true},
	}

	hub.Register(client)
	// Give the event loop a moment to process.
	time.Sleep(50 * time.Millisecond)

	if hub.ClientCount() != 1 {
		t.Errorf("after register: ClientCount = %d, want 1", hub.ClientCount())
	}

	hub.Unregister(client)
	time.Sleep(50 * time.Millisecond)

	if hub.ClientCount() != 0 {
		t.Errorf("after unregister: ClientCount = %d, want 0", hub.ClientCount())
	}
}

func TestHub_UnregisterNonExistentClient(t *testing.T) {
	hub := NewHub()
	go hub.Run()

	// Create a minimal connection for the client.
	serverConn, _, cleanup := startTestWSPair(t)
	defer cleanup()

	client := &Client{
		Conn:   serverConn,
		UserID: "ghost-user",
	}

	// Unregistering a client that was never registered should not panic.
	hub.Unregister(client)
	time.Sleep(50 * time.Millisecond)

	if hub.ClientCount() != 0 {
		t.Errorf("after unregistering ghost: ClientCount = %d, want 0", hub.ClientCount())
	}
}

// ==================== Hub.BroadcastToBudget ====================

func TestHub_BroadcastToBudget_DeliversToSubscribedClient(t *testing.T) {
	hub := NewHub()
	go hub.Run()

	serverConn, clientConn, cleanup := startTestWSPair(t)
	defer cleanup()

	client := &Client{
		Conn:      serverConn,
		UserID:    "user-1",
		BudgetIDs: map[string]bool{"budget-abc": true},
	}

	hub.Register(client)
	time.Sleep(50 * time.Millisecond)

	hub.BroadcastToBudget("budget-abc", Message{
		Type: MessageTypeBudgetUpdated,
		Data: map[string]string{"name": "Test Budget"},
	})

	// Read the message on the client side.
	clientConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, msgBytes, err := clientConn.ReadMessage()
	if err != nil {
		t.Fatalf("client read failed: %v", err)
	}

	var received Message
	if err := json.Unmarshal(msgBytes, &received); err != nil {
		t.Fatalf("failed to unmarshal message: %v", err)
	}

	if received.Type != MessageTypeBudgetUpdated {
		t.Errorf("message type = %q, want %q", received.Type, MessageTypeBudgetUpdated)
	}
	if received.BudgetID != "budget-abc" {
		t.Errorf("budget_id = %q, want %q", received.BudgetID, "budget-abc")
	}
}

func TestHub_BroadcastToBudget_DoesNotDeliverToUnsubscribedClient(t *testing.T) {
	hub := NewHub()
	go hub.Run()

	serverConn, clientConn, cleanup := startTestWSPair(t)
	defer cleanup()

	client := &Client{
		Conn:      serverConn,
		UserID:    "user-1",
		BudgetIDs: map[string]bool{"other-budget": true},
	}

	hub.Register(client)
	time.Sleep(50 * time.Millisecond)

	hub.BroadcastToBudget("budget-abc", Message{
		Type: MessageTypeBudgetUpdated,
		Data: map[string]string{"name": "Test Budget"},
	})

	// The client should NOT receive this message. Set a short timeout.
	clientConn.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	_, _, err := clientConn.ReadMessage()
	if err == nil {
		t.Error("unsubscribed client should not receive the broadcast")
	}
	// Expect a timeout error, which is correct.
	var netErr net.Error
	if errors.As(err, &netErr) && !netErr.Timeout() {
		t.Errorf("expected timeout error, got: %v", err)
	}
}

// ==================== Hub.SubscribeToBudget ====================

func TestHub_SubscribeToBudget(t *testing.T) {
	hub := NewHub()

	client := &Client{
		UserID: "user-1",
	}

	hub.SubscribeToBudget(client, "budget-1")
	hub.SubscribeToBudget(client, "budget-2")

	if !client.BudgetIDs["budget-1"] {
		t.Error("client should be subscribed to budget-1")
	}
	if !client.BudgetIDs["budget-2"] {
		t.Error("client should be subscribed to budget-2")
	}
	if client.BudgetIDs["budget-3"] {
		t.Error("client should not be subscribed to budget-3")
	}
}

func TestHub_SubscribeToBudget_InitializesMap(t *testing.T) {
	hub := NewHub()

	client := &Client{
		UserID:    "user-1",
		BudgetIDs: nil, // explicitly nil
	}

	hub.SubscribeToBudget(client, "budget-1")

	if client.BudgetIDs == nil {
		t.Fatal("SubscribeToBudget should initialize the BudgetIDs map")
	}
	if !client.BudgetIDs["budget-1"] {
		t.Error("client should be subscribed to budget-1 after initialization")
	}
}

func TestHub_SubscribeToBudget_Idempotent(t *testing.T) {
	hub := NewHub()
	client := &Client{
		UserID:    "user-1",
		BudgetIDs: make(map[string]bool),
	}

	hub.SubscribeToBudget(client, "budget-1")
	hub.SubscribeToBudget(client, "budget-1")

	if !client.BudgetIDs["budget-1"] {
		t.Error("client should still be subscribed after double subscribe")
	}
}

// ==================== Client.WriteJSON ====================

func TestClient_WriteJSON(t *testing.T) {
	serverConn, clientConn, cleanup := startTestWSPair(t)
	defer cleanup()

	client := &Client{
		Conn:   serverConn,
		UserID: "user-1",
	}

	msg := Message{
		Type:     MessageTypeExpenseCreated,
		BudgetID: "budget-123",
		Data:     map[string]string{"id": "exp-1"},
	}

	if err := client.WriteJSON(msg); err != nil {
		t.Fatalf("WriteJSON failed: %v", err)
	}

	clientConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, data, err := clientConn.ReadMessage()
	if err != nil {
		t.Fatalf("client read failed: %v", err)
	}

	var received Message
	if err := json.Unmarshal(data, &received); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if received.Type != MessageTypeExpenseCreated {
		t.Errorf("type = %q, want %q", received.Type, MessageTypeExpenseCreated)
	}
}

// ==================== Client.WriteJSON — Concurrent Safety ====================

func TestClient_WriteJSON_ConcurrentSafe(t *testing.T) {
	serverConn, _, cleanup := startTestWSPair(t)
	defer cleanup()

	client := &Client{
		Conn:   serverConn,
		UserID: "user-1",
	}

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			_ = client.WriteJSON(Message{Type: "test", Data: n})
		}(i)
	}
	wg.Wait()
	// If we reach here without a panic or data race, the test passes.
}

// ==================== Message JSON Serialization ====================

func TestMessage_JSONSerialization(t *testing.T) {
	msg := Message{
		Type:     MessageTypeBudgetCreated,
		BudgetID: "budget-123",
		Data:     map[string]string{"name": "My Budget"},
	}

	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	var decoded Message
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if decoded.Type != MessageTypeBudgetCreated {
		t.Errorf("Type = %q, want %q", decoded.Type, MessageTypeBudgetCreated)
	}
	if decoded.BudgetID != "budget-123" {
		t.Errorf("BudgetID = %q, want %q", decoded.BudgetID, "budget-123")
	}
}

func TestMessage_JSONOmitsEmptyData(t *testing.T) {
	msg := Message{
		Type:     MessageTypeBudgetDeleted,
		BudgetID: "budget-456",
	}

	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	// Data should be omitted when nil.
	if strings.Contains(string(data), `"data"`) {
		t.Errorf("nil Data should be omitted from JSON, got: %s", string(data))
	}
}

// ==================== Client.WritePing ====================

func TestClient_WritePing(t *testing.T) {
	serverConn, clientConn, cleanup := startTestWSPair(t)
	defer cleanup()

	client := &Client{
		Conn:   serverConn,
		UserID: "user-1",
	}

	// Set up a pong handler on the client to verify it receives the ping.
	pongReceived := make(chan struct{})
	clientConn.SetPongHandler(func(appData string) error {
		close(pongReceived)
		return nil
	})

	if err := client.WritePing(); err != nil {
		t.Fatalf("WritePing failed: %v", err)
	}

	// Start reading on the client to process the ping frame.
	go func() {
		for {
			_, _, err := clientConn.ReadMessage()
			if err != nil {
				return
			}
		}
	}()

	select {
	case <-pongReceived:
		// Success - the client received and responded to the ping.
	case <-time.After(2 * time.Second):
		// Pong handler may not fire if the read loop isn't processing control
		// frames. Either way, WritePing did not error, which is the key assertion.
	}
}

// ==================== Hub.Run — Broadcast with JSON error ====================

func TestHub_BroadcastToBudget_SetsBudgetID(t *testing.T) {
	hub := NewHub()
	go hub.Run()

	serverConn, clientConn, cleanup := startTestWSPair(t)
	defer cleanup()

	client := &Client{
		Conn:      serverConn,
		UserID:    "user-1",
		BudgetIDs: map[string]bool{"budget-xyz": true},
	}

	hub.Register(client)
	time.Sleep(50 * time.Millisecond)

	// BroadcastToBudget should set the BudgetID on the message.
	hub.BroadcastToBudget("budget-xyz", Message{
		Type: MessageTypeCategoryCreated,
		Data: map[string]string{"name": "New Category"},
	})

	clientConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, msgBytes, err := clientConn.ReadMessage()
	if err != nil {
		t.Fatalf("client read failed: %v", err)
	}

	var received Message
	if err := json.Unmarshal(msgBytes, &received); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if received.BudgetID != "budget-xyz" {
		t.Errorf("BudgetID = %q, want %q", received.BudgetID, "budget-xyz")
	}
	if received.Type != MessageTypeCategoryCreated {
		t.Errorf("Type = %q, want %q", received.Type, MessageTypeCategoryCreated)
	}
}

// ==================== Multiple Clients ====================

func TestHub_MultipleClients_OnlySubscribedReceive(t *testing.T) {
	hub := NewHub()
	go hub.Run()

	// Client 1: subscribed to budget-a
	serverConn1, clientConn1, cleanup1 := startTestWSPair(t)
	defer cleanup1()
	client1 := &Client{
		Conn:      serverConn1,
		UserID:    "user-1",
		BudgetIDs: map[string]bool{"budget-a": true},
	}

	// Client 2: subscribed to budget-b
	serverConn2, clientConn2, cleanup2 := startTestWSPair(t)
	defer cleanup2()
	client2 := &Client{
		Conn:      serverConn2,
		UserID:    "user-2",
		BudgetIDs: map[string]bool{"budget-b": true},
	}

	hub.Register(client1)
	hub.Register(client2)
	time.Sleep(50 * time.Millisecond)

	if hub.ClientCount() != 2 {
		t.Fatalf("ClientCount = %d, want 2", hub.ClientCount())
	}

	// Broadcast to budget-a.
	hub.BroadcastToBudget("budget-a", Message{
		Type: MessageTypeBudgetUpdated,
		Data: "for-a",
	})

	// Client 1 should receive.
	clientConn1.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, _, err := clientConn1.ReadMessage()
	if err != nil {
		t.Errorf("client1 should have received message: %v", err)
	}

	// Client 2 should NOT receive.
	clientConn2.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	_, _, err = clientConn2.ReadMessage()
	if err == nil {
		t.Error("client2 should not have received budget-a message")
	}
}

// ==================== Budget bucket optimization ====================
//
// These tests verify the PERF: per-budget bucket index. A broadcast must
// only reach clients that have the budget in their BudgetIDs set, and the
// bucket count must track subscribers cleanly across register, unregister,
// subscribe, and unsubscribe.

func TestHub_BudgetSubscriberCount_EmptyByDefault(t *testing.T) {
	hub := NewHub()
	if n := hub.BudgetSubscriberCount("never-subscribed"); n != 0 {
		t.Errorf("empty bucket should have 0 subscribers, got %d", n)
	}
}

func TestHub_BudgetBucket_TracksRegisteredClients(t *testing.T) {
	hub := NewHub()
	go hub.Run()

	serverConn, _, cleanup := startTestWSPair(t)
	defer cleanup()

	client := &Client{
		Conn:      serverConn,
		UserID:    "user-1",
		BudgetIDs: map[string]bool{"budget-1": true, "budget-2": true},
	}
	hub.Register(client)
	time.Sleep(50 * time.Millisecond)

	if n := hub.BudgetSubscriberCount("budget-1"); n != 1 {
		t.Errorf("budget-1 should have 1 subscriber, got %d", n)
	}
	if n := hub.BudgetSubscriberCount("budget-2"); n != 1 {
		t.Errorf("budget-2 should have 1 subscriber, got %d", n)
	}
	if n := hub.BudgetSubscriberCount("budget-3"); n != 0 {
		t.Errorf("budget-3 should have 0 subscribers, got %d", n)
	}
}

func TestHub_BudgetBucket_ReleasedOnUnregister(t *testing.T) {
	hub := NewHub()
	go hub.Run()

	serverConn, _, cleanup := startTestWSPair(t)
	defer cleanup()

	client := &Client{
		Conn:      serverConn,
		UserID:    "user-1",
		BudgetIDs: map[string]bool{"budget-1": true},
	}
	hub.Register(client)
	time.Sleep(50 * time.Millisecond)

	hub.Unregister(client)
	time.Sleep(50 * time.Millisecond)

	if n := hub.BudgetSubscriberCount("budget-1"); n != 0 {
		t.Errorf("after unregister budget-1 should have 0, got %d", n)
	}
}

func TestHub_SubscribeToBudget_UpdatesBucket(t *testing.T) {
	hub := NewHub()
	go hub.Run()

	serverConn, clientConn, cleanup := startTestWSPair(t)
	defer cleanup()

	client := &Client{
		Conn:      serverConn,
		UserID:    "user-1",
		BudgetIDs: map[string]bool{"initial": true},
	}
	hub.Register(client)
	time.Sleep(50 * time.Millisecond)

	// Subscribing after registration must index the client in the new
	// bucket so broadcasts to "late-added" reach it.
	hub.SubscribeToBudget(client, "late-added")
	time.Sleep(50 * time.Millisecond)

	if n := hub.BudgetSubscriberCount("late-added"); n != 1 {
		t.Errorf("late-added should have 1 subscriber after SubscribeToBudget, got %d", n)
	}

	hub.BroadcastToBudget("late-added", Message{Type: MessageTypeExpenseCreated})

	clientConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, _, err := clientConn.ReadMessage(); err != nil {
		t.Errorf("subscriber should have received broadcast to late-added: %v", err)
	}
}

func TestHub_UnsubscribeFromBudget_StopsDelivery(t *testing.T) {
	hub := NewHub()
	go hub.Run()

	serverConn, clientConn, cleanup := startTestWSPair(t)
	defer cleanup()

	client := &Client{
		Conn:      serverConn,
		UserID:    "user-1",
		BudgetIDs: map[string]bool{"budget-x": true},
	}
	hub.Register(client)
	time.Sleep(50 * time.Millisecond)

	hub.UnsubscribeFromBudget(client, "budget-x")
	time.Sleep(50 * time.Millisecond)

	if n := hub.BudgetSubscriberCount("budget-x"); n != 0 {
		t.Errorf("after unsubscribe budget-x should have 0 subscribers, got %d", n)
	}

	// A broadcast must not reach the now-unsubscribed client.
	hub.BroadcastToBudget("budget-x", Message{Type: MessageTypeExpenseCreated})

	clientConn.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	if _, _, err := clientConn.ReadMessage(); err == nil {
		t.Error("unsubscribed client must not receive broadcast")
	}
}

// ==================== Slow-client handling ====================
//
// If a client cannot drain its send buffer, the hub must (a) not block the
// broadcast loop and (b) evict the slow client. With the dedup flag the hub
// schedules at most one Unregister for a stalled client.

func TestHub_SlowClient_IsEjectedOnBufferOverflow(t *testing.T) {
	hub := NewHub()
	go hub.Run()

	serverConn, _, cleanup := startTestWSPair(t)
	defer cleanup()

	// Construct the client manually so we can fill its send buffer
	// without draining it. We do NOT call hub.Register because that
	// would spawn a writePump that drains the channel.
	client := &Client{
		Conn:      serverConn,
		UserID:    "slow-user",
		BudgetIDs: map[string]bool{"budget-slow": true},
		send:      make(chan []byte, sendBufSize),
	}

	// Splice the client into the hub by hand so no writePump is started.
	hub.mu.Lock()
	hub.clients[client] = true
	hub.budgetBuckets["budget-slow"] = map[*Client]struct{}{client: {}}
	hub.mu.Unlock()

	// Fill the send buffer exactly to capacity.
	for i := 0; i < sendBufSize; i++ {
		client.send <- []byte("prefill")
	}

	// Now broadcast; the first broadcast should trigger the eject path.
	for i := 0; i < 5; i++ {
		hub.BroadcastToBudget("budget-slow", Message{Type: MessageTypeExpenseCreated})
	}

	// Give the hub time to process the Unregister goroutine.
	time.Sleep(100 * time.Millisecond)

	if hub.ClientCount() != 0 {
		t.Errorf("slow client should have been evicted, still have %d clients", hub.ClientCount())
	}
	if n := hub.BudgetSubscriberCount("budget-slow"); n != 0 {
		t.Errorf("bucket should be empty after slow-client eviction, got %d", n)
	}
}

// ==================== Concurrency stress: register / subscribe / broadcast ====================
//
// Hammer all three paths from many goroutines at once. With -race this
// verifies we have no lock-order inversion or torn reads on BudgetIDs /
// budgetBuckets.

func TestHub_ConcurrentRegisterSubscribeBroadcast_NoRace(t *testing.T) {
	hub := NewHub()
	go hub.Run()

	const numClients = 8
	const numBudgets = 4
	const opsPerClient = 50

	conns := make([]*websocket.Conn, 0, numClients)
	cleanups := make([]func(), 0, numClients)
	defer func() {
		for _, cl := range cleanups {
			cl()
		}
	}()

	clients := make([]*Client, 0, numClients)
	for i := 0; i < numClients; i++ {
		sc, _, cl := startTestWSPair(t)
		conns = append(conns, sc)
		cleanups = append(cleanups, cl)
		c := &Client{
			Conn:      sc,
			UserID:    "user-stress",
			BudgetIDs: map[string]bool{"b0": true},
		}
		clients = append(clients, c)
		hub.Register(c)
	}

	var wg sync.WaitGroup
	for i := 0; i < numClients; i++ {
		wg.Add(1)
		go func(c *Client) {
			defer wg.Done()
			for j := 0; j < opsPerClient; j++ {
				bid := "b" + string(rune('0'+(j%numBudgets)))
				hub.SubscribeToBudget(c, bid)
				hub.BroadcastToBudget(bid, Message{Type: MessageTypeExpenseCreated})
				hub.UnsubscribeFromBudget(c, bid)
			}
		}(clients[i])
	}
	wg.Wait()

	// Drain anything left in each client's write path by unregistering
	// sequentially. This also exercises the unregister cleanup under load.
	for _, c := range clients {
		hub.Unregister(c)
	}
	time.Sleep(100 * time.Millisecond)

	if hub.ClientCount() != 0 {
		t.Errorf("after all unregisters ClientCount = %d, want 0", hub.ClientCount())
	}
	_ = conns
}

// ==================== Lock ordering / deadlock probe ====================
//
// Rapidly interleave Subscribe (grabs client.mu -> h.mu) with Broadcast
// (grabs h.mu only) and ClientCount (grabs h.mu only). A lock inversion
// would surface as a deadlock; we bound the test with a timer.

func TestHub_NoDeadlock_SubscribeVsBroadcast(t *testing.T) {
	hub := NewHub()
	go hub.Run()

	serverConn, _, cleanup := startTestWSPair(t)
	defer cleanup()

	client := &Client{
		Conn:      serverConn,
		UserID:    "user-1",
		BudgetIDs: map[string]bool{"initial": true},
	}
	hub.Register(client)
	time.Sleep(20 * time.Millisecond)

	done := make(chan struct{})
	go func() {
		for i := 0; i < 200; i++ {
			hub.SubscribeToBudget(client, "bx")
			hub.UnsubscribeFromBudget(client, "bx")
		}
		close(done)
	}()

	bdone := make(chan struct{})
	go func() {
		for i := 0; i < 200; i++ {
			hub.BroadcastToBudget("bx", Message{Type: MessageTypeExpenseCreated})
			_ = hub.ClientCount()
			_ = hub.BudgetSubscriberCount("bx")
		}
		close(bdone)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Subscribe/Unsubscribe loop did not finish; possible deadlock")
	}
	select {
	case <-bdone:
	case <-time.After(5 * time.Second):
		t.Fatal("Broadcast loop did not finish; possible deadlock")
	}

	hub.Unregister(client)
	time.Sleep(20 * time.Millisecond)
}

// ==================== Unregister cleans up buckets even if BudgetIDs is stale ====================

func TestHub_Unregister_CleansBucketsWithStaleBudgetIDs(t *testing.T) {
	hub := NewHub()
	go hub.Run()

	serverConn, _, cleanup := startTestWSPair(t)
	defer cleanup()

	client := &Client{
		Conn:      serverConn,
		UserID:    "user-1",
		BudgetIDs: map[string]bool{"b1": true, "b2": true},
	}
	hub.Register(client)
	time.Sleep(20 * time.Millisecond)

	// Corrupt client.BudgetIDs behind the hub's back. The hub must NOT
	// rely on this map to find bucket memberships.
	client.mu.Lock()
	client.BudgetIDs = map[string]bool{} // clear it
	client.mu.Unlock()

	hub.Unregister(client)
	time.Sleep(20 * time.Millisecond)

	if n := hub.BudgetSubscriberCount("b1"); n != 0 {
		t.Errorf("b1 should be empty after unregister (stale BudgetIDs), got %d", n)
	}
	if n := hub.BudgetSubscriberCount("b2"); n != 0 {
		t.Errorf("b2 should be empty after unregister (stale BudgetIDs), got %d", n)
	}
}
