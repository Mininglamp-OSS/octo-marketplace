package notify

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"time"
)

// The published language-neutral test vector from octo-server
// docs/card-action-callback-consumer.md ("Language-neutral test vector").
//
// DO NOT "fix" these constants to make a failing test pass. They are the
// cross-repo wire contract. If this test fails, marketplace and octo-server
// have drifted on the canonical string, and every production callback would
// 401 — reconcile the implementations, not the vector.
const (
	vectorSecret     = "0123456789abcdef0123456789abcdef" // gitleaks:allow
	vectorMethod     = "POST"
	vectorPath       = "/v1/card-actions/decide"
	vectorTimestamp  = "1784073600"
	vectorEventID    = "9007199254740993"
	vectorBody       = `{"event_id":"9007199254740993","action_id":"approval-execute","decision":"execute","operator_uid":"user-b","inputs":{},"data":{"owner":"tasks","action_type":"task.execute.decision","decision":"execute","task_id":"task-1"},"message_id":"190001234567890","channel_id":"notification","channel_type":1,"space_id":"space-1","acted_at":1784073600}`
	vectorBodySHA256 = "e5f9edc7558b6dbac6f754308b161d79a84e9d4635377a8afd6f95b6baa4c6cc"    // gitleaks:allow
	vectorSignature  = "v1=77d6abe3e80bd90d70545ce90d8c87daafd65a22b62919cee71b450613d6e50f" // gitleaks:allow
)

func TestPublishedVector_BodyDigest(t *testing.T) {
	sum := sha256.Sum256([]byte(vectorBody))
	if got := hex.EncodeToString(sum[:]); got != vectorBodySHA256 {
		t.Fatalf("body sha256 = %s, want %s (the vector body literal was altered)", got, vectorBodySHA256)
	}
}

func TestPublishedVector_CanonicalRequest(t *testing.T) {
	got := CanonicalRequest(vectorMethod, vectorPath, vectorTimestamp, vectorEventID, []byte(vectorBody))
	want := strings.Join([]string{
		"v1", "POST", vectorPath, vectorTimestamp, vectorEventID, vectorBodySHA256,
	}, "\n")
	if got != want {
		t.Fatalf("canonical request mismatch\n got: %q\nwant: %q", got, want)
	}
}

func TestPublishedVector_Sign(t *testing.T) {
	got := Sign(vectorSecret, vectorMethod, vectorPath, vectorTimestamp, vectorEventID, []byte(vectorBody))
	if got != vectorSignature {
		t.Fatalf("signature = %s, want %s", got, vectorSignature)
	}
}

func TestPublishedVector_Verify(t *testing.T) {
	if !Verify(vectorSecret, vectorSignature, vectorMethod, vectorPath, vectorTimestamp, vectorEventID, []byte(vectorBody)) {
		t.Fatal("Verify rejected the published vector")
	}
}

// TestSign_EveryComponentIsLoadBearing flips one component of the canonical
// string at a time; each must change the signature. A component that does NOT
// change it is not covered by the MAC and can be tampered with in transit.
func TestSign_EveryComponentIsLoadBearing(t *testing.T) {
	cases := []struct {
		name                                     string
		secret, method, path, timestamp, eventID string
		body                                     string
	}{
		{"secret", "0123456789abcdef0123456789abcdeF", vectorMethod, vectorPath, vectorTimestamp, vectorEventID, vectorBody}, // gitleaks:allow
		{"method", vectorSecret, "PUT", vectorPath, vectorTimestamp, vectorEventID, vectorBody},
		{"path", vectorSecret, vectorMethod, "/v1/card-actions/decide2", vectorTimestamp, vectorEventID, vectorBody},
		{"timestamp", vectorSecret, vectorMethod, vectorPath, "1784073601", vectorEventID, vectorBody},
		{"event id", vectorSecret, vectorMethod, vectorPath, vectorTimestamp, "9007199254740994", vectorBody},
		// One byte of the body: the trailing digit of acted_at.
		{"body byte", vectorSecret, vectorMethod, vectorPath, vectorTimestamp, vectorEventID,
			strings.Replace(vectorBody, `"acted_at":1784073600}`, `"acted_at":1784073601}`, 1)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.name == "body byte" && tc.body == vectorBody {
				t.Fatal("body mutation did not apply")
			}
			got := Sign(tc.secret, tc.method, tc.path, tc.timestamp, tc.eventID, []byte(tc.body))
			if got == vectorSignature {
				t.Fatalf("flipping %s did not change the signature: %s", tc.name, got)
			}
			// The published signature must not verify against the tampered
			// inputs either (the secret case is covered by TestVerify_Rejections).
			if tc.name != "secret" &&
				Verify(vectorSecret, vectorSignature, tc.method, tc.path, tc.timestamp, tc.eventID, []byte(tc.body)) {
				t.Fatalf("Verify accepted the vector signature with %s tampered", tc.name)
			}
		})
	}
}

