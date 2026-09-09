package notify

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func testClient(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return New(srv.URL, "test-token", 2*time.Second)
}

func TestEnabled(t *testing.T) {
	cases := []struct {
		name, baseURL, token string
		want                 bool
	}{
		{"both set", "http://octo.example", "tok", true},
		{"no token", "http://octo.example", "", false},
		{"no base url", "", "tok", false},
		{"neither", "", "", false},
		{"whitespace only", "   ", "  ", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := New(tc.baseURL, tc.token, time.Second).Enabled(); got != tc.want {
				t.Fatalf("Enabled() = %v, want %v", got, tc.want)
			}
		})
	}
	var nilClient *Client
	if nilClient.Enabled() {
		t.Fatal("nil client must report disabled")
	}
}

// A disabled client must fail fast rather than dialing an empty base URL.
func TestDisabledClient_FailsFastWithoutDialing(t *testing.T) {
	c := New("", "", time.Second)
	if _, err := c.MemberRole(context.Background(), "sp", "u"); err == nil {
		t.Fatal("MemberRole on a disabled client must error")
	} else if !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("unclear disabled error: %v", err)
	}
	_, err := c.NotifySpaceAdmins(context.Background(), NotifyRequest{
		SpaceID:      "sp",
		ApprovalCard: &ApprovalCard{ActionType: "a"},
	})
	if err == nil {
		t.Fatal("NotifySpaceAdmins on a disabled client must error")
	}
	if !errors.Is(err, errDisabled) {
		t.Fatalf("want errDisabled, got %v", err)
	}
}

func TestMemberRole_Roles(t *testing.T) {
	// role 0 is a REAL role and must survive as *int(0), not be flattened into
	// "absent" the way a plain int would.
	cases := []struct {
		name string
		body string
		want *int
	}{
		{"member role 0", `{"data":{"role":0}}`, intPtr(RoleMember)},
		{"admin role 1", `{"data":{"role":1}}`, intPtr(RoleAdmin)},
		{"owner role 2", `{"data":{"role":2}}`, intPtr(RoleOwner)},
		{"explicit null", `{"data":{"role":null}}`, nil},
		{"absent role key", `{"data":{}}`, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					t.Errorf("method = %s, want GET", r.Method)
				}
				if got := r.URL.Path; got != "/v1/internal/spaces/sp_1/members/u_1/role" {
					t.Errorf("path = %s", got)
				}
				if got := r.Header.Get("X-Internal-Token"); got != "test-token" {
					t.Errorf("X-Internal-Token = %q", got)
				}
				_, _ = w.Write([]byte(tc.body))
			})
			got, err := c.MemberRole(context.Background(), "sp_1", "u_1")
			if err != nil {
				t.Fatalf("MemberRole: %v", err)
			}
			switch {
			case tc.want == nil && got != nil:
				t.Fatalf("role = %d, want nil", *got)
			case tc.want != nil && got == nil:
				t.Fatalf("role = nil, want %d", *tc.want)
			case tc.want != nil && *got != *tc.want:
				t.Fatalf("role = %d, want %d", *got, *tc.want)
			}
		})
	}
}

// "role 0" and "role null" must be distinguishable at the Go type level: this
// is exactly what a plain int return would have lost.
func TestMemberRole_ZeroIsNotNull(t *testing.T) {
	zero := decodeRole(t, `{"data":{"role":0}}`)
	null := decodeRole(t, `{"data":{"role":null}}`)
	if zero == nil {
		t.Fatal("role 0 decoded as nil")
	}
	if null != nil {
		t.Fatalf("role null decoded as %d", *null)
	}
}

func decodeRole(t *testing.T, body string) *int {
	t.Helper()
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	})
	got, err := c.MemberRole(context.Background(), "sp", "u")
	if err != nil {
		t.Fatalf("MemberRole: %v", err)
	}
	return got
}

func TestMemberRole_ErrorStatuses(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		code   string
	}{
		{"400 invalid param", http.StatusBadRequest, `{"error":{"code":"err.shared.param.invalid","message":"bad space_id"}}`, "err.shared.param.invalid"},
		{"401 token invalid", http.StatusUnauthorized, `{"error":{"code":"err.shared.auth.token_invalid","message":"nope"}}`, "err.shared.auth.token_invalid"},
		{"500 internal", http.StatusInternalServerError, `{"error":{"code":"err.shared.internal","message":"boom"}}`, "err.shared.internal"},
		{"non-envelope body", http.StatusBadGateway, `gateway down`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			})
			role, err := c.MemberRole(context.Background(), "sp", "u")
			if err == nil {
				t.Fatal("expected an error")
			}
			if role != nil {
				t.Fatal("role must be nil on error")
			}
			var apiErr *APIError
			if !errors.As(err, &apiErr) {
				t.Fatalf("want *APIError, got %T: %v", err, err)
			}
			if apiErr.Status != tc.status {
				t.Fatalf("Status = %d, want %d", apiErr.Status, tc.status)
			}
			if apiErr.Code != tc.code {
				t.Fatalf("Code = %q, want %q", apiErr.Code, tc.code)
			}
		})
	}
}

