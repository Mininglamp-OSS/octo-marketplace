package plugin

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/notify"
	pluginsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/plugin"
)

const cardSecret = "0123456789abcdef0123456789abcdef" // gitleaks:allow

// cardEngine mounts the callback the way production does: on the ROOT engine,
// with no authenticator in the chain.
func cardEngine(t *testing.T, svc CardActionService, secret string, now time.Time) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h := NewCardAction(svc, secret, 5*time.Minute)
	h.now = func() time.Time { return now }
	h.Register(r)
	return r
}

func signedCardRequest(t *testing.T, body string, ts time.Time, eventID string) *http.Request {
	t.Helper()
	stamp := strconv.FormatInt(ts.Unix(), 10)
	req := httptest.NewRequest(http.MethodPost, CardActionPath, bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(notify.HeaderTimestamp, stamp)
	req.Header.Set(notify.HeaderEventID, eventID)
	req.Header.Set(notify.HeaderSignature, notify.Sign(cardSecret, http.MethodPost, CardActionPath, stamp, eventID, []byte(body)))
	return req
}

func cardBody(eventID, decision, operator, reviewID string) string {
	return `{"event_id":"` + eventID + `","action_id":"approval-execute","decision":"` + decision +
		`","operator_uid":"` + operator + `","inputs":{},"data":{"owner":"marketplace","review_id":"` + reviewID +
		`"},"message_id":"1","channel_id":"notification","channel_type":1,"space_id":"space-a","acted_at":1784073600}`
}

// The signed path: the handler must hash the EXACT bytes it received and hand
// the decoded fields to the service.
func TestCardActionAcceptsASignedCallback(t *testing.T) {
	svc := &fakeCardActionService{result: &pluginsvc.CardActionResult{
		Disposition: "applied", State: "approved", Requester: "user-1",
		Display: map[string]string{"title": "Demo"},
	}}
	now := time.Unix(1784073600, 0)
	body := cardBody("9007199254740993", "approve", "admin-1", "review-1")
	rec := httptest.NewRecorder()
	cardEngine(t, svc, cardSecret, now).ServeHTTP(rec, signedCardRequest(t, body, now, "9007199254740993"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if svc.event != "9007199254740993" || svc.oper != "admin-1" || svc.choice != "approve" || svc.review != "review-1" {
		t.Fatalf("service got %q/%q/%q/%q", svc.event, svc.oper, svc.choice, svc.review)
	}
	// octo-server decodes with DisallowUnknownFields and validates both enums, so
	// the response must be exactly these four keys.
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for key := range out {
		switch key {
		case "disposition", "state", "requester_uid", "display":
		default:
			t.Errorf("unexpected response field %q; octo-server rejects the whole body", key)
		}
	}
	if out["disposition"] != "applied" || out["state"] != "approved" || out["requester_uid"] != "user-1" {
		t.Fatalf("response = %v", out)
	}
}

// Every rejection below must happen BEFORE the service is reached: an
// unauthenticated caller must not be able to make the service do work.
func TestCardActionRejectsUnverifiableRequests(t *testing.T) {
	now := time.Unix(1784073600, 0)
	goodEvent := "9007199254740993"
	goodBody := cardBody(goodEvent, "approve", "admin-1", "review-1")

	tests := []struct {
		name    string
		secret  string
		mutate  func(*http.Request)
		want    int
		reaches bool
	}{
		{
			name:   "unconfigured secret closes the endpoint",
			secret: "",
			want:   http.StatusUnauthorized,
		},
		{
			// The case an attacker would actually try once they know the secret is
			// unset: sign the canonical string with the EMPTY secret, so the HMAC is
			// self-consistent and only a deliberate refusal stops it.
			//
			// Two independent guards enforce this — card_action.go's `h.secret == ""`
			// short-circuit and notify.Verify's own empty-secret refusal (pinned by
			// notify/signature_test.go) — and that is on purpose, so removing either
			// ALONE leaves this test green. It is layered, not redundant: this asserts
			// the invariant survives, and the signature-package test asserts the lower
			// guard exists. Deleting both is what this catches, which is the failure
			// that matters, because then any caller who can reach the endpoint can
			// forge a decision.
			name:   "a callback signed with the empty secret is still refused",
			secret: "",
			mutate: func(r *http.Request) {
				stamp := r.Header.Get(notify.HeaderTimestamp)
				r.Header.Set(notify.HeaderSignature, notify.Sign("", http.MethodPost, CardActionPath, stamp, goodEvent, []byte(goodBody)))
			},
			want: http.StatusUnauthorized,
		},
		{
			name:   "missing signature",
			secret: cardSecret,
			mutate: func(r *http.Request) { r.Header.Del(notify.HeaderSignature) },
			want:   http.StatusUnauthorized,
		},
		{
			name:   "tampered body",
			secret: cardSecret,
			mutate: func(r *http.Request) {
				r.Body = httptest.NewRequest(http.MethodPost, CardActionPath,
					bytes.NewReader([]byte(cardBody(goodEvent, "approve", "attacker", "review-1")))).Body
			},
			want: http.StatusUnauthorized,
		},
		{
			name:   "signature from a different secret",
			secret: cardSecret,
			mutate: func(r *http.Request) {
				stamp := r.Header.Get(notify.HeaderTimestamp)
				r.Header.Set(notify.HeaderSignature, notify.Sign("ffffffffffffffffffffffffffffffff",
					http.MethodPost, CardActionPath, stamp, goodEvent, []byte(goodBody)))
			},
			want: http.StatusUnauthorized,
		},
		{
			name:   "stale timestamp",
			secret: cardSecret,
			mutate: func(r *http.Request) {
				stale := strconv.FormatInt(now.Add(-10*time.Minute).Unix(), 10)
				r.Header.Set(notify.HeaderTimestamp, stale)
				r.Header.Set(notify.HeaderSignature, notify.Sign(cardSecret, http.MethodPost, CardActionPath, stale, goodEvent, []byte(goodBody)))
			},
			want: http.StatusUnauthorized,
		},
		{
			name:   "header event id does not match the body",
			secret: cardSecret,
			mutate: func(r *http.Request) {
				other := "9007199254740994"
				stamp := r.Header.Get(notify.HeaderTimestamp)
				r.Header.Set(notify.HeaderEventID, other)
				// Re-sign so ONLY the header/body mismatch is under test.
				r.Header.Set(notify.HeaderSignature, notify.Sign(cardSecret, http.MethodPost, CardActionPath, stamp, other, []byte(goodBody)))
			},
			want: http.StatusUnauthorized,
		},
		{
			name:   "non-numeric event id",
			secret: cardSecret,
			mutate: func(r *http.Request) {
				bad := "not-a-number"
				stamp := r.Header.Get(notify.HeaderTimestamp)
				payload := cardBody(bad, "approve", "admin-1", "review-1")
				r.Body = httptest.NewRequest(http.MethodPost, CardActionPath, bytes.NewReader([]byte(payload))).Body
				r.Header.Set(notify.HeaderEventID, bad)
				r.Header.Set(notify.HeaderSignature, notify.Sign(cardSecret, http.MethodPost, CardActionPath, stamp, bad, []byte(payload)))
			},
			// Correctly signed but permanently unusable: DLQ it rather than retry.
			want: http.StatusBadRequest,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := &fakeCardActionService{result: &pluginsvc.CardActionResult{Disposition: "applied", State: "approved"}}
			req := signedCardRequest(t, goodBody, now, goodEvent)
			if tt.mutate != nil {
				tt.mutate(req)
			}
			rec := httptest.NewRecorder()
			cardEngine(t, svc, tt.secret, now).ServeHTTP(rec, req)
			if rec.Code != tt.want {
				t.Fatalf("status = %d, want %d (%s)", rec.Code, tt.want, rec.Body.String())
			}
			if svc.calls != 0 {
				t.Fatal("an unverified request reached the decision service")
			}
		})
	}
}

// A transient service fault must be a retryable 5xx. Answering 200 would have
// octo-server ack the event and drop a real admin's decision forever.
func TestCardActionReturnsRetryableStatusOnFault(t *testing.T) {
	svc := &fakeCardActionService{err: errors.New("role lookup unavailable")}
	now := time.Unix(1784073600, 0)
	body := cardBody("7", "approve", "admin-1", "review-1")
	rec := httptest.NewRecorder()
	cardEngine(t, svc, cardSecret, now).ServeHTTP(rec, signedCardRequest(t, body, now, "7"))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 so the event is retried", rec.Code)
	}
}

// A well-formed but forbidden payload (operator is not a reviewer) is a HANDLED
// outcome: 200 with a typed "forbidden" refusal, so the card renders and the
// event is acked instead of retried.
func TestCardActionReportsForbiddenPayloadInBand(t *testing.T) {
	svc := &fakeCardActionService{err: pluginsvc.ErrReviewInvalid}
	now := time.Unix(1784073600, 0)
	body := cardBody("8", "approve", "member-1", "review-1")
	rec := httptest.NewRecorder()
	cardEngine(t, svc, cardSecret, now).ServeHTTP(rec, signedCardRequest(t, body, now, "8"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 with a typed refusal", rec.Code)
	}
	var out pluginsvc.CardActionResult
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Disposition != "forbidden" || out.State != "pending" {
		t.Fatalf("result = %+v", out)
	}
}

// An unrecognized decision value is a permanently malformed payload (protocol
// drift between marketplace and octo-server): the handler must return 400 so
// the event is routed to the DLQ instead of being silently acked as "forbidden".
func TestCardActionBadDecisionGoesToDLQ(t *testing.T) {
	svc := &fakeCardActionService{err: pluginsvc.ErrCardBadDecision}
	now := time.Unix(1784073600, 0)
	body := cardBody("9", "obliterate", "admin-1", "review-1")
	rec := httptest.NewRecorder()
	cardEngine(t, svc, cardSecret, now).ServeHTTP(rec, signedCardRequest(t, body, now, "9"))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 so the event hits the DLQ", rec.Code)
	}
}

// The canonical string is a cross-repo contract. Pinning octo-server's published
// vector here means a drift on either side fails a test instead of 401ing every
// callback in production.
func TestCardActionMatchesThePublishedSignatureVector(t *testing.T) {
	const (
		vectorSecret    = "0123456789abcdef0123456789abcdef" // gitleaks:allow
		vectorTimestamp = "1784073600"
		vectorEventID   = "9007199254740993"
		vectorBody      = `{"event_id":"9007199254740993","action_id":"approval-execute","decision":"execute","operator_uid":"user-b","inputs":{},"data":{"owner":"tasks","action_type":"task.execute.decision","decision":"execute","task_id":"task-1"},"message_id":"190001234567890","channel_id":"notification","channel_type":1,"space_id":"space-1","acted_at":1784073600}`
		vectorBodySHA   = "e5f9edc7558b6dbac6f754308b161d79a84e9d4635377a8afd6f95b6baa4c6cc"
		vectorSignature = "v1=77d6abe3e80bd90d70545ce90d8c87daafd65a22b62919cee71b450613d6e50f"
	)
	// The vector's path is the one this handler mounts on; if they ever diverge
	// the signature can never verify.
	if CardActionPath != "/v1/card-actions/decide" {
		t.Fatalf("CardActionPath = %q; the published vector signs /v1/card-actions/decide", CardActionPath)
	}
	sum := sha256.Sum256([]byte(vectorBody))
	if got := hex.EncodeToString(sum[:]); got != vectorBodySHA {
		t.Fatalf("body sha256 = %s, want %s", got, vectorBodySHA)
	}
	if !notify.Verify(vectorSecret, vectorSignature, http.MethodPost, CardActionPath, vectorTimestamp, vectorEventID, []byte(vectorBody)) {
		t.Fatal("the handler's verification rejects octo-server's published vector")
	}
}
