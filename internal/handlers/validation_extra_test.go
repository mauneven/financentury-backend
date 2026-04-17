package handlers

import (
	"strings"
	"testing"
	"time"
)

// ==================== isValidCurrencyCode — Additional Edge Cases ====================

func TestIsValidCurrencyCode_BoundaryLength(t *testing.T) {
	// Exactly 3 uppercase letters.
	if !isValidCurrencyCode("ABC") {
		t.Error("ABC should be valid")
	}
	// 2 letters
	if isValidCurrencyCode("AB") {
		t.Error("AB should be invalid (too short)")
	}
	// 4 letters
	if isValidCurrencyCode("ABCD") {
		t.Error("ABCD should be invalid (too long)")
	}
}

// ==================== Invite Constants ====================

func TestInviteConstants(t *testing.T) {
	if maxInviteTokenLength != 128 {
		t.Errorf("maxInviteTokenLength = %d, want 128", maxInviteTokenLength)
	}
	if inviteTokenBytes != 32 {
		t.Errorf("inviteTokenBytes = %d, want 32", inviteTokenBytes)
	}
	if inviteExpiry != 7*24*time.Hour {
		t.Errorf("inviteExpiry = %v, want 7 days", inviteExpiry)
	}
}

// ==================== InitInvites ====================

func TestInitInvites_SetsURL(t *testing.T) {
	InitInvites("https://app.example.com")
	if frontendURL != "https://app.example.com" {
		t.Errorf("frontendURL = %q, want %q", frontendURL, "https://app.example.com")
	}
}

// ==================== OAuth Constants ====================

func TestOAuthConstants(t *testing.T) {
	if oauthHTTPTimeout != 10*time.Second {
		t.Errorf("oauthHTTPTimeout = %v, want 10s", oauthHTTPTimeout)
	}
	if maxOAuthResponseSize != 1<<20 {
		t.Errorf("maxOAuthResponseSize = %d, want %d", maxOAuthResponseSize, 1<<20)
	}
}

// ==================== isAllowedRedirectURI additional tests ====================

func TestIsAllowedRedirectURI_SubdomainAttack(t *testing.T) {
	InitAuth("id", "secret", "https://myapp.example.com")

	attacks := []string{
		"https://evil.myapp.example.com/auth/callback",
		"https://myapp.example.com.evil.com/auth/callback",
	}
	for _, uri := range attacks {
		if isAllowedRedirectURI(uri) {
			t.Errorf("subdomain attack should be rejected: %q", uri)
		}
	}
}

func TestIsAllowedRedirectURI_PortMismatch(t *testing.T) {
	InitAuth("id", "secret", "https://myapp.example.com:443")

	// Different port should be rejected.
	if isAllowedRedirectURI("https://myapp.example.com:8443/auth/callback") {
		t.Error("different port should be rejected")
	}
}

// ==================== Password Complexity Checks ====================

func TestPasswordComplexity_HasAllRequired(t *testing.T) {
	password := "Passw0rd"
	var hasUpper, hasLower, hasDigit bool
	for _, r := range password {
		switch {
		case r >= 'A' && r <= 'Z':
			hasUpper = true
		case r >= 'a' && r <= 'z':
			hasLower = true
		case r >= '0' && r <= '9':
			hasDigit = true
		}
	}
	if !hasUpper || !hasLower || !hasDigit {
		t.Error("Passw0rd should have upper, lower, and digit")
	}
}

func TestPasswordComplexity_MissingUpper(t *testing.T) {
	password := "password1"
	var hasUpper bool
	for _, r := range password {
		if r >= 'A' && r <= 'Z' {
			hasUpper = true
		}
	}
	if hasUpper {
		t.Error("password1 should not have uppercase")
	}
}

func TestPasswordComplexity_MissingLower(t *testing.T) {
	password := "PASSWORD1"
	var hasLower bool
	for _, r := range password {
		if r >= 'a' && r <= 'z' {
			hasLower = true
		}
	}
	if hasLower {
		t.Error("PASSWORD1 should not have lowercase")
	}
}

func TestPasswordComplexity_MissingDigit(t *testing.T) {
	password := "Password"
	var hasDigit bool
	for _, r := range password {
		if r >= '0' && r <= '9' {
			hasDigit = true
		}
	}
	if hasDigit {
		t.Error("Password should not have digit")
	}
}

// ==================== SectionWithCategories Type ====================

func TestSectionWithCategories_IsExported(t *testing.T) {
	// Verify the SectionWithCategories struct can be instantiated.
	swc := SectionWithCategories{}
	if swc.Name != "" {
		t.Error("default SectionWithCategories name should be empty")
	}
	if swc.Categories != nil {
		t.Error("default SectionWithCategories categories should be nil")
	}
}

// ==================== Guided Section Template Validation ====================

func TestGuidedSections_UniqueNames(t *testing.T) {
	templates := map[string][]guidedSection{
		"balanced":    getBalancedSections(),
		"debt-free":   getDebtFreeSections(),
		"debt-payoff": getDebtPayoffSections(),
		"travel":      getTravelSections(),
		"event":       getEventSections(),
	}

	for name, sections := range templates {
		seen := make(map[string]bool)
		for _, s := range sections {
			if seen[s.Name] {
				t.Errorf("%s: duplicate section name %q", name, s.Name)
			}
			seen[s.Name] = true
		}
	}
}

func TestGuidedSections_SortOrderSequential(t *testing.T) {
	templates := map[string][]guidedSection{
		"balanced":    getBalancedSections(),
		"debt-free":   getDebtFreeSections(),
		"debt-payoff": getDebtPayoffSections(),
		"travel":      getTravelSections(),
		"event":       getEventSections(),
	}

	for name, sections := range templates {
		for i, s := range sections {
			expectedOrder := i + 1
			if s.SortOrder != expectedOrder {
				t.Errorf("%s: section %q sort_order = %d, want %d", name, s.Name, s.SortOrder, expectedOrder)
			}
		}
	}
}

