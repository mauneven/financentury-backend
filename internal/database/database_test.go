package database

import (
	"net/url"
	"strings"
	"testing"
)

// ==================== Filter.Eq — URL Escaping ====================

func TestFilterEq_URLEscapesValue(t *testing.T) {
	f := NewFilter().Eq("email", "user@example.com")
	query := f.Build()

	if !strings.Contains(query, "email=eq."+url.QueryEscape("user@example.com")) {
		t.Errorf("Eq should URL-escape values, got %q", query)
	}
}

func TestFilterEq_SQLInjectionEscaped(t *testing.T) {
	sqli := "value'; DROP TABLE budgets; --"
	f := NewFilter().Eq("name", sqli)
	query := f.Build()

	if strings.Contains(query, "'") {
		t.Errorf("apostrophe should be URL-escaped in query: %q", query)
	}
	if strings.Contains(query, ";") {
		t.Errorf("semicolons should be URL-escaped in query: %q", query)
	}
	expected := "name=eq." + url.QueryEscape(sqli)
	if !strings.Contains(query, expected) {
		t.Errorf("query should contain escaped SQLi value\ngot:  %q\nwant: %q", query, expected)
	}
}

func TestFilterEq_SpecialCharacters(t *testing.T) {
	special := []struct {
		name  string
		value string
	}{
		{"ampersand", "a&b"},
		{"equals sign", "a=b"},
		{"percent", "100%"},
		{"plus sign", "a+b"},
		{"slash", "a/b"},
		{"backslash", "a\\b"},
		{"angle brackets", "<script>alert(1)</script>"},
		{"unicode emoji", "\U0001F600"},
		{"null byte", "a\x00b"},
		{"newline", "a\nb"},
		{"tab", "a\tb"},
	}

	for _, tc := range special {
		t.Run(tc.name, func(t *testing.T) {
			f := NewFilter().Eq("col", tc.value)
			query := f.Build()
			expected := "col=eq." + url.QueryEscape(tc.value)
			if query != expected {
				t.Errorf("Eq special char %s:\ngot:  %q\nwant: %q", tc.name, query, expected)
			}
		})
	}
}

// ==================== Filter.Neq — URL Escaping ====================

func TestFilterNeq_URLEscapesValue(t *testing.T) {
	f := NewFilter().Neq("status", "deleted; DROP TABLE")
	query := f.Build()
	if strings.Contains(query, ";") {
		t.Errorf("Neq should URL-escape semicolons: %q", query)
	}
}

// ==================== Filter.In — URL Escaping ====================

func TestFilterIn_URLEscapesValues(t *testing.T) {
	values := []string{"a&b", "c=d", "e'f"}
	f := NewFilter().In("col", values)
	query := f.Build()

	if strings.Contains(query, "&b") && !strings.Contains(query, url.QueryEscape("a&b")) {
		t.Errorf("In should URL-escape values: %q", query)
	}
	if strings.Contains(query, "'") {
		t.Errorf("apostrophes should be URL-escaped in In: %q", query)
	}
}

func TestFilterIn_SQLInjectionInValues(t *testing.T) {
	values := []string{"safe", "'; DROP TABLE users; --", "also_safe"}
	f := NewFilter().In("id", values)
	query := f.Build()

	if strings.Contains(query, "'") {
		t.Errorf("SQL injection in In values should be escaped: %q", query)
	}
}

func TestFilterIn_EmptyValues(t *testing.T) {
	f := NewFilter().In("col", []string{})
	query := f.Build()
	expected := "col=in.()"
	if query != expected {
		t.Errorf("In with empty values: got %q, want %q", query, expected)
	}
}

func TestFilterIn_SingleValue(t *testing.T) {
	f := NewFilter().In("col", []string{"only"})
	query := f.Build()
	expected := "col=in.(only)"
	if query != expected {
		t.Errorf("In with single value: got %q, want %q", query, expected)
	}
}

// ==================== Filter.Select — URL Escaping ====================

func TestFilterSelect_URLEscapesColumns(t *testing.T) {
	f := NewFilter().Select("id,email,full_name")
	query := f.Build()
	expected := "select=" + url.QueryEscape("id,email,full_name")
	if query != expected {
		t.Errorf("Select: got %q, want %q", query, expected)
	}
}

func TestFilterSelect_SpecialCharsEscaped(t *testing.T) {
	f := NewFilter().Select("id; DROP TABLE budgets;--")
	query := f.Build()
	if strings.Contains(query, ";") {
		t.Errorf("Select should URL-escape semicolons: %q", query)
	}
}

// ==================== Filter.Order ====================

func TestFilterOrder_BasicUsage(t *testing.T) {
	f := NewFilter().Order("created_at", "desc")
	query := f.Build()
	expected := "order=created_at.desc"
	if query != expected {
		t.Errorf("Order: got %q, want %q", query, expected)
	}
}

func TestFilterOrder_AscDirection(t *testing.T) {
	f := NewFilter().Order("name", "asc")
	query := f.Build()
	expected := "order=name.asc"
	if query != expected {
		t.Errorf("Order asc: got %q, want %q", query, expected)
	}
}

// ==================== Filter.Limit ====================

func TestFilterLimit_BasicUsage(t *testing.T) {
	f := NewFilter().Limit(10)
	query := f.Build()
	expected := "limit=10"
	if query != expected {
		t.Errorf("Limit: got %q, want %q", query, expected)
	}
}

