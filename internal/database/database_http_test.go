package database

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ==================== doRequest ====================

func TestDoRequest_SetsHeaders(t *testing.T) {
	var receivedHeaders http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedHeaders = r.Header.Clone()
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, "test-api-key")
	_, _, err := client.doRequest(http.MethodGet, server.URL+"/test", nil, nil)
	if err != nil {
		t.Fatalf("doRequest failed: %v", err)
	}

	if receivedHeaders.Get("apikey") != "test-api-key" {
		t.Errorf("apikey header = %q, want %q", receivedHeaders.Get("apikey"), "test-api-key")
	}
	if receivedHeaders.Get("Authorization") != "Bearer test-api-key" {
		t.Errorf("Authorization header = %q, want %q", receivedHeaders.Get("Authorization"), "Bearer test-api-key")
	}
	if receivedHeaders.Get("Content-Type") != "application/json" {
		t.Errorf("Content-Type header = %q, want %q", receivedHeaders.Get("Content-Type"), "application/json")
	}
}

func TestDoRequest_SetsExtraHeaders(t *testing.T) {
	var receivedHeaders http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedHeaders = r.Header.Clone()
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, "key")
	extra := map[string]string{"Prefer": "return=representation", "X-Custom": "value"}
	_, _, err := client.doRequest(http.MethodPost, server.URL+"/test", []byte(`{}`), extra)
	if err != nil {
		t.Fatalf("doRequest failed: %v", err)
	}

	if receivedHeaders.Get("Prefer") != "return=representation" {
		t.Errorf("Prefer header = %q, want %q", receivedHeaders.Get("Prefer"), "return=representation")
	}
	if receivedHeaders.Get("X-Custom") != "value" {
		t.Errorf("X-Custom header = %q, want %q", receivedHeaders.Get("X-Custom"), "value")
	}
}

func TestDoRequest_SendsBody(t *testing.T) {
	var receivedBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		receivedBody = string(body)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, "key")
	payload := `{"name":"test"}`
	_, _, err := client.doRequest(http.MethodPost, server.URL+"/test", []byte(payload), nil)
	if err != nil {
		t.Fatalf("doRequest failed: %v", err)
	}

	if receivedBody != payload {
		t.Errorf("received body = %q, want %q", receivedBody, payload)
	}
}

func TestDoRequest_ReturnsResponseBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`[{"id":"123"}]`))
	}))
	defer server.Close()

	client := NewClient(server.URL, "key")
	body, statusCode, err := client.doRequest(http.MethodGet, server.URL+"/test", nil, nil)
	if err != nil {
		t.Fatalf("doRequest failed: %v", err)
	}
	if statusCode != http.StatusOK {
		t.Errorf("status = %d, want %d", statusCode, http.StatusOK)
	}
	if string(body) != `[{"id":"123"}]` {
		t.Errorf("body = %q, want %q", string(body), `[{"id":"123"}]`)
	}
}

func TestDoRequest_ReturnsStatusCode(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"error":"not found"}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, "key")
	_, statusCode, err := client.doRequest(http.MethodGet, server.URL+"/test", nil, nil)
	if err != nil {
		t.Fatalf("doRequest failed: %v", err)
	}
	if statusCode != http.StatusNotFound {
		t.Errorf("status = %d, want %d", statusCode, http.StatusNotFound)
	}
}

func TestDoRequest_NilBody(t *testing.T) {
	var receivedMethod string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedMethod = r.Method
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, "key")
	_, _, err := client.doRequest(http.MethodDelete, server.URL+"/test", nil, nil)
	if err != nil {
		t.Fatalf("doRequest failed: %v", err)
	}
	if receivedMethod != http.MethodDelete {
		t.Errorf("method = %q, want %q", receivedMethod, http.MethodDelete)
	}
}

func TestDoRequest_InvalidURL(t *testing.T) {
	client := NewClient("http://localhost:99999", "key")
	_, _, err := client.doRequest(http.MethodGet, "http://localhost:99999/test", nil, nil)
	if err == nil {
		t.Error("expected error for invalid URL/port")
	}
}

// ==================== Get ====================

