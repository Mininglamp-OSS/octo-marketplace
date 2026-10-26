// Package migrationauth verifies the offline authorization artifact for the
// marketplace migration CLI.
//
// The artifact is {payload,signature}. The signature is Ed25519 over the UTF-8
// JCS encoding of payload, base64url without padding. Trusted keys and issuers
// come exclusively from deployment-managed configuration; the artifact cannot
// carry a public key or select an algorithm.
package migrationauth

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/jcs"
)

const SchemaVersion = "1.0"

const maxArtifactBytes = 1 << 20

var (
	ErrInvalid            = errors.New("migration authorization invalid")
	ErrSignature          = fmt.Errorf("%w: signature", ErrInvalid)
	ErrExpired            = fmt.Errorf("%w: expiry", ErrInvalid)
	ErrNotYetValid        = fmt.Errorf("%w: issued_at", ErrInvalid)
	ErrRevoked            = fmt.Errorf("%w: revoked", ErrInvalid)
	ErrActor              = fmt.Errorf("%w: actor", ErrInvalid)
	ErrEnvironment        = fmt.Errorf("%w: environment", ErrInvalid)
	ErrScope              = fmt.Errorf("%w: source scope", ErrInvalid)
	ErrIdentity           = fmt.Errorf("%w: source identity", ErrInvalid)
	ErrPlanDigest         = fmt.Errorf("%w: plan digest", ErrInvalid)
	ErrBatch              = fmt.Errorf("%w: batch", ErrInvalid)
	ErrUnknownKey         = fmt.Errorf("%w: key", ErrInvalid)
	ErrIssuer             = fmt.Errorf("%w: issuer", ErrInvalid)
	ErrMalformedArtifact  = fmt.Errorf("%w: malformed artifact", ErrInvalid)
	ErrMalformedTrust     = errors.New("migration trust configuration invalid")
	ErrAttachmentKeyInput = errors.New("migration authorization must not carry a public key or algorithm")
)

var (
	lowerHex64 = regexp.MustCompile(`^[0-9a-f]{64}$`)
	idPattern  = regexp.MustCompile(`^[A-Za-z0-9._:/@+-]{1,200}$`)
)

// Payload is the signed migration authorization claim set. The field order is
// irrelevant to verification; JCS sorts object keys.
type Payload struct {
	SchemaVersion   string `json:"schema_version"`
	AuthorizationID string `json:"authorization_id"`
	KeyID           string `json:"key_id"`
	IssuerID        string `json:"issuer_id"`
	ActorID         string `json:"actor_id"`
	EnvironmentID   string `json:"environment_id"`
	SourceScope     string `json:"source_scope"`
	SourceIdentity  string `json:"source_identity"`
	PlanDigest      string `json:"plan_digest"`
	BatchID         string `json:"batch_id"`
	IssuedAt        string `json:"issued_at"`
	ExpiresAt       string `json:"expires_at"`
}

// Envelope is the on-disk authorization artifact. Unknown fields (in
// particular an embedded public key or algorithm selection) are rejected.
type Envelope struct {
	Payload   Payload `json:"payload"`
	Signature string  `json:"signature"`
}

// TrustKey is one deployment-managed verification key.
type TrustKey struct {
	KeyID     string `json:"key_id"`
	IssuerID  string `json:"issuer_id"`
	PublicKey string `json:"public_key"`
	Enabled   bool   `json:"enabled"`
}

// TrustIssuer bounds the source scopes one issuer may authorize.
type TrustIssuer struct {
	IssuerID            string   `json:"issuer_id"`
	AllowedSourceScopes []string `json:"allowed_source_scopes"`
	Enabled             bool     `json:"enabled"`
}

// TrustConfig is the complete verification policy. It is loaded from a
// deployment-managed file, never from the plan or authorization artifact.
type TrustConfig struct {
	SchemaVersion           string        `json:"schema_version"`
	EnvironmentID           string        `json:"environment_id"`
	ActorID                 string        `json:"actor_id"`
	Keys                    []TrustKey    `json:"keys"`
	Issuers                 []TrustIssuer `json:"issuers"`
	RevokedKeyIDs           []string      `json:"revoked_key_ids"`
	RevokedAuthorizationIDs []string      `json:"revoked_authorization_ids"`

	keys        map[string]trustedKey
	issuers     map[string]TrustIssuer
	revokedKeys map[string]bool
	revokedAuth map[string]bool
}

type trustedKey struct {
	issuerID  string
	publicKey ed25519.PublicKey
	enabled   bool
}

// Expected binds the authorization to the exact execution target.
type Expected struct {
	SourceScope    string
	SourceIdentity string
	PlanDigest     string
	BatchID        string
}