func TestMemberRole_MalformedBody(t *testing.T) {
	for _, body := range []string{`not json`, `{}`, `{"data":null}`} {
		c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(body))
		})
		if _, err := c.MemberRole(context.Background(), "sp", "u"); err == nil {
			t.Fatalf("body %q must not decode as a valid role response", body)
		}
	}
}

func TestMemberRole_RequiresIdentifiers(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("must not reach the server")
	})
	if _, err := c.MemberRole(context.Background(), "", "u"); err == nil {
		t.Fatal("empty space_id must error")
	}
	if _, err := c.MemberRole(context.Background(), "sp", "  "); err == nil {
		t.Fatal("blank uid must error")
	}
}

// Path segments are escaped so a hostile id cannot climb the URL into another
// internal route.
func TestMemberRole_EscapesPathSegments(t *testing.T) {
	var gotPath string
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.EscapedPath()
		_, _ = w.Write([]byte(`{"data":{"role":null}}`))
	})
	if _, err := c.MemberRole(context.Background(), "sp/../../admins", "u"); err != nil {
		t.Fatalf("MemberRole: %v", err)
	}
	if strings.Contains(gotPath, "/../") {
		t.Fatalf("unescaped traversal reached the wire: %s", gotPath)
	}
}

func TestNotifySpaceAdmins_SendsTargetRoleAndNeverTargets(t *testing.T) {
	var raw map[string]json.RawMessage
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/v1/internal/notify" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q", got)
		}
		if got := r.Header.Get("X-Internal-Token"); got != "test-token" {
			t.Errorf("X-Internal-Token = %q", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
			t.Errorf("decode body: %v", err)
		}
		_, _ = w.Write([]byte(`{"data":{"delivered":["u1","u2"],"filtered":{"u3":"send_failed"}}}`))
	})

	resp, err := c.NotifySpaceAdmins(context.Background(), NotifyRequest{
		SpaceID:  "sp_1",
		ActorUID: "u_applicant",
		ApprovalCard: &ApprovalCard{
			ActionType:  "marketplace.plugin_review.decision",
			Title:       "插件上架申请 · demo",
			Description: "类型:技能",
			Data:        map[string]string{"review_id": "rv_1", "plugin_id": "pl_1"},
		},
	})
	if err != nil {
		t.Fatalf("NotifySpaceAdmins: %v", err)
	}

	// The deleted roster endpoint must never come back through this body.
	if _, ok := raw["targets"]; ok {
		t.Fatal("request body carried `targets`; octo-server 400s on both, and a roster must never leave marketplace")
	}
	var role string
	if err := json.Unmarshal(raw["target_role"], &role); err != nil {
		t.Fatalf("target_role missing/unreadable: %v", err)
	}
	if role != "space_admin" {
		t.Fatalf("target_role = %q, want space_admin", role)
	}
	assertJSONString(t, raw, "space_id", "sp_1")
	// Service defaults to "marketplace" when the caller leaves it blank.
	assertJSONString(t, raw, "service", "marketplace")
	assertJSONString(t, raw, "actor_uid", "u_applicant")

	var card map[string]any
	if err := json.Unmarshal(raw["approval_card"], &card); err != nil {
		t.Fatalf("approval_card: %v", err)
	}
	if card["action_type"] != "marketplace.plugin_review.decision" {
		t.Fatalf("action_type = %v", card["action_type"])
	}
	// Omitting `actions` is what makes octo-server render its styled default
	// approve/deny buttons.
	if _, ok := card["actions"]; ok {
		t.Fatal("approval_card must not carry `actions`")
	}

	if len(resp.Delivered) != 2 || resp.Delivered[0] != "u1" || resp.Delivered[1] != "u2" {
		t.Fatalf("delivered = %v", resp.Delivered)
	}
	if resp.Filtered["u3"] != "send_failed" {
		t.Fatalf("filtered = %v", resp.Filtered)
	}
}

func TestNotifySpaceAdmins_ExplicitServiceIsPreserved(t *testing.T) {
	var raw map[string]json.RawMessage
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&raw)
		_, _ = w.Write([]byte(`{"data":{"delivered":[],"filtered":{}}}`))
	})
	_, err := c.NotifySpaceAdmins(context.Background(), NotifyRequest{
		SpaceID:      "sp_1",
		Service:      "marketplace-review",
		ApprovalCard: &ApprovalCard{ActionType: "a"},
	})
	if err != nil {
		t.Fatalf("NotifySpaceAdmins: %v", err)
	}
	assertJSONString(t, raw, "service", "marketplace-review")
}

