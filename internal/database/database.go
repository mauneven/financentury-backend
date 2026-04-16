// Package database provides a PostgreSQL client used by handlers to interact
// with the database. Queries are executed directly via pgx — no middleware
// (PostgREST, ORM, etc.) is required, making the database provider-agnostic.
package database

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Client wraps a pgx connection pool with convenience methods that accept
// the same query format used by the rest of the codebase (the Filter builder).
type Client struct {
	Pool *pgxpool.Pool
}

// DB is the global database client instance.
var DB *Client

// Init initializes the global database client from a PostgreSQL connection URL.
func Init(databaseURL string) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		panic(fmt.Sprintf("invalid DATABASE_URL: %v", err))
	}
	cfg.MinConns = 5
	cfg.MaxConns = 20
	cfg.MaxConnLifetime = 30 * time.Minute
	cfg.MaxConnIdleTime = 5 * time.Minute
	cfg.HealthCheckPeriod = 30 * time.Second
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		panic(fmt.Sprintf("failed to connect to database: %v", err))
	}
	DB = &Client{Pool: pool}
}

// Close releases the connection pool.
func Close() {
	if DB != nil && DB.Pool != nil {
		DB.Pool.Close()
	}
}

// ---------------------------------------------------------------------------
// Query parser — translates the Filter-built query string into SQL clauses
// ---------------------------------------------------------------------------

type parsedQuery struct {
	selectCols string
	conditions []condition
	orderCol   string
	orderDir   string
	limit      string
	offset     string
}

type condition struct {
	column   string
	operator string // eq, neq, gt, gte, lt, lte, in
	value    string // raw value (for "in": "(a,b,c)")
}

func parseQuery(query string) parsedQuery {
	p := parsedQuery{selectCols: "*"}
	if query == "" {
		return p
	}
	for _, part := range strings.Split(query, "&") {
		kv := strings.SplitN(part, "=", 2)
		if len(kv) != 2 {
			continue
		}
		key, _ := url.QueryUnescape(kv[0])
		val, _ := url.QueryUnescape(kv[1])

		switch key {
		case "select":
			p.selectCols = val
		case "order":
			if dot := strings.LastIndex(val, "."); dot > 0 {
				p.orderCol = val[:dot]
				p.orderDir = strings.ToUpper(val[dot+1:])
			}
		case "limit":
			p.limit = val
		case "offset":
			p.offset = val
		default:
			if dot := strings.Index(val, "."); dot > 0 {
				p.conditions = append(p.conditions, condition{
					column:   key,
					operator: val[:dot],
					value:    val[dot+1:],
				})
			}
		}
	}
	return p
}

// quoteIdent quotes a SQL identifier to prevent injection.
func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// buildWhere generates a WHERE clause with parameterized placeholders.
// argStart is the starting $N index.
func (p parsedQuery) buildWhere(argStart int) (string, []interface{}) {
	if len(p.conditions) == 0 {
		return "", nil
	}
	var clauses []string
	var args []interface{}
	idx := argStart

	for _, c := range p.conditions {
		col := quoteIdent(c.column)
		switch c.operator {
		case "eq":
			clauses = append(clauses, fmt.Sprintf("%s::text = $%d", col, idx))
			args = append(args, c.value)
			idx++
		case "neq":
			clauses = append(clauses, fmt.Sprintf("%s::text != $%d", col, idx))
			args = append(args, c.value)
			idx++
		case "gt":
			clauses = append(clauses, fmt.Sprintf("%s > $%d", col, idx))
			args = append(args, c.value)
			idx++
		case "gte":
			clauses = append(clauses, fmt.Sprintf("%s >= $%d", col, idx))
			args = append(args, c.value)
			idx++
		case "lt":
			clauses = append(clauses, fmt.Sprintf("%s < $%d", col, idx))
			args = append(args, c.value)
			idx++
		case "lte":
			clauses = append(clauses, fmt.Sprintf("%s <= $%d", col, idx))
			args = append(args, c.value)
			idx++
		case "in":
			inner := strings.Trim(c.value, "()")
			vals := strings.Split(inner, ",")
			ph := make([]string, len(vals))
			for i, v := range vals {
				ph[i] = fmt.Sprintf("$%d", idx)
				args = append(args, v)
				idx++
			}
			clauses = append(clauses, fmt.Sprintf("%s::text IN (%s)", col, strings.Join(ph, ",")))
		}
	}
	return "WHERE " + strings.Join(clauses, " AND "), args
}