// ParseEnvelope parses the authorization artifact with strict JSON semantics.
func ParseEnvelope(raw []byte) (Envelope, error) {
	if len(raw) == 0 || len(raw) > maxArtifactBytes {
		return Envelope{}, ErrMalformedArtifact
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var envelope Envelope
	if err := decoder.Decode(&envelope); err != nil {
		return Envelope{}, ErrMalformedArtifact
	}
	if err := noTrailing(decoder); err != nil {
		return Envelope{}, ErrMalformedArtifact
	}
	if err := validatePayloadShape(envelope.Payload); err != nil {
		return Envelope{}, err
	}
	if len(envelope.Signature) != base64.RawURLEncoding.EncodedLen(ed25519.SignatureSize) {
		return Envelope{}, ErrMalformedArtifact
	}
	return envelope, nil
}

// ParseTrustConfig parses and indexes a deployment trust configuration.
func ParseTrustConfig(raw []byte) (*TrustConfig, error) {
	if len(raw) == 0 || len(raw) > maxArtifactBytes {
		return nil, ErrMalformedTrust
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var cfg TrustConfig
	if err := decoder.Decode(&cfg); err != nil {
		return nil, ErrMalformedTrust
	}
	if err := noTrailing(decoder); err != nil {
		return nil, ErrMalformedTrust
	}
	if cfg.SchemaVersion != SchemaVersion || !validID(cfg.EnvironmentID) || !validID(cfg.ActorID) {
		return nil, ErrMalformedTrust
	}
	cfg.keys = map[string]trustedKey{}
	for _, key := range cfg.Keys {
		if !validID(key.KeyID) || !validID(key.IssuerID) || key.KeyID == "" {
			return nil, ErrMalformedTrust
		}
		if _, exists := cfg.keys[key.KeyID]; exists {
			return nil, ErrMalformedTrust
		}
		public, err := decodeKey(key.PublicKey)
		if err != nil {
			return nil, ErrMalformedTrust
		}
		cfg.keys[key.KeyID] = trustedKey{issuerID: key.IssuerID, publicKey: public, enabled: key.Enabled}
	}
	cfg.issuers = map[string]TrustIssuer{}
	for _, issuer := range cfg.Issuers {
		if !validID(issuer.IssuerID) {
			return nil, ErrMalformedTrust
		}
		if _, exists := cfg.issuers[issuer.IssuerID]; exists {
			return nil, ErrMalformedTrust
		}
		for _, scope := range issuer.AllowedSourceScopes {
			if !lowerHex64.MatchString(scope) {
				return nil, ErrMalformedTrust
			}
		}
		cfg.issuers[issuer.IssuerID] = issuer
	}
	cfg.revokedKeys = map[string]bool{}
	for _, id := range cfg.RevokedKeyIDs {
		if !validID(id) {
			return nil, ErrMalformedTrust
		}
		cfg.revokedKeys[id] = true
	}
	cfg.revokedAuth = map[string]bool{}
	for _, id := range cfg.RevokedAuthorizationIDs {
		if !validID(id) {
			return nil, ErrMalformedTrust
		}
		cfg.revokedAuth[id] = true
	}
	return &cfg, nil
}

// LoadTrustConfig reads a deployment trust file. The file is never supplied by
// the plan or operator flags.
func LoadTrustConfig(path string) (*TrustConfig, error) {
	raw, err := readBounded(path)
	if err != nil {
		return nil, ErrMalformedTrust
	}
	return ParseTrustConfig(raw)
}

// TrustFromEnv loads the deployment-managed trust file path from the
// environment. An empty path is a configuration failure, not an open mode.
func TrustFromEnv() (*TrustConfig, error) {
	path := strings.TrimSpace(os.Getenv("MARKETPLACE_MIGRATION_TRUST_CONFIG"))
	if path == "" {
		return nil, fmt.Errorf("%w: MARKETPLACE_MIGRATION_TRUST_CONFIG is required", ErrMalformedTrust)
	}
	return LoadTrustConfig(path)
}

// Verify checks the signature and every binding for expected. It returns the
// verified payload only; callers may log IDs and safe reasons, never keys.
func (c *TrustConfig) Verify(envelope Envelope, expected Expected, now time.Time) (*Payload, error) {
	if err := validatePayloadShape(envelope.Payload); err != nil {
		return nil, err
	}
	payload := envelope.Payload
	if c.revokedAuth[payload.AuthorizationID] {
		return nil, ErrRevoked
	}
	key, ok := c.keys[payload.KeyID]
	if !ok {
		return nil, ErrUnknownKey
	}
	if c.revokedKeys[payload.KeyID] {
		return nil, ErrRevoked
	}
	if !key.enabled {
		return nil, ErrUnknownKey
	}
	if key.issuerID != payload.IssuerID {
		return nil, ErrIssuer
	}
	issuer, ok := c.issuers[payload.IssuerID]
	if !ok || !issuer.Enabled {
		return nil, ErrIssuer
	}
	scopeAllowed := false
	for _, scope := range issuer.AllowedSourceScopes {
		if scope == payload.SourceScope {
			scopeAllowed = true
			break
		}
	}
	if !scopeAllowed {
		return nil, ErrScope
	}
	if payload.ActorID != c.ActorID {
		return nil, ErrActor
	}
	if payload.EnvironmentID != c.EnvironmentID {
		return nil, ErrEnvironment
	}
	if expected.SourceScope != "" && payload.SourceScope != expected.SourceScope {
		return nil, ErrScope
	}
	if expected.SourceIdentity != "" && payload.SourceIdentity != expected.SourceIdentity {
		return nil, ErrIdentity
	}
	if expected.PlanDigest != "" && payload.PlanDigest != expected.PlanDigest {
		return nil, ErrPlanDigest
	}
	if expected.BatchID != "" && payload.BatchID != expected.BatchID {
		return nil, ErrBatch
	}
	issued, err := parseUTC(payload.IssuedAt)
	if err != nil {
		return nil, ErrMalformedArtifact
	}
	expires, err := parseUTC(payload.ExpiresAt)
	if err != nil || !expires.After(issued) {
		return nil, ErrMalformedArtifact
	}
	if now.Before(issued) {
		return nil, ErrNotYetValid
	}
	if !now.Before(expires) {
		return nil, ErrExpired
	}
	signature, err := decodeSignature(envelope.Signature)
	if err != nil {
		return nil, ErrMalformedArtifact
	}
	canonical, err := jcs.Canonicalize(payload)
	if err != nil {
		return nil, ErrMalformedArtifact
	}
	if !ed25519.Verify(key.publicKey, canonical, signature) {
		return nil, ErrSignature
	}
	return &payload, nil
}

// Sign builds an artifact for issuer tooling and tests. Production private keys
// live outside this service.
func Sign(payload Payload, privateKey ed25519.PrivateKey) (Envelope, error) {
	if err := validatePayloadShape(payload); err != nil {
		return Envelope{}, err
	}
	if len(privateKey) != ed25519.PrivateKeySize {
		return Envelope{}, ErrSignature
	}
	canonical, err := jcs.Canonicalize(payload)
	if err != nil {
		return Envelope{}, err
	}
	signature := ed25519.Sign(privateKey, canonical)
	return Envelope{Payload: payload, Signature: base64.RawURLEncoding.EncodeToString(signature)}, nil
}

// CanonicalPayload is exposed for issuer/tooling tests that must sign exactly
// the bytes the verifier will canonicalize.
func CanonicalPayload(payload Payload) ([]byte, error) {
	return jcs.Canonicalize(payload)
}

func validatePayloadShape(payload Payload) error {
	if payload.SchemaVersion != SchemaVersion {
		return ErrMalformedArtifact
	}
	for _, value := range []string{payload.AuthorizationID, payload.KeyID, payload.IssuerID, payload.ActorID, payload.EnvironmentID, payload.BatchID} {
		if !validID(value) {
			return ErrMalformedArtifact
		}
	}
	if !lowerHex64.MatchString(payload.SourceScope) || !lowerHex64.MatchString(payload.PlanDigest) {
		return ErrMalformedArtifact
	}
	if strings.TrimSpace(payload.SourceIdentity) == "" || len(payload.SourceIdentity) > 512 || strings.ContainsAny(payload.SourceIdentity, "\x00\r\n") {
		return ErrMalformedArtifact
	}
	if _, err := parseUTC(payload.IssuedAt); err != nil {
		return ErrMalformedArtifact
	}
	if _, err := parseUTC(payload.ExpiresAt); err != nil {
		return ErrMalformedArtifact
	}
	return nil
}

func parseUTC(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, err
	}
	// Require an explicit zero offset; a local or non-UTC instant is not the
	// deployment contract and would make expiry reasoning ambiguous.
	if _, offset := parsed.Zone(); offset != 0 {
		return time.Time{}, fmt.Errorf("timestamp is not UTC")
	}
	return parsed.UTC(), nil
}

func decodeKey(encoded string) (ed25519.PublicKey, error) {
	if strings.Contains(encoded, "=") {
		return nil, ErrMalformedTrust
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(raw) != ed25519.PublicKeySize {
		return nil, ErrMalformedTrust
	}
	return ed25519.PublicKey(raw), nil
}

func decodeSignature(encoded string) ([]byte, error) {
	if strings.Contains(encoded, "=") {
		return nil, ErrMalformedArtifact
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(raw) != ed25519.SignatureSize {
		return nil, ErrMalformedArtifact
	}
	return raw, nil
}

func validID(value string) bool {
	return value != "" && idPattern.MatchString(value)
}

func noTrailing(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err == nil {
		return fmt.Errorf("trailing JSON value")
	} else if !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

func readBounded(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, maxArtifactBytes+1))
	if err != nil || len(raw) > maxArtifactBytes {
		return nil, ErrMalformedTrust
	}
	return raw, nil
}