// Zero admins is a SUCCESS with an empty delivered list, not an error, and the
// caller must be able to tell it apart from "everyone was filtered".
func TestNotifySpaceAdmins_ZeroAdminsIsSuccess(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"delivered":[],"filtered":{}}}`))
	})
	resp, err := c.NotifySpaceAdmins(context.Background(), NotifyRequest{
		SpaceID:      "sp_1",
		ApprovalCard: &ApprovalCard{ActionType: "a"},
	})
	if err != nil {
		t.Fatalf("zero admins must not be an error: %v", err)
	}
	if len(resp.Delivered) != 0 || len(resp.Filtered) != 0 {
		t.Fatalf("resp = %+v", resp)
	}
}

// Absent delivered/filtered keys normalize to non-nil empties so callers can
// range/index without a nil check.
func TestNotifySpaceAdmins_NormalizesAbsentFields(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{}}`))
	})
	resp, err := c.NotifySpaceAdmins(context.Background(), NotifyRequest{
		SpaceID:      "sp_1",
		ApprovalCard: &ApprovalCard{ActionType: "a"},
	})
	if err != nil {
		t.Fatalf("NotifySpaceAdmins: %v", err)
	}
	if resp.Delivered == nil || resp.Filtered == nil {
		t.Fatalf("nil collections leaked to the caller: %+v", resp)
	}
}

