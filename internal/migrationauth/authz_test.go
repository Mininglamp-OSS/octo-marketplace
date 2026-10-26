package migrationauth

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

type fixture struct {
	trust    *TrustConfig
	payload  Payload
	envelope Envelope
	now      time.Time
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	payload := Payload{
		SchemaVersion:   SchemaVersion,
		AuthorizationID: "auth-1",
		KeyID:           "key-1",
		IssuerID:        "issuer-1",
		ActorID:         "migration-operator",
		EnvironmentID:   "prod-eu",
		SourceScope:     strings.Repeat("a", 64),
		SourceIdentity:  "plugin-1@version-1",
		PlanDigest:      strings.Repeat("b", 64),
		BatchID:         "batch-1",
		IssuedAt:        now.Add(-time.Minute).Format(time.RFC3339),
		ExpiresAt:       now.Add(time.Hour).Format(time.RFC3339),
	}
	envelope, err := Sign(payload, private)
	if err != nil {
		t.Fatal(err)
	}
	cfg := TrustConfig{
		SchemaVersion: SchemaVersion,
		EnvironmentID: "prod-eu",
		ActorID:       "migration-operator",
		Keys: []TrustKey{{
			KeyID:     "key-1",
			IssuerID:  "issuer-1",
			PublicKey: base64.RawURLEncoding.EncodeToString(public),
			Enabled:   true,
		}},
		Issuers: []TrustIssuer{{
			IssuerID:            "issuer-1",
			AllowedSourceScopes: []string{strings.Repeat("a", 64)},
			Enabled:             true,
		}},
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	trust, err := ParseTrustConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	return fixture{trust: trust, payload: payload, envelope: envelope, now: now}
}

func expected(p Payload) Expected {
	return Expected{SourceScope: p.SourceScope, SourceIdentity: p.SourceIdentity, PlanDigest: p.PlanDigest, BatchID: p.BatchID}
}

func TestVerifyAcceptsSignedArtifact(t *testing.T) {
	f := newFixture(t)
	got, err := f.trust.Verify(f.envelope, expected(f.payload), f.now)
	if err != nil {
		t.Fatal(err)
	}
	if got.AuthorizationID != "auth-1" || got.ActorID != "migration-operator" {
		t.Fatalf("payload: %+v", got)
	}
	// Re-serializing the artifact must not change verification.
	raw, _ := json.Marshal(f.envelope)
	parsed, err := ParseEnvelope(raw)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.trust.Verify(parsed, expected(f.payload), f.now); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyRejectsTamperedSignatureAndPayload(t *testing.T) {
	f := newFixture(t)
	raw, _ := base64.RawURLEncoding.DecodeString(f.envelope.Signature)
	raw[0] ^= 0xff
	f.envelope.Signature = base64.RawURLEncoding.EncodeToString(raw)
	if _, err := f.trust.Verify(f.envelope, expected(f.payload), f.now); !errors.Is(err, ErrSignature) {
		t.Fatalf("tampered signature: %v", err)
	}

	f = newFixture(t)
	f.envelope.Payload.BatchID = "batch-2"
	if _, err := f.trust.Verify(f.envelope, Expected{BatchID: "batch-2"}, f.now); !errors.Is(err, ErrSignature) {
		t.Fatalf("tampered payload: %v", err)
	}
}

func TestVerifyRejectsBindingMismatches(t *testing.T) {
	f := newFixture(t)
	for name, expected := range map[string]Expected{
		"actor":       {},
		"scope":       {SourceScope: strings.Repeat("c", 64)},
		"identity":    {SourceIdentity: "plugin-2@version-2"},
		"plan-digest": {PlanDigest: strings.Repeat("d", 64)},
		"batch":       {BatchID: "other-batch"},
	} {
		t.Run(name, func(t *testing.T) {
			if name == "actor" {
				cfg := *f.trust
				cfg.ActorID = "someone-else"
				_, err := cfg.Verify(f.envelope, Expected{}, f.now)
				if !errors.Is(err, ErrActor) {
					t.Fatalf("actor: %v", err)
				}
				return
			}
			_, err := f.trust.Verify(f.envelope, expected, f.now)
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("%s: %v", name, err)
			}
		})
	}
}

func TestVerifyRejectsExpiryIssuedFutureAndBadUTC(t *testing.T) {
	f := newFixture(t)
	if _, err := f.trust.Verify(f.envelope, expected(f.payload), f.now.Add(2*time.Hour)); !errors.Is(err, ErrExpired) {
		t.Fatalf("expired: %v", err)
	}
	if _, err := f.trust.Verify(f.envelope, expected(f.payload), f.now.Add(-2*time.Hour)); !errors.Is(err, ErrNotYetValid) {
		t.Fatalf("future: %v", err)
	}
	f = newFixture(t)
	f.envelope.Payload.IssuedAt = "2026-10-26T12:00:00+02:00"
	if _, err := ParseEnvelope(mustJSON(t, f.envelope)); !errors.Is(err, ErrMalformedArtifact) {
		t.Fatalf("non-UTC timestamp: %v", err)
	}
}

func TestVerifyRejectsRevokedKeyAuthorizationAndDisabledIssuer(t *testing.T) {
	f := newFixture(t)
	f.trust.revokedKeys["key-1"] = true
	if _, err := f.trust.Verify(f.envelope, expected(f.payload), f.now); !errors.Is(err, ErrRevoked) {
		t.Fatalf("revoked key: %v", err)
	}
	f = newFixture(t)
	f.trust.revokedAuth["auth-1"] = true
	if _, err := f.trust.Verify(f.envelope, expected(f.payload), f.now); !errors.Is(err, ErrRevoked) {
		t.Fatalf("revoked authorization: %v", err)
	}
	f = newFixture(t)
	issuer := f.trust.issuers["issuer-1"]
	issuer.Enabled = false
	f.trust.issuers["issuer-1"] = issuer
	if _, err := f.trust.Verify(f.envelope, expected(f.payload), f.now); !errors.Is(err, ErrIssuer) {
		t.Fatalf("disabled issuer: %v", err)
	}
	f = newFixture(t)
	issuer = f.trust.issuers["issuer-1"]
	issuer.AllowedSourceScopes = nil
	f.trust.issuers["issuer-1"] = issuer
	if _, err := f.trust.Verify(f.envelope, expected(f.payload), f.now); !errors.Is(err, ErrScope) {
		t.Fatalf("scope not allowed: %v", err)
	}
}

func TestArtifactCannotSupplyTrustInput(t *testing.T) {
	f := newFixture(t)
	var doc map[string]any
	raw, _ := json.Marshal(f.envelope)
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	doc["public_key"] = "attacker"
	raw, _ = json.Marshal(doc)
	if _, err := ParseEnvelope(raw); !errors.Is(err, ErrMalformedArtifact) {
		t.Fatalf("attachment key accepted: %v", err)
	}
	payload := f.envelope.Payload
	payload.SchemaVersion = "2.0"
	if _, err := ParseEnvelope(mustJSON(t, Envelope{Payload: payload, Signature: f.envelope.Signature})); !errors.Is(err, ErrMalformedArtifact) {
		t.Fatalf("unknown schema accepted: %v", err)
	}
}

func TestTrustConfigRejectsMalformedOrDisabledKeys(t *testing.T) {
	f := newFixture(t)
	for name, mutate := range map[string]func(*TrustConfig){
		"no-environment": func(c *TrustConfig) { c.EnvironmentID = "" },
		"bad-public-key": func(c *TrustConfig) { c.Keys[0].PublicKey = "not-a-key" },
		"padding":        func(c *TrustConfig) { c.Keys[0].PublicKey += "=" },
		"bad-scope":      func(c *TrustConfig) { c.Issuers[0].AllowedSourceScopes = []string{"not-hex"} },
		"duplicate": func(c *TrustConfig) {
			c.Keys = append(c.Keys, c.Keys[0])
		},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := *f.trust
			cfg.Keys = append([]TrustKey(nil), f.trust.Keys...)
			cfg.Issuers = append([]TrustIssuer(nil), f.trust.Issuers...)
			mutate(&cfg)
			raw, _ := json.Marshal(cfg)
			if _, err := ParseTrustConfig(raw); !errors.Is(err, ErrMalformedTrust) {
				t.Fatalf("accepted malformed trust: %v", err)
			}
		})
	}
}

func TestTrustFromEnvRequiresPath(t *testing.T) {
	t.Setenv("MARKETPLACE_MIGRATION_TRUST_CONFIG", "")
	if _, err := TrustFromEnv(); !errors.Is(err, ErrMalformedTrust) {
		t.Fatalf("empty path: %v", err)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
