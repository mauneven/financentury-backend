package handlers

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v2"

	"github.com/the-financial-workspace/backend/internal/captcha"
)

// TestAttestKeyStore_LookupNoDB verifies the production KeyStore returns
// ErrKeyNotFound when there is no database wired (test env). This is the
// fail-safe path that keeps existing unit tests building.
func TestAttestKeyStore_LookupNoDB(t *testing.T) {
	_, _, err := AttestKeyStore{}.Lookup(context.Background(), "any-id")
	if !errors.Is(err, captcha.ErrKeyNotFound) {
		t.Fatalf("Lookup without DB = %v, want ErrKeyNotFound", err)
	}
}

// TestAttestKeyStore_BumpCounterNoDB verifies BumpCounter is a no-op when
// the database is unwired.
func TestAttestKeyStore_BumpCounterNoDB(t *testing.T) {
	store := AttestKeyStore{}
	if err := store.BumpCounter(context.Background(), "id", 1); err != nil {
		t.Fatalf("BumpCounter without DB = %v, want nil", err)
	}
}

// TestAttestChallengeStore_NoDB verifies Issue/Consume are no-op safe when
// the database is unwired — operators running on a test rig still get a
// usable challenge buffer to pair with the in-memory verifier.
func TestAttestChallengeStore_NoDB(t *testing.T) {
	store := AttestChallengeStore{}
	c, err := store.Issue(context.Background(), 1)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if len(c) != attestChallengeBytes {
		t.Fatalf("challenge len = %d, want %d", len(c), attestChallengeBytes)
	}
	if err := store.Consume(context.Background(), c); err != nil {
		t.Fatalf("Consume = %v, want nil (no-DB no-op)", err)
	}
}

// TestAttestChallengeHandler_IssuesChallenge verifies the bootstrap GET
// returns a JSON envelope with a base64 challenge + TTL.
func TestAttestChallengeHandler_IssuesChallenge(t *testing.T) {
	app := fiber.New()
	app.Get("/api/attest/challenge", AttestChallenge)
	req := httptest.NewRequest(http.MethodGet, "/api/attest/challenge", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	var decoded struct {
		Challenge string `json:"challenge"`
		ExpiresIn int    `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decoded.Challenge == "" {
		t.Error("challenge empty")
	}
	if decoded.ExpiresIn <= 0 {
		t.Errorf("expires_in = %d, want > 0", decoded.ExpiresIn)
	}
}

// TestAttestRegisterHandler_RejectsMissingFields verifies basic input
// validation on the registration bootstrap.
func TestAttestRegisterHandler_RejectsMissingFields(t *testing.T) {
	app := fiber.New()
	app.Post("/api/attest/register", AttestRegister)
	cases := []map[string]any{
		{},
		{"key_id": "k1"},
		{"key_id": "k1", "attestation_object": []byte("x")},
	}
	for i, body := range cases {
		raw, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/api/attest/register", bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		resp, err := app.Test(req)
		if err != nil {
			t.Fatalf("case %d: %v", i, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("case %d status = %d, want 400", i, resp.StatusCode)
		}
	}
}

// TestAttestRegisterRequestBytes round-trips a registration request body
// to catch JSON tag drift.
func TestAttestRegisterRequestBytes(t *testing.T) {
	_, pub := mustECDSAKey(t)
	pubDER, err := captcha.MarshalECDSAPublicKey(pub)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	body, err := attestRegisterRequestBytes("k", pubDER, []byte("c"))
	if err != nil {
		t.Fatalf("bytes: %v", err)
	}
	var decoded attestRegisterRequest
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decoded.KeyID != "k" {
		t.Errorf("KeyID = %q", decoded.KeyID)
	}
}

func mustECDSAKey(t *testing.T) (*ecdsa.PrivateKey, *ecdsa.PublicKey) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	return priv, &priv.PublicKey
}