func TestNotifySpaceAdmins_ErrorStatuses(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusInternalServerError} {
		c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"error":{"code":"err.shared.param.invalid","message":"x"}}`))
		})
		resp, err := c.NotifySpaceAdmins(context.Background(), NotifyRequest{
			SpaceID:      "sp_1",
			ApprovalCard: &ApprovalCard{ActionType: "a"},
		})
		if resp != nil {
			t.Fatalf("status %d returned a partial report", status)
		}
		var apiErr *APIError
		if !errors.As(err, &apiErr) || apiErr.Status != status {
			t.Fatalf("status %d: want *APIError with that status, got %v", status, err)
		}
	}
}

func TestNotifySpaceAdmins_Validation(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("must not reach the server")
	})
	cases := []struct {
		name string
		req  NotifyRequest
	}{
		{"missing space_id", NotifyRequest{ApprovalCard: &ApprovalCard{ActionType: "a"}}},
		{"blank space_id", NotifyRequest{SpaceID: "  ", ApprovalCard: &ApprovalCard{ActionType: "a"}}},
		{"nil card", NotifyRequest{SpaceID: "sp"}},
		{"missing action_type", NotifyRequest{SpaceID: "sp", ApprovalCard: &ApprovalCard{}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := c.NotifySpaceAdmins(context.Background(), tc.req); err == nil {
				t.Fatal("expected a validation error")
			}
		})
	}
}

// A 3xx must surface as an error, never be followed: the internal token is a
// custom header Go would copy verbatim to the redirect target.
func TestRedirectsAreRefused(t *testing.T) {
	var leaked bool
	attacker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Internal-Token") != "" {
			leaked = true
		}
		_, _ = w.Write([]byte(`{"data":{"role":2}}`))
	}))
	defer attacker.Close()

	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, attacker.URL+r.URL.Path, http.StatusFound)
	})

	role, err := c.MemberRole(context.Background(), "sp", "u")
	if err == nil {
		t.Fatalf("redirect was followed, role = %v", role)
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusFound {
		t.Fatalf("want *APIError 302, got %v", err)
	}
	if leaked {
		t.Fatal("internal token was forwarded to the redirect target")
	}

	resp, err := c.NotifySpaceAdmins(context.Background(), NotifyRequest{
		SpaceID:      "sp",
		ApprovalCard: &ApprovalCard{ActionType: "a"},
	})
	if err == nil {
		t.Fatalf("redirect was followed on notify, resp = %+v", resp)
	}
	if leaked {
		t.Fatal("internal token was forwarded to the redirect target")
	}
}

func TestContextCancellationIsHonored(t *testing.T) {
	release := make(chan struct{})
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		<-release
	})
	defer close(release)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.MemberRole(ctx, "sp", "u"); err == nil {
		t.Fatal("expected a context error")
	}
}

func TestAPIErrorMessage(t *testing.T) {
	cases := []struct {
		err  *APIError
		want string
	}{
		{&APIError{Status: 401, Code: "err.shared.auth.token_invalid", Message: "bad"}, "notify: status 401: err.shared.auth.token_invalid: bad"},
		{&APIError{Status: 400, Code: "err.shared.param.invalid"}, "notify: status 400: err.shared.param.invalid"},
		{&APIError{Status: 502, Message: "gateway"}, "notify: status 502: gateway"},
		{&APIError{Status: 503}, "notify: status 503"},
	}
	for _, tc := range cases {
		if got := tc.err.Error(); got != tc.want {
			t.Fatalf("Error() = %q, want %q", got, tc.want)
		}
	}
}

func TestTruncateRunes_CutsOnRuneBoundaries(t *testing.T) {
	got := truncateRunes("一二三四五", 2)
	if got != "一二..." {
		t.Fatalf("truncateRunes = %q", got)
	}
	if truncateRunes("abc", 10) != "abc" {
		t.Fatal("short strings must pass through unchanged")
	}
	if truncateRunes("abc", 0) != "" {
		t.Fatal("non-positive budget must yield an empty string")
	}
}

func assertJSONString(t *testing.T, raw map[string]json.RawMessage, key, want string) {
	t.Helper()
	var got string
	if err := json.Unmarshal(raw[key], &got); err != nil {
		t.Fatalf("%s: %v", key, err)
	}
	if got != want {
		t.Fatalf("%s = %q, want %q", key, got, want)
	}
}

func intPtr(v int) *int { return &v }

// TestMemberRole_ClampsOutOfRangeValues is the IM-path mirror of the resolver
// test: a drifted octo-server returning the inverted octo-web encoding (3=member)
// must NOT be treated as admin here, or every plain member can approve via IM.
func TestMemberRole_ClampsOutOfRangeValues(t *testing.T) {
	cases := []struct {
		name string
		wire int // role value in {"data":{"role":<wire>}}
		want int // clamped role we expect
	}{
		{"web-encoded member 3 -> member", 3, RoleMember},
		{"large value -> member", 99, RoleMember},
		{"negative -> member", -1, RoleMember},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body, _ := json.Marshal(map[string]any{"data": map[string]int{"role": tc.wire}})
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write(body)
			})
			got, err := c.MemberRole(context.Background(), "sp", "u")
			if err != nil {
				t.Fatalf("MemberRole: %v", err)
			}
			if got == nil {
				t.Fatal("expected non-nil clamped role, got nil (would read as non-member)")
			}
			if *got != tc.want {
				t.Fatalf("role=%d, want %d — authorization boundary would leak", *got, tc.want)
			}
			// The IM path compares `*role >= RoleAdmin`; for every out-of-range
			// input this MUST be false, or the whole point is lost.
			if *got >= RoleAdmin {
				t.Fatalf("clamped role %d >= RoleAdmin — fail-closed invariant broken", *got)
			}
		})
	}
}

// TestMemberRole_ClampKeepsNullDistinct confirms we didn't break the "null means
// not a member" contract when adding the clamp: a null role must STILL come back
// as nil, not collapse to RoleMember.
func TestMemberRole_ClampKeepsNullDistinct(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"role":null}}`))
	})
	got, err := c.MemberRole(context.Background(), "sp", "u")
	if err != nil {
		t.Fatalf("MemberRole: %v", err)
	}
	if got != nil {
		t.Fatalf("null role must return nil (not a member), got %d", *got)
	}
}

// TestMemberRole_DriftOncePerBadValue guards the flood-control choice: each
// distinct bad value logs once, and the server can be hit N times without N log
// lines. Like the resolver test, we don't introspect zap; we check the once
// bookkeeping exists and fires.
func TestMemberRole_DriftOncePerBadValue(t *testing.T) {
	calls := 0
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = w.Write([]byte(`{"data":{"role":3}}`))
	})
	for i := 0; i < 50; i++ {
		role, err := c.MemberRole(context.Background(), "sp", "u")
		if err != nil {
			t.Fatalf("MemberRole[%d]: %v", i, err)
		}
		if role == nil || *role != RoleMember {
			t.Fatalf("call %d: role=%v, want %d", i, role, RoleMember)
		}
	}
	if calls != 50 {
		t.Fatalf("server calls=%d, want 50", calls)
	}
	c.driftMu.Lock()
	o := c.roleDriftOnce[3]
	c.driftMu.Unlock()
	if o == nil {
		t.Fatal("roleDriftOnce[3] was never created despite 50 responses with role=3")
	}
}

// TestMemberRole_ClampConcurrentSafe guards against a data race on roleDriftOnce
// when many card-action callbacks arrive in parallel during an upstream drift.
func TestMemberRole_ClampConcurrentSafe(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"role":3}}`))
	})
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if _, err := c.MemberRole(ctx, "sp", "u"); err != nil {
				t.Errorf("MemberRole: %v", err)
			}
		}()
	}
	wg.Wait()
}