func TestGet_BuildsCorrectURL(t *testing.T) {
	var receivedURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedURL = r.URL.String()
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`[]`))
	}))
	defer server.Close()

	client := NewClient(server.URL, "key")
	_, _, err := client.Get("budgets", "select=*&user_id=eq.abc-123")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}

	if !strings.Contains(receivedURL, "/rest/v1/budgets") {
		t.Errorf("URL should contain /rest/v1/budgets, got %q", receivedURL)
	}
	if !strings.Contains(receivedURL, "select=*") {
		t.Errorf("URL should contain query params, got %q", receivedURL)
	}
}

func TestGet_EmptyQuery(t *testing.T) {
	var receivedURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedURL = r.URL.String()
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`[]`))
	}))
	defer server.Close()

	client := NewClient(server.URL, "key")
	_, _, err := client.Get("budgets", "")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}

	if strings.Contains(receivedURL, "?") {
		t.Errorf("URL should not have query string with empty query, got %q", receivedURL)
	}
}

func TestGet_UsesGETMethod(t *testing.T) {
	var receivedMethod string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedMethod = r.Method
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`[]`))
	}))
	defer server.Close()

	client := NewClient(server.URL, "key")
	_, _, _ = client.Get("budgets", "")

	if receivedMethod != http.MethodGet {
		t.Errorf("method = %q, want GET", receivedMethod)
	}
}

// ==================== Post ====================

func TestPost_UsesCorrectMethodAndHeaders(t *testing.T) {
	var receivedMethod string
	var receivedPrefer string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedMethod = r.Method
		receivedPrefer = r.Header.Get("Prefer")
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`[{"id":"new-id"}]`))
	}))
	defer server.Close()

	client := NewClient(server.URL, "key")
	body, statusCode, err := client.Post("budgets", []byte(`{"name":"test"}`))
	if err != nil {
		t.Fatalf("Post failed: %v", err)
	}

	if receivedMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", receivedMethod)
	}
	if receivedPrefer != "return=representation" {
		t.Errorf("Prefer header = %q, want %q", receivedPrefer, "return=representation")
	}
	if statusCode != http.StatusCreated {
		t.Errorf("status = %d, want 201", statusCode)
	}

	var result []map[string]string
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if len(result) != 1 || result[0]["id"] != "new-id" {
		t.Errorf("unexpected response body: %s", string(body))
	}
}

func TestPost_SendsBody(t *testing.T) {
	var receivedBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		receivedBody = string(data)
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`[]`))
	}))
	defer server.Close()

	client := NewClient(server.URL, "key")
	payload := `{"name":"Budget","monthly_income":5000000}`
	_, _, err := client.Post("budgets", []byte(payload))
	if err != nil {
		t.Fatalf("Post failed: %v", err)
	}

	if receivedBody != payload {
		t.Errorf("received body = %q, want %q", receivedBody, payload)
	}
}

// ==================== Patch ====================

func TestPatch_UsesCorrectMethodAndQuery(t *testing.T) {
	var receivedMethod string
	var receivedURL string
	var receivedPrefer string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedMethod = r.Method
		receivedURL = r.URL.String()
		receivedPrefer = r.Header.Get("Prefer")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`[{"id":"updated"}]`))
	}))
	defer server.Close()

	client := NewClient(server.URL, "key")
	_, statusCode, err := client.Patch("budgets", "id=eq.123", []byte(`{"name":"updated"}`))
	if err != nil {
		t.Fatalf("Patch failed: %v", err)
	}

	if receivedMethod != http.MethodPatch {
		t.Errorf("method = %q, want PATCH", receivedMethod)
	}
	if !strings.Contains(receivedURL, "id=eq.123") {
		t.Errorf("URL should contain query, got %q", receivedURL)
	}
	if receivedPrefer != "return=representation" {
		t.Errorf("Prefer header = %q, want %q", receivedPrefer, "return=representation")
	}
	if statusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", statusCode)
	}
}

func TestPatch_EmptyQuery(t *testing.T) {
	var receivedURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedURL = r.URL.String()
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`[]`))
	}))
	defer server.Close()

	client := NewClient(server.URL, "key")
	_, _, _ = client.Patch("budgets", "", []byte(`{}`))

	if strings.Contains(receivedURL, "?") {
		t.Errorf("URL should not have query with empty query, got %q", receivedURL)
	}
}