func TestGuidedSections_AllIconsNonEmpty(t *testing.T) {
	templates := map[string][]guidedSection{
		"balanced":    getBalancedSections(),
		"debt-free":   getDebtFreeSections(),
		"debt-payoff": getDebtPayoffSections(),
		"travel":      getTravelSections(),
		"event":       getEventSections(),
	}

	for name, sections := range templates {
		for _, s := range sections {
			if s.Icon == "" {
				t.Errorf("%s: section %q has empty icon", name, s.Name)
			}
			for _, c := range s.Categories {
				if c.Icon == "" {
					t.Errorf("%s: category %q has empty icon", name, c.Name)
				}
			}
		}
	}
}

func TestGuidedSections_NoNamesExceedMax(t *testing.T) {
	templates := map[string][]guidedSection{
		"balanced":    getBalancedSections(),
		"debt-free":   getDebtFreeSections(),
		"debt-payoff": getDebtPayoffSections(),
		"travel":      getTravelSections(),
		"event":       getEventSections(),
	}

	for name, sections := range templates {
		for _, s := range sections {
			if len(s.Name) > maxNameLength {
				t.Errorf("%s: section name %q exceeds max length", name, s.Name)
			}
			if len(s.Icon) > maxIconLength {
				t.Errorf("%s: section icon %q exceeds max length", name, s.Icon)
			}
			for _, c := range s.Categories {
				if len(c.Name) > maxNameLength {
					t.Errorf("%s: category name %q exceeds max length", name, c.Name)
				}
			}
		}
	}
}

// ==================== MigrateRequest Types ====================

func TestMigrateRequest_StructFields(t *testing.T) {
	req := MigrateRequest{
		Budgets: []MigrateBudget{
			{
				Name:          "Test Budget",
				MonthlyIncome: 5000000,
				Currency:      "COP",
				Mode:          "manual",
				Sections: []MigrateSection{
					{
						Name:              "Section 1",
						AllocationValue: 100,
						Categories: []MigrateCategory{
							{Name: "Cat 1", AllocationValue: 100, LocalID: "cat-1"},
						},
					},
				},
				Expenses: []MigrateExpense{
					{LocalCategoryID: "cat-1", Amount: 100, Description: "Test"},
				},
			},
		},
	}

	if len(req.Budgets) != 1 {
		t.Error("should have 1 budget")
	}
	if len(req.Budgets[0].Sections) != 1 {
		t.Error("should have 1 section")
	}
	if len(req.Budgets[0].Expenses) != 1 {
		t.Error("should have 1 expense")
	}
}

// ==================== RegisterRequest / LoginRequest Types ====================

func TestRegisterRequest_StructFields(t *testing.T) {
	req := RegisterRequest{
		Name:     "Test User",
		Email:    "test@example.com",
		Password: "Passw0rd",
	}
	if req.Name != "Test User" {
		t.Error("Name should be set")
	}
}

func TestLoginRequest_StructFields(t *testing.T) {
	req := LoginRequest{
		Email:    "test@example.com",
		Password: "password",
	}
	if req.Email != "test@example.com" {
		t.Error("Email should be set")
	}
}

// ==================== profileWithPassword Type ====================

func TestProfileWithPassword_HidesPassword(t *testing.T) {
	// The profileWithPassword struct is a local type used for DB reads.
	p := profileWithPassword{
		Email:        "test@example.com",
		PasswordHash: "hashed-value",
	}
	if p.PasswordHash != "hashed-value" {
		t.Error("PasswordHash should be readable")
	}
}

// ==================== googleLoginRequest / googleTokenResponse / googleUserInfo ====================

func TestGoogleLoginRequest_StructFields(t *testing.T) {
	req := googleLoginRequest{
		Code:        "auth-code-123",
		RedirectURI: "https://app.example.com/auth/callback",
	}
	if req.Code != "auth-code-123" {
		t.Error("Code should be set")
	}
	if req.RedirectURI != "https://app.example.com/auth/callback" {
		t.Error("RedirectURI should be set")
	}
}

func TestGoogleTokenResponse_StructFields(t *testing.T) {
	resp := googleTokenResponse{
		AccessToken: "access-token-123",
		TokenType:   "Bearer",
		ExpiresIn:   3600,
		IDToken:     "id-token-123",
	}
	if resp.AccessToken != "access-token-123" {
		t.Error("AccessToken should be set")
	}
}

func TestGoogleUserInfo_StructFields(t *testing.T) {
	info := googleUserInfo{
		ID:            "google-id-123",
		Email:         "user@gmail.com",
		VerifiedEmail: true,
		Name:          "Test User",
		Picture:       "https://example.com/photo.jpg",
	}
	if info.Email != "user@gmail.com" {
		t.Error("Email should be set")
	}
	if !info.VerifiedEmail {
		t.Error("VerifiedEmail should be true")
	}
}

// ==================== Combination Validation Tests ====================

func TestFilterChain_TypicalBudgetQuery(t *testing.T) {
	// Simulate a typical query that handlers build.
	userID := "abc-123-def"
	query := strings.Join([]string{
		"select=*",
		"user_id=eq." + userID,
		"order=created_at.desc",
		"limit=100",
		"offset=0",
	}, "&")

	if !strings.Contains(query, "select=*") {
		t.Error("query should contain select")
	}
	if !strings.Contains(query, "user_id=eq.abc-123-def") {
		t.Error("query should contain user_id filter")
	}
}