func (p parsedQuery) buildOrder() string {
	if p.orderCol == "" {
		return ""
	}
	dir := "ASC"
	if p.orderDir == "DESC" {
		dir = "DESC"
	}
	return fmt.Sprintf("ORDER BY %s %s", quoteIdent(p.orderCol), dir)
}

func (p parsedQuery) buildLimitOffset(argStart int) (string, []interface{}) {
	var parts []string
	var args []interface{}
	idx := argStart
	if p.limit != "" {
		parts = append(parts, fmt.Sprintf("LIMIT $%d", idx))
		args = append(args, p.limit)
		idx++
	}
	if p.offset != "" {
		parts = append(parts, fmt.Sprintf("OFFSET $%d", idx))
		args = append(args, p.offset)
	}
	return strings.Join(parts, " "), args
}

func selectColumns(raw string) string {
	if raw == "" || raw == "*" {
		return "*"
	}
	cols := strings.Split(raw, ",")
	quoted := make([]string, len(cols))
	for i, c := range cols {
		quoted[i] = quoteIdent(strings.TrimSpace(c))
	}
	return strings.Join(quoted, ", ")
}

// ---------------------------------------------------------------------------
// CRUD methods — same public API as the previous PostgREST client
// ---------------------------------------------------------------------------

// Get performs a SELECT and returns results as a JSON array.
func (c *Client) Get(table, query string) ([]byte, int, error) {
	p := parseQuery(query)
	where, args := p.buildWhere(1)
	limitOffset, loArgs := p.buildLimitOffset(len(args) + 1)
	args = append(args, loArgs...)

	inner := fmt.Sprintf("SELECT %s FROM %s %s %s %s",
		selectColumns(p.selectCols), quoteIdent(table), where, p.buildOrder(), limitOffset)

	sql := fmt.Sprintf("SELECT COALESCE(json_agg(to_json(t)), '[]'::json) FROM (%s) t", inner)

	var result json.RawMessage
	if err := c.Pool.QueryRow(context.Background(), sql, args...).Scan(&result); err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("query %s failed: %w", table, err)
	}
	return result, http.StatusOK, nil
}

// Post performs an INSERT and returns the created row(s) as a JSON array.
func (c *Client) Post(table string, body []byte) ([]byte, int, error) {
	var data map[string]interface{}
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, http.StatusBadRequest, fmt.Errorf("invalid JSON body: %w", err)
	}

	cols := make([]string, 0, len(data))
	phs := make([]string, 0, len(data))
	args := make([]interface{}, 0, len(data))
	i := 1
	for k, v := range data {
		cols = append(cols, quoteIdent(k))
		phs = append(phs, fmt.Sprintf("$%d", i))
		args = append(args, v)
		i++
	}

	sql := fmt.Sprintf(
		"WITH ins AS (INSERT INTO %s (%s) VALUES (%s) RETURNING *) SELECT json_agg(to_json(ins)) FROM ins",
		quoteIdent(table), strings.Join(cols, ", "), strings.Join(phs, ", "))

	var result json.RawMessage
	if err := c.Pool.QueryRow(context.Background(), sql, args...).Scan(&result); err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("insert into %s failed: %w", table, err)
	}
	return result, http.StatusCreated, nil
}

