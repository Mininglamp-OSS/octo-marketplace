package notify

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"time"
)

// HMAC-SHA256 authentication for octo-server's card-action callback.
//
// This is a CROSS-REPO WIRE CONTRACT. The authoritative definition is
// octo-server docs/card-action-callback-consumer.md; signature_test.go pins the
// published language-neutral test vector so a drift on either side fails a test
// here instead of 401-ing every callback in production.
//
// Canonical UTF-8 value, "\n"-joined:
//
//	v1\nPOST\n<escaped-path>\n<timestamp>\n<event-id>\n<sha256-hex-of-exact-body>
//
// and the header value is "v1=" + lowercase hex HMAC-SHA256 of that value.

// signatureVersion prefixes both the canonical value and the header value.
const signatureVersion = "v1"

// Headers octo-server sends with a card-action callback.
const (
	HeaderSignature = "X-Octo-Signature"
	HeaderTimestamp = "X-Octo-Timestamp"
	HeaderEventID   = "X-Octo-Event-ID"
)

// CanonicalRequest builds the exact string that gets signed.
//
// body MUST be the raw bytes read off the wire. Re-serializing the decoded JSON
// changes the digest and therefore the signature — the callback handler has to
// read the body with a raw-body reader BEFORE any JSON binding.
//
// path MUST be the escaped request path (http.Request.URL.EscapedPath()), not
// the decoded one, because that is what octo-server signed.
func CanonicalRequest(method, path, timestamp, eventID string, body []byte) string {
	sum := sha256.Sum256(body)
	return strings.Join([]string{
		signatureVersion,
		strings.ToUpper(method),
		path,
		timestamp,
		eventID,
		hex.EncodeToString(sum[:]),
	}, "\n")
}

// Sign returns the full header value ("v1=<64 lowercase hex>") for the inputs.
func Sign(secret, method, path, timestamp, eventID string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(CanonicalRequest(method, path, timestamp, eventID, body)))
	return signatureVersion + "=" + hex.EncodeToString(mac.Sum(nil))
}

// Verify reports whether signature authenticates the given request components.
//
// It does NOT check freshness — pair every call with VerifyTimestampAt, or a
// captured request replays forever against a still-valid signature.
//
// An empty secret is always a rejection: an unconfigured callback secret means
// the route is CLOSED, never open. A signature without the "v1=" prefix is also
// rejected rather than being compared as a bare hex digest, so a future "v2="
// scheme can never be silently accepted under v1 rules.
func Verify(secret, signature, method, path, timestamp, eventID string, body []byte) bool {
	if secret == "" || signature == "" {
		return false
	}
	if !strings.HasPrefix(signature, signatureVersion+"=") {
		return false
	}
	expected := Sign(secret, method, path, timestamp, eventID, body)
	// Constant-time: a byte-at-a-time compare would leak the correct prefix
	// length to an attacker who can time repeated callbacks.
	return hmac.Equal([]byte(expected), []byte(signature))
}

// VerifyTimestampAt reports whether the X-Octo-Timestamp header (Unix seconds)
// is within maxSkew of now.
//
// The clock is injected so the freshness boundary is testable without sleeping.
// Rejection is SYMMETRIC: a future-dated timestamp is as suspect as a stale one
// (it would otherwise extend a captured request's replay window arbitrarily).
// Empty, non-numeric, and non-positive values are rejected, as is a
// non-positive maxSkew (an unconfigured window means closed, not infinite).
//
// The comparison is done in integer SECONDS, not via time.Time.Sub. Sub
// saturates at ±time.Duration's ~292-year range, and negating the saturated
// minimum wraps back to a negative value — so a header like "1784073600000"
// (milliseconds pasted where seconds belong) would compare as fresh. Seconds
// arithmetic has no such cliff. maxSkew is floored to whole seconds, which
// costs nothing because the header only carries seconds.
func VerifyTimestampAt(header string, now time.Time, maxSkew time.Duration) bool {
	if maxSkew <= 0 {
		return false
	}
	header = strings.TrimSpace(header)
	if header == "" {
		return false
	}
	ts, err := strconv.ParseInt(header, 10, 64)
	if err != nil || ts <= 0 {
		return false
	}
	delta := now.Unix() - ts
	if delta < 0 {
		delta = -delta
	}
	return delta >= 0 && delta <= int64(maxSkew/time.Second)
}