func TestFilterLimit_Zero(t *testing.T) {
	f := NewFilter().Limit(0)
	query := f.Build()
	expected := "limit=0"
	if query != expected {
		t.Errorf("Limit zero: got %q, want %q", query, expected)
	}
}

func TestFilterLimit_Negative(t *testing.T) {
	f := NewFilter().Limit(-1)
	query := f.Build()
	expected := "limit=-1"
	if query != expected {
		t.Errorf("Limit negative: got %q, want %q", query, expected)
	}
}

// ==================== Filter.Gt/Gte/Lt/Lte ====================

func TestFilterGt_URLEscapesValue(t *testing.T) {
	f := NewFilter().Gt("amount", "100.50")
	query := f.Build()
	expected := "amount=gt." + url.QueryEscape("100.50")
	if query != expected {
		t.Errorf("Gt: got %q, want %q", query, expected)
	}
}

func TestFilterGte_URLEscapesValue(t *testing.T) {
	f := NewFilter().Gte("date", "2026-01-01")
	query := f.Build()
	expected := "date=gte." + url.QueryEscape("2026-01-01")
	if query != expected {
		t.Errorf("Gte: got %q, want %q", query, expected)
	}
}

func TestFilterLt_URLEscapesValue(t *testing.T) {
	f := NewFilter().Lt("count", "50")
	query := f.Build()
	expected := "count=lt.50"
	if query != expected {
		t.Errorf("Lt: got %q, want %q", query, expected)
	}
}

func TestFilterLte_URLEscapesValue(t *testing.T) {
	f := NewFilter().Lte("score", "99.99")
	query := f.Build()
	expected := "score=lte." + url.QueryEscape("99.99")
	if query != expected {
		t.Errorf("Lte: got %q, want %q", query, expected)
	}
}

// ==================== Filter.Build — Chaining ====================

func TestFilterBuild_ChainsMultipleFilters(t *testing.T) {
	f := NewFilter().
		Select("id,name").
		Eq("user_id", "abc-123").
		Order("created_at", "desc").
		Limit(10)

	query := f.Build()

	parts := strings.Split(query, "&")
	if len(parts) != 4 {
		t.Errorf("expected 4 parts, got %d: %q", len(parts), query)
	}

	if !strings.HasPrefix(parts[0], "select=") {
		t.Errorf("first part should be select=, got %q", parts[0])
	}
	if !strings.Contains(parts[1], "user_id=eq.") {
		t.Errorf("second part should contain user_id=eq., got %q", parts[1])
	}
	if parts[2] != "order=created_at.desc" {
		t.Errorf("third part should be order, got %q", parts[2])
	}
	if parts[3] != "limit=10" {
		t.Errorf("fourth part should be limit, got %q", parts[3])
	}
}

func TestFilterBuild_EmptyFilter(t *testing.T) {
	f := NewFilter()
	query := f.Build()
	if query != "" {
		t.Errorf("empty filter should build to empty string, got %q", query)
	}
}

// ==================== Filter — Injection Through Column Names ====================

func TestFilterEq_ColumnNameNotEscaped(t *testing.T) {
	f := NewFilter().Eq("col; DROP TABLE", "value")
	query := f.Build()

	if !strings.Contains(query, "col; DROP TABLE=eq.value") {
		t.Errorf("column name should appear as-is: %q", query)
	}
}

// ==================== parseQuery ====================

func TestParseQuery_Empty(t *testing.T) {
	p := parseQuery("")
	if p.selectCols != "*" {
		t.Errorf("empty query selectCols = %q, want *", p.selectCols)
	}
	if len(p.conditions) != 0 {
		t.Errorf("empty query should have 0 conditions, got %d", len(p.conditions))
	}
}

func TestParseQuery_SelectAndEq(t *testing.T) {
	q := NewFilter().Select("id,name").Eq("user_id", "abc").Build()
	p := parseQuery(q)
	if p.selectCols != "id,name" {
		t.Errorf("selectCols = %q, want id,name", p.selectCols)
	}
	if len(p.conditions) != 1 {
		t.Fatalf("expected 1 condition, got %d", len(p.conditions))
	}
	if p.conditions[0].column != "user_id" || p.conditions[0].operator != "eq" || p.conditions[0].value != "abc" {
		t.Errorf("condition = %+v", p.conditions[0])
	}
}

func TestParseQuery_OrderLimitOffset(t *testing.T) {
	q := NewFilter().Order("created_at", "desc").Limit(10).Offset(5).Build()
	p := parseQuery(q)
	if p.orderCol != "created_at" || p.orderDir != "DESC" {
		t.Errorf("order = %s.%s, want created_at.DESC", p.orderCol, p.orderDir)
	}
	if p.limit != "10" {
		t.Errorf("limit = %q, want 10", p.limit)
	}
	if p.offset != "5" {
		t.Errorf("offset = %q, want 5", p.offset)
	}
}

func TestQuoteIdent(t *testing.T) {
	if q := quoteIdent("name"); q != `"name"` {
		t.Errorf("quoteIdent(name) = %q", q)
	}
	if q := quoteIdent(`a"b`); q != `"a""b"` {
		t.Errorf("quoteIdent(a\"b) = %q", q)
	}
}

// ==================== Close ====================

func TestClose_DoesNotPanic(t *testing.T) {
	Close()
}