// Patch performs an UPDATE on rows matching the query and returns them as JSON.
func (c *Client) Patch(table, query string, body []byte) ([]byte, int, error) {
	var data map[string]interface{}
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, http.StatusBadRequest, fmt.Errorf("invalid JSON body: %w", err)
	}

	sets := make([]string, 0, len(data))
	args := make([]interface{}, 0, len(data))
	i := 1
	for k, v := range data {
		sets = append(sets, fmt.Sprintf("%s = $%d", quoteIdent(k), i))
		args = append(args, v)
		i++
	}

	p := parseQuery(query)
	where, wArgs := p.buildWhere(i)
	args = append(args, wArgs...)

	sql := fmt.Sprintf(
		"WITH upd AS (UPDATE %s SET %s %s RETURNING *) SELECT json_agg(to_json(upd)) FROM upd",
		quoteIdent(table), strings.Join(sets, ", "), where)

	var result json.RawMessage
	if err := c.Pool.QueryRow(context.Background(), sql, args...).Scan(&result); err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("update %s failed: %w", table, err)
	}
	return result, http.StatusOK, nil
}

// Delete removes rows matching the query.
func (c *Client) Delete(table, query string) ([]byte, int, error) {
	p := parseQuery(query)
	where, args := p.buildWhere(1)

	sql := fmt.Sprintf("DELETE FROM %s %s", quoteIdent(table), where)
	if _, err := c.Pool.Exec(context.Background(), sql, args...); err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("delete from %s failed: %w", table, err)
	}
	return nil, http.StatusNoContent, nil
}

// RPC calls a PostgreSQL function and returns the result as JSON.
func (c *Client) RPC(functionName string, body []byte) ([]byte, int, error) {
	sql := fmt.Sprintf("SELECT to_json(%s($1::json))", quoteIdent(functionName))
	var result json.RawMessage
	if err := c.Pool.QueryRow(context.Background(), sql, string(body)).Scan(&result); err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("rpc %s failed: %w", functionName, err)
	}
	return result, http.StatusOK, nil
}

// ---------------------------------------------------------------------------
// Filter builder — unchanged API, same query string format
// ---------------------------------------------------------------------------

// Filter helps build query strings in a type-safe, chainable way.
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

// Eq adds an equality filter.
func (f *Filter) Eq(column, value string) *Filter {
	f.params = append(f.params, column+"=eq."+url.QueryEscape(value))
	return f
}

// Neq adds a not-equal filter.
func (f *Filter) Neq(column, value string) *Filter {
	f.params = append(f.params, column+"=neq."+url.QueryEscape(value))
	return f
}

// Gt adds a greater-than filter.
func (f *Filter) Gt(column, value string) *Filter {
	f.params = append(f.params, column+"=gt."+url.QueryEscape(value))
	return f
}

// Gte adds a greater-than-or-equal filter.
func (f *Filter) Gte(column, value string) *Filter {
	f.params = append(f.params, column+"=gte."+url.QueryEscape(value))
	return f
}

// Lt adds a less-than filter.
func (f *Filter) Lt(column, value string) *Filter {
	f.params = append(f.params, column+"=lt."+url.QueryEscape(value))
	return f
}

// Lte adds a less-than-or-equal filter.
func (f *Filter) Lte(column, value string) *Filter {
	f.params = append(f.params, column+"=lte."+url.QueryEscape(value))
	return f
}

// In adds an IN filter.
func (f *Filter) In(column string, values []string) *Filter {
	escaped := make([]string, len(values))
	for i, v := range values {
		escaped[i] = url.QueryEscape(v)
	}
	f.params = append(f.params, column+"=in.("+strings.Join(escaped, ",")+")")
	return f
}

// Order sets the ordering.
func (f *Filter) Order(column, direction string) *Filter {
	f.params = append(f.params, "order="+column+"."+direction)
	return f
}

// Limit limits the number of returned rows.
func (f *Filter) Limit(n int) *Filter {
	f.params = append(f.params, fmt.Sprintf("limit=%d", n))
	return f
}

// Offset skips the first n rows.
func (f *Filter) Offset(n int) *Filter {
	f.params = append(f.params, fmt.Sprintf("offset=%d", n))
	return f
}

// Build returns the assembled query string.
func (f *Filter) Build() string {
	return strings.Join(f.params, "&")
}