func TestVerify_Rejections(t *testing.T) {
	body := []byte(vectorBody)
	bare := strings.TrimPrefix(vectorSignature, "v1=")

	cases := []struct {
		name      string
		secret    string
		signature string
	}{
		{"empty secret means closed, not open", "", vectorSignature},
		{"empty signature", vectorSecret, ""},
		{"missing v1= prefix", vectorSecret, bare},
		{"wrong version prefix", vectorSecret, "v2=" + bare},
		{"uppercase hex", vectorSecret, "v1=" + strings.ToUpper(bare)},
		{"truncated digest", vectorSecret, vectorSignature[:len(vectorSignature)-2]},
		{"wrong secret", "0123456789abcdef0123456789abcdeF", vectorSignature}, // gitleaks:allow
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if Verify(tc.secret, tc.signature, vectorMethod, vectorPath, vectorTimestamp, vectorEventID, body) {
				t.Fatal("Verify accepted a signature it must reject")
			}
		})
	}

	// Pin the empty-secret close specifically: Sign("", …) computes an HMAC
	// with an empty key, and Verify("", Sign("", …), …) MUST return false even
	// though the two match byte-for-byte. A test that only feeds a signature
	// produced with a DIFFERENT secret passes whether or not the empty-secret
	// guard exists (HMAC mismatch alone rejects it), so this case would catch
	// a regression where someone removes the `secret == ""` early-return.
	t.Run("empty secret rejects a signature produced with the empty key", func(t *testing.T) {
		sig := Sign("", vectorMethod, vectorPath, vectorTimestamp, vectorEventID, body)
		if Verify("", sig, vectorMethod, vectorPath, vectorTimestamp, vectorEventID, body) {
			t.Fatal("Verify accepted an empty-secret signature; the endpoint must be closed when unconfigured")
		}
	})
}

// TestVerify_ReSerializedBodyFails is the reason the callback handler must read
// the raw body: semantically identical JSON with different bytes must fail.
func TestVerify_ReSerializedBodyFails(t *testing.T) {
	reserialized := strings.ReplaceAll(vectorBody, `":`, `": `)
	if reserialized == vectorBody {
		t.Fatal("re-serialization produced identical bytes")
	}
	if Verify(vectorSecret, vectorSignature, vectorMethod, vectorPath, vectorTimestamp, vectorEventID, []byte(reserialized)) {
		t.Fatal("Verify accepted a re-serialized body")
	}
}

func TestVerifyTimestampAt(t *testing.T) {
	now := time.Unix(1784073600, 0)
	const skew = 5 * time.Minute

	cases := []struct {
		name   string
		header string
		now    time.Time
		skew   time.Duration
		want   bool
	}{
		{"exact now", "1784073600", now, skew, true},
		{"stale inside window", "1784073301", now, skew, true},
		{"stale on the boundary", "1784073300", now, skew, true},
		{"stale past the boundary", "1784073299", now, skew, false},
		{"future inside window", "1784073899", now, skew, true},
		{"future on the boundary", "1784073900", now, skew, true},
		// Symmetry: a future-dated timestamp is as suspect as a stale one.
		{"future past the boundary", "1784073901", now, skew, false},
		{"far future", "9999999999", now, skew, false},
		{"empty", "", now, skew, false},
		{"whitespace only", "   ", now, skew, false},
		{"non-numeric", "not-a-number", now, skew, false},
		{"float", "1784073600.5", now, skew, false},
		// Regression: a Duration-based comparison saturates and wraps for these,
		// making an absurd timestamp look fresh.
		{"milliseconds mistaken for seconds", "1784073600000", now, skew, false},
		{"max int64", "9223372036854775807", now, skew, false},
		{"zero", "0", now, skew, false},
		{"negative", "-1784073600", now, skew, false},
		{"surrounding whitespace tolerated", " 1784073600 ", now, skew, true},
		{"non-positive skew means closed", "1784073600", now, 0, false},
		{"negative skew means closed", "1784073600", now, -time.Second, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := VerifyTimestampAt(tc.header, tc.now, tc.skew); got != tc.want {
				t.Fatalf("VerifyTimestampAt(%q) = %v, want %v", tc.header, got, tc.want)
			}
		})
	}
}

func TestCanonicalRequest_MethodIsUppercased(t *testing.T) {
	lower := CanonicalRequest("post", vectorPath, vectorTimestamp, vectorEventID, []byte(vectorBody))
	upper := CanonicalRequest("POST", vectorPath, vectorTimestamp, vectorEventID, []byte(vectorBody))
	if lower != upper {
		t.Fatal("method case must not change the canonical request")
	}
}

func TestCanonicalRequest_EmptyBody(t *testing.T) {
	// sha256 of the empty string, for the degenerate no-body case.
	const emptySHA = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	got := CanonicalRequest("POST", "/x", "1", "2", nil)
	if !strings.HasSuffix(got, "\n"+emptySHA) {
		t.Fatalf("empty body digest missing from canonical request: %q", got)
	}
}