// ==================== Delete ====================

func TestDelete_UsesCorrectMethod(t *testing.T) {
	var receivedMethod string
	var receivedURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedMethod = r.Method
		receivedURL = r.URL.String()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	client := NewClient(server.URL, "key")
	_, statusCode, err := client.Delete("budgets", "id=eq.123")
	if err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	if receivedMethod != http.MethodDelete {
		t.Errorf("method = %q, want DELETE", receivedMethod)
	}
	if !strings.Contains(receivedURL, "id=eq.123") {
		t.Errorf("URL should contain query, got %q", receivedURL)
	}
	if statusCode != http.StatusNoContent {
		t.Errorf("status = %d, want 204", statusCode)
	}
}

func TestDelete_EmptyQuery(t *testing.T) {
	var receivedURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedURL = r.URL.String()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	client := NewClient(server.URL, "key")
	_, _, _ = client.Delete("budgets", "")

	if strings.Contains(receivedURL, "?") {
		t.Errorf("URL should not have query with empty query, got %q", receivedURL)
	}
}

// ==================== RPC ====================

func TestRPC_UsesCorrectURLAndMethod(t *testing.T) {
	var receivedMethod string
	var receivedURL string
	var receivedBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedMethod = r.Method
		receivedURL = r.URL.String()
		data, _ := io.ReadAll(r.Body)
		receivedBody = string(data)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"result":"ok"}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, "key")
	body, statusCode, err := client.RPC("get_budget_summary", []byte(`{"budget_id":"abc"}`))
	if err != nil {
		t.Fatalf("RPC failed: %v", err)
	}

	if receivedMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", receivedMethod)
	}
	if !strings.Contains(receivedURL, "/rest/v1/rpc/get_budget_summary") {
		t.Errorf("URL should contain /rest/v1/rpc/get_budget_summary, got %q", receivedURL)
	}
	if receivedBody != `{"budget_id":"abc"}` {
		t.Errorf("body = %q, want %q", receivedBody, `{"budget_id":"abc"}`)
	}
	if statusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", statusCode)
	}
	if string(body) != `{"result":"ok"}` {
		t.Errorf("response body = %q, want %q", string(body), `{"result":"ok"}`)
	}
}

// ==================== Filter.Offset ====================

func TestFilterOffset_BasicUsage(t *testing.T) {
	f := NewFilter().Offset(20)
	query := f.Build()
	expected := "offset=20"
	if query != expected {
		t.Errorf("Offset: got %q, want %q", query, expected)
	}
}

func TestFilterOffset_Zero(t *testing.T) {
	f := NewFilter().Offset(0)
	query := f.Build()
	expected := "offset=0"
	if query != expected {
		t.Errorf("Offset zero: got %q, want %q", query, expected)
	}
}

func TestFilterOffset_CombinedWithLimit(t *testing.T) {
	f := NewFilter().Limit(10).Offset(20)
	query := f.Build()
	if !strings.Contains(query, "limit=10") {
		t.Errorf("should contain limit=10, got %q", query)
	}
	if !strings.Contains(query, "offset=20") {
		t.Errorf("should contain offset=20, got %q", query)
	}
}

// ==================== Close ====================

func TestClose_IsNoOp(t *testing.T) {
	// Close should not panic even without initialization.
	Close()
	// Call it again to verify idempotency.
	Close()
}

// ==================== Large Buffer Pool ====================

func TestBufPool_LargeBufferNotReturned(t *testing.T) {
	buf := getBuf()
	// Write more than 1MB to make the buffer large.
	bigData := make([]byte, 2<<20) // 2MB
	buf.Write(bigData)

	// putBuf should NOT return this to the pool because cap > 1MB.
	putBuf(buf)

	// Get a new buffer - it should be fresh (not the large one).
	buf2 := getBuf()
	if buf2.Cap() > 1<<20 {
		t.Error("large buffer should not have been returned to pool")
	}
	putBuf(buf2)
}
