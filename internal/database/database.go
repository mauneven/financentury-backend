// Package database provides the Supabase REST API client used by handlers
// to interact with the PostgreSQL database via PostgREST.
package database

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// maxResponseSize limits response bodies to 10 MB.
const maxResponseSize = 10 << 20

// Client is the Supabase REST API client that replaces a direct database
// connection pool. It uses connection pooling via http.Transport for efficient
// HTTP/1.1 and HTTP/2 connection reuse.
type Client struct {
	BaseURL    string
	APIKey     string
	HTTPClient *http.Client
}

// bufPool is a sync.Pool of *bytes.Buffer used to reduce allocations when
// reading response bodies in hot paths.
var bufPool = sync.Pool{
	New: func() interface{} {
		return bytes.NewBuffer(make([]byte, 0, 4096))
	},
}

// getBuf retrieves a buffer from the pool and resets it.
func getBuf() *bytes.Buffer {
	buf := bufPool.Get().(*bytes.Buffer)
	buf.Reset()
	return buf
}

// putBuf returns a buffer to the pool.
func putBuf(buf *bytes.Buffer) {
	// Prevent excessively large buffers from being returned to the pool.
	if buf.Cap() <= 1<<20 {
		bufPool.Put(buf)
	}
}

// DB is the global Supabase client instance.
var DB *Client

// NewClient creates a new Supabase REST API client with an optimised
// http.Transport that enables connection pooling and keep-alive.
func NewClient(baseURL, apiKey string) *Client {
	transport := &http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 20,
		IdleConnTimeout:     90 * time.Second,
		DialContext: (&net.Dialer{
			Timeout:   5 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: 10 * time.Second,
		ForceAttemptHTTP2:     true,
	}

	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		APIKey:  apiKey,
		HTTPClient: &http.Client{
			Timeout:   15 * time.Second,
			Transport: transport,
		},
	}
}

// Init initializes the global Supabase client.
func Init(baseURL, apiKey string) {
	DB = NewClient(baseURL, apiKey)
}

// Close is a no-op for the HTTP client (kept for API compatibility).
func Close() {}

// doRequest executes an HTTP request with the Supabase auth headers. It uses
// a pooled buffer to read the response body efficiently.
func (c *Client) doRequest(method, url string, body []byte, extraHeaders map[string]string) ([]byte, int, error) {
	var bodyReader io.Reader
	if body != nil {
		bodyReader = bytes.NewReader(body)
	}

	req, err := http.NewRequest(method, url, bodyReader)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("apikey", c.APIKey)
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	req.Header.Set("Content-Type", "application/json")

	for k, v := range extraHeaders {
		req.Header.Set(k, v)
	}

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	// Read into pooled buffer.
	buf := getBuf()
	defer putBuf(buf)

	_, err = io.Copy(buf, io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("failed to read response body: %w", err)
	}

	// Copy the buffer contents to a new slice so the buffer can be returned.
	result := make([]byte, buf.Len())
	copy(result, buf.Bytes())

	return result, resp.StatusCode, nil
}

// Get performs a GET request on the given table with query parameters.
// query should be a pre-built query string (e.g., "select=*&user_id=eq.abc").
func (c *Client) Get(table, query string) ([]byte, int, error) {
	u := fmt.Sprintf("%s/rest/v1/%s", c.BaseURL, table)
	if query != "" {
		u += "?" + query
	}
	return c.doRequest(http.MethodGet, u, nil, nil)
}

// Post performs a POST request to insert row(s) into a table.
// Returns the created row(s) via Prefer: return=representation.
func (c *Client) Post(table string, body []byte) ([]byte, int, error) {
	u := fmt.Sprintf("%s/rest/v1/%s", c.BaseURL, table)
	headers := map[string]string{
		"Prefer": "return=representation",
	}
	return c.doRequest(http.MethodPost, u, body, headers)
}

// Patch performs a PATCH request to update row(s) matching the query filter.
// Returns the updated row(s) via Prefer: return=representation.
func (c *Client) Patch(table, query string, body []byte) ([]byte, int, error) {
	u := fmt.Sprintf("%s/rest/v1/%s", c.BaseURL, table)
	if query != "" {
		u += "?" + query
	}
	headers := map[string]string{
		"Prefer": "return=representation",
	}
	return c.doRequest(http.MethodPatch, u, body, headers)
}

// Delete performs a DELETE request on rows matching the query filter.
func (c *Client) Delete(table, query string) ([]byte, int, error) {
	u := fmt.Sprintf("%s/rest/v1/%s", c.BaseURL, table)
	if query != "" {
		u += "?" + query
	}
	return c.doRequest(http.MethodDelete, u, nil, nil)
}

// RPC calls a Supabase RPC function with a JSON body.
func (c *Client) RPC(functionName string, body []byte) ([]byte, int, error) {
	u := fmt.Sprintf("%s/rest/v1/rpc/%s", c.BaseURL, functionName)
	return c.doRequest(http.MethodPost, u, body, nil)
}

// --- Filter Builder Helpers ---

// Filter helps build PostgREST query strings in a type-safe, chainable way.
type Filter struct {
	params []string
}

// NewFilter creates a new Filter builder.
func NewFilter() *Filter {
	return &Filter{}
}

// Select sets the columns to select.
func (f *Filter) Select(columns string) *Filter {
	f.params = append(f.params, "select="+url.QueryEscape(columns))
	return f
}

// Eq adds an equality filter: column=eq.value
func (f *Filter) Eq(column, value string) *Filter {
	f.params = append(f.params, column+"=eq."+url.QueryEscape(value))
	return f
}

// Neq adds a not-equal filter: column=neq.value
func (f *Filter) Neq(column, value string) *Filter {
	f.params = append(f.params, column+"=neq."+url.QueryEscape(value))
	return f
}

// Gt adds a greater-than filter: column=gt.value
func (f *Filter) Gt(column, value string) *Filter {
	f.params = append(f.params, column+"=gt."+url.QueryEscape(value))
	return f
}

// Gte adds a greater-than-or-equal filter: column=gte.value
func (f *Filter) Gte(column, value string) *Filter {
	f.params = append(f.params, column+"=gte."+url.QueryEscape(value))
	return f
}

// Lt adds a less-than filter: column=lt.value
func (f *Filter) Lt(column, value string) *Filter {
	f.params = append(f.params, column+"=lt."+url.QueryEscape(value))
	return f
}

// Lte adds a less-than-or-equal filter: column=lte.value
func (f *Filter) Lte(column, value string) *Filter {
	f.params = append(f.params, column+"=lte."+url.QueryEscape(value))
	return f
}

// In adds an IN filter: column=in.(val1,val2,...)
func (f *Filter) In(column string, values []string) *Filter {
	escaped := make([]string, len(values))
	for i, v := range values {
		escaped[i] = url.QueryEscape(v)
	}
	f.params = append(f.params, column+"=in.("+strings.Join(escaped, ",")+")")
	return f
}

// Order sets the ordering: column.asc or column.desc
func (f *Filter) Order(column, direction string) *Filter {
	f.params = append(f.params, "order="+column+"."+direction)
	return f
}

// Limit limits the number of returned rows.
func (f *Filter) Limit(n int) *Filter {
	f.params = append(f.params, fmt.Sprintf("limit=%d", n))
	return f
}

// Build returns the assembled query string.
func (f *Filter) Build() string {
	return strings.Join(f.params, "&")
}
