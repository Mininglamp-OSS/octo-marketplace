package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	pluginhandler "github.com/Mininglamp-OSS/octo-marketplace/internal/api/handler/plugin"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/fleet"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/logging"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/middleware"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	pluginrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/plugin"
	pluginsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/plugin"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

type installHTTPReply struct {
	status           int
	body, retryAfter string
	disconnect       bool
}

func serveInstallReply(t *testing.T, w http.ResponseWriter, r *http.Request, reply installHTTPReply) {
	t.Helper()
	if r.URL.Path != "/v1/capabilities/install" || r.Header.Get("Idempotency-Key") == "" {
		t.Errorf("unexpected Fleet request: path=%s key-present=%t", r.URL.Path, r.Header.Get("Idempotency-Key") != "")
	}
	if reply.disconnect {
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		_ = conn.Close()
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if reply.retryAfter != "" {
		w.Header().Set("Retry-After", reply.retryAfter)
	}
	w.WriteHeader(reply.status)
	_, _ = w.Write([]byte(reply.body))
}

func installationHTTPFixture(t *testing.T, pinned bool, installer pluginsvc.CapabilityInstaller) (*gin.Engine, string, *sql.DB) {
	t.Helper()
	database := reviewDB(t)
	svc := pluginsvc.New(pluginrepo.New(database)).WithVersionManagement(true).WithReferences(pinned).WithPinnedInstallation(pinned).WithInstallationHMACKeys("test", map[string]string{"test": "synthetic-installation-test-key-32-bytes"}).WithCapabilityInstaller(installer)
	root, err := svc.Create(context.Background(), managedOwner(), managedWrite("1.0.0", "released instructions", model.PluginVisibilityPrivate))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.Publish(context.Background(), managedOwner(), pluginsvc.PublishParams{PluginID: root.Plugin.ID}); err != nil {
		t.Fatal(err)
	}
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	auth := middleware.NewAuthenticator(false, nil, model.Identity{UID: "user-1", Name: "Owner"}, "space-a")
	pluginhandler.New(svc).Register(engine.Group("/api/v1", auth.Handler()))
	return engine, root.Plugin.ID, database
}

func installationHTTPRequest(root, route, key string) *http.Request {
	path := "/api/v1/plugins/install"
	body := map[string]string{"plugin_id": root, "workspace_id": "workspace-1", "runtime_id": "old-runtime-id"}
	if route == "new-keyed" {
		path = "/api/v1/plugins/" + root + "/installations"
		body = map[string]string{"runtime_id": "11111111-1111-4111-8111-111111111111"}
	}
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(raw)))
	req.Header.Set("Content-Type", "application/json")
	if route != "legacy-keyless" {
		req.Header.Set("Idempotency-Key", key)
	}
	if route == "new-keyed" {
		req.Header.Set("X-Workspace-ID", "workspace-1")
	}
	return req
}

// Keep the real Fleet HTTP decoder in the path: injecting a preclassified
// InstallationAttemptError into the handler misses the service regression.
func TestInstallUpstreamHTTPContracts(t *testing.T) {
	const conflictMessage = "upstream-secret: synthetic-credential"
	core, logs := observer.New(zap.DebugLevel)
	restore := logging.Replace(zap.New(core))
	defer restore()
	defer func() {
		for _, entry := range logs.All() {
			raw, err := json.Marshal(entry.ContextMap())
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(entry.Message+string(raw), "upstream-secret") {
				t.Fatal("Fleet text leaked in logs")
			}
		}
	}()
	for _, pinned := range []bool{false, true} {
		t.Run(fmt.Sprintf("pinned=%t", pinned), func(t *testing.T) {
			var current atomic.Pointer[installHTTPReply]
			var calls atomic.Int64
			remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				serveInstallReply(t, w, r, *current.Load())
			}))
			defer remote.Close()
			engine, root, _ := installationHTTPFixture(t, pinned, fleet.New(remote.URL))
			for _, tc := range []struct {
				name                              string
				reply                             installHTTPReply
				status                            int
				code, message, reason, retryAfter string
			}{
				{name: "key-duplicate", reply: installHTTPReply{status: 409, body: `{"error":{"code":"DUPLICATE","message":"` + conflictMessage + `","details":{"resource":"idempotency_key","name":"upstream-secret"}}}`}, status: 409, code: "DUPLICATE", message: "幂等键已用于其他安装参数，请核对原安装记录。", reason: "idempotency_key_reused"},
				{name: "skill-duplicate", reply: installHTTPReply{status: 409, body: `{"error":{"code":"DUPLICATE","message":"` + conflictMessage + `","details":{"resource":"skill","name":"upstream-secret"}}}`}, status: 409, code: "DUPLICATE", message: "安装发生技能资源冲突，请核对工作区已有资源。"},
				{name: "expert-duplicate", reply: installHTTPReply{status: 409, body: `{"error":{"code":"DUPLICATE","message":"` + conflictMessage + `","details":{"resource":"expert"}}}`}, status: 409, code: "DUPLICATE", message: "安装发生专家资源冲突，请核对工作区已有资源。"},
				{name: "skill-content-conflict", reply: installHTTPReply{status: 409, body: `{"error":{"code":"CONFLICT","message":"` + conflictMessage + `","details":{"resource":"skill","name":"upstream-secret"}}}`}, status: 409, code: "CONFLICT", message: "安装发生技能资源冲突，请核对工作区已有资源。"},
				{name: "expert-content-conflict", reply: installHTTPReply{status: 409, body: `{"error":{"code":"CONFLICT","message":"` + conflictMessage + `","details":{"resource":"expert"}}}`}, status: 409, code: "CONFLICT", message: "安装发生专家资源冲突，请核对工作区已有资源。"},
				{name: "team-content-conflict", reply: installHTTPReply{status: 409, body: `{"error":{"code":"CONFLICT","message":"` + conflictMessage + `","details":{"resource":"expert_team"}}}`}, status: 409, code: "CONFLICT", message: "安装发生专家团资源冲突，请核对工作区已有资源。"},
				{name: "legacy-key-conflict", reply: installHTTPReply{status: 409, body: `{"error":{"code":"IDEMPOTENCY_KEY_REUSED","message":"` + conflictMessage + `"}}`}, status: 409, code: "CONFLICT", message: "幂等键已用于其他安装参数，请核对原安装记录。", reason: "idempotency_key_reused"},
				{name: "duplicate-blank-message", reply: installHTTPReply{status: 409, body: `{"error":{"code":"DUPLICATE","message":"   "}}`}, status: 409, code: "DUPLICATE", message: "安装发生冲突，请检查已有资源或安装参数。"},
				{name: "rate-limited", reply: installHTTPReply{status: 429, body: `{"error":{"code":"RATE_LIMITED","message":"upstream-secret"}}`, retryAfter: "7"}, status: 429, code: "RATE_LIMITED", retryAfter: "7"},
				{name: "rate-limited-invalid-delay", reply: installHTTPReply{status: 429, body: `{"error":{"code":"RATE_LIMITED"}}`, retryAfter: "invalid"}, status: 429, code: "RATE_LIMITED"},
				{name: "rate-limited-negative-delay", reply: installHTTPReply{status: 429, body: `{"error":{"code":"RATE_LIMITED"}}`, retryAfter: "-1"}, status: 429, code: "RATE_LIMITED"},
				{name: "in-progress", reply: installHTTPReply{status: 409, body: `{"error":{"code":"INSTALLATION_IN_PROGRESS","message":"upstream-secret"}}`}, status: 503, code: "UPSTREAM_UNAVAILABLE"},
				{name: "generic-conflict", reply: installHTTPReply{status: 409, body: `{"error":{"code":"CONFLICT","message":"upstream-secret"}}`}, status: 503, code: "UPSTREAM_UNAVAILABLE"},
				{name: "unrecognized-resource-conflict", reply: installHTTPReply{status: 409, body: `{"error":{"code":"CONFLICT","message":"upstream-secret","details":{"resource":"unknown"}}}`}, status: 503, code: "UPSTREAM_UNAVAILABLE"},
				{name: "key-in-progress-conflict", reply: installHTTPReply{status: 409, body: `{"error":{"code":"CONFLICT","message":"upstream-secret","details":{"resource":"idempotency_key"}}}`}, status: 503, code: "UPSTREAM_UNAVAILABLE"},
				{name: "resource-in-progress", reply: installHTTPReply{status: 409, body: `{"error":{"code":"INSTALLATION_IN_PROGRESS","details":{"resource":"skill"}}}`}, status: 503, code: "UPSTREAM_UNAVAILABLE"},
				{name: "undecodable-conflict", reply: installHTTPReply{status: 409, body: `not-json`}, status: 503, code: "UPSTREAM_UNAVAILABLE"},
				{name: "connection-lost", reply: installHTTPReply{disconnect: true}, status: 503, code: "UPSTREAM_UNAVAILABLE"},
				{name: "timeout", reply: installHTTPReply{status: 408, body: `{"error":{"code":"TIMEOUT"}}`}, status: 503, code: "UPSTREAM_UNAVAILABLE"},
				{name: "empty-result", reply: installHTTPReply{status: 200, body: `{"data":null}`}, status: 503, code: "UPSTREAM_UNAVAILABLE"},
				{name: "server-error", reply: installHTTPReply{status: 500, body: `{"error":{"code":"INTERNAL_ERROR","message":"upstream-secret"}}`}, status: 503, code: "UPSTREAM_UNAVAILABLE"},
			} {
				for _, route := range []string{"legacy-keyless", "legacy-keyed", "new-keyed"} {
					t.Run(tc.name+"/"+route, func(t *testing.T) {
						reply := tc.reply
						current.Store(&reply)
						before := calls.Load()
						rec := httptest.NewRecorder()
						engine.ServeHTTP(rec, installationHTTPRequest(root, route, tc.name+"-"+route))
						var response struct {
							Error struct {
								Code, Message, Hint string
								Details             map[string]any
							}
						}
						if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
							t.Fatal(err)
						}
						if rec.Code != tc.status || response.Error.Code != tc.code || calls.Load() != before+1 {
							t.Fatalf("status=%d calls=%d body=%s", rec.Code, calls.Load()-before, rec.Body.String())
						}
						if tc.message != "" && response.Error.Message != tc.message {
							t.Fatalf("conflict message changed: %q", response.Error.Message)
						}
						if rec.Header().Get("Retry-After") != tc.retryAfter {
							t.Fatalf("Retry-After=%q", rec.Header().Get("Retry-After"))
						}
						if strings.Contains(rec.Body.String(), "upstream-secret") {
							t.Fatal("raw upstream detail/message leaked")
						}
						if tc.reason != "" && response.Error.Details["conflict_reason"] != tc.reason {
							t.Fatalf("conflict reason lost: %s", rec.Body.String())
						}
						if tc.status == 503 {
							if response.Error.Details["reason"] != "installation_result_unknown" || response.Error.Details["can_retry_same_key"] != (route != "legacy-keyless") {
								t.Fatalf("uncertain recovery contract lost: %s", rec.Body.String())
							}
							if route == "legacy-keyless" && !strings.Contains(response.Error.Hint, "先核对工作区已有资源") {
								t.Fatal("keyless uncertainty warning lost")
							}
						} else if response.Error.Details["reason"] == "installation_result_unknown" {
							t.Fatal("definite rejection marked unknown")
						}
						if tc.status == 409 && strings.Contains(response.Error.Hint, "使用相同的幂等键和安装参数重试") {
							t.Fatal("same-key retry cannot fix a definite content conflict")
						}
					})
				}
			}
		})
	}
}

// A definite later rejection describes that send only; it cannot disprove an
// earlier remote commit whose response was lost. Keep the persisted unknown latch.
func TestInstallHTTPRejectionPreservesEarlierUnknown(t *testing.T) {
	for _, status := range []int{409, 429} {
		for _, route := range []string{"legacy-keyed", "new-keyed"} {
			t.Run(fmt.Sprintf("status=%d/%s", status, route), func(t *testing.T) {
				var calls atomic.Int64
				remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					reply := installHTTPReply{disconnect: true}
					if calls.Add(1) > 1 {
						reply = installHTTPReply{status: status, body: `{"error":{"code":"DUPLICATE","details":{"resource":"idempotency_key"}}}`}
						if status == 429 {
							reply.body = `{"error":{"code":"RATE_LIMITED"}}`
							reply.retryAfter = "7"
						}
					}
					serveInstallReply(t, w, r, reply)
				}))
				defer remote.Close()
				engine, root, database := installationHTTPFixture(t, true, fleet.New(remote.URL))
				for i := 0; i < 2; i++ {
					rec := httptest.NewRecorder()
					engine.ServeHTTP(rec, installationHTTPRequest(root, route, "same-original-key"))
					var response struct {
						Error struct{ Details map[string]any }
					}
					if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
						t.Fatal(err)
					}
					if rec.Code != 503 || response.Error.Details["reason"] != "installation_result_unknown" || response.Error.Details["can_retry_same_key"] != true {
						t.Fatalf("earlier uncertainty erased: %s", rec.Body.String())
					}
				}
				var state string
				var hadUnknown bool
				if err := database.QueryRow(`SELECT attempt_state,had_unknown FROM plugin_install_attempts WHERE space_id='space-a' AND caller_uid='user-1' AND workspace_id='workspace-1'`).Scan(&state, &hadUnknown); err != nil {
					t.Fatal(err)
				}
				if state != "UNKNOWN" || !hadUnknown || calls.Load() != 2 {
					t.Fatalf("state=%s had_unknown=%t calls=%d", state, hadUnknown, calls.Load())
				}
			})
		}
	}
}

func TestInstallHTTPRateLimitSameKeyRetry(t *testing.T) {
	for _, pinned := range []bool{false, true} {
		for _, route := range []string{"legacy-keyed", "new-keyed"} {
			t.Run(fmt.Sprintf("pinned=%t/%s", pinned, route), func(t *testing.T) {
				type sentRequest struct{ key, body string }
				var first atomic.Pointer[sentRequest]
				var calls atomic.Int64
				remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					raw, err := io.ReadAll(r.Body)
					if err != nil {
						t.Error(err)
					}
					sent := &sentRequest{key: r.Header.Get("Idempotency-Key"), body: string(raw)}
					if calls.Add(1) == 1 {
						first.Store(sent)
					} else if *sent != *first.Load() {
						t.Error("rate-limit retry changed the Fleet key or frozen request")
					}
					serveInstallReply(t, w, r, installHTTPReply{status: 429, body: `{"error":{"code":"RATE_LIMITED"}}`, retryAfter: "7"})
				}))
				defer remote.Close()
				engine, root, database := installationHTTPFixture(t, pinned, fleet.New(remote.URL))
				var originalDeadline time.Time
				for i := 0; i < 2; i++ {
					rec := httptest.NewRecorder()
					engine.ServeHTTP(rec, installationHTTPRequest(root, route, "same-rate-limit-key"))
					if rec.Code != 429 || rec.Header().Get("Retry-After") != "7" {
						t.Fatalf("same-key rate-limit retry blocked: status=%d body=%s", rec.Code, rec.Body.String())
					}
					if pinned {
						var deadline time.Time
						var hadUnknown bool
						if err := database.QueryRow(`SELECT retry_until,had_unknown FROM plugin_install_attempts WHERE space_id='space-a' AND caller_uid='user-1' AND workspace_id='workspace-1'`).Scan(&deadline, &hadUnknown); err != nil {
							t.Fatal(err)
						}
						if hadUnknown {
							t.Fatal("a definite rate limit invented earlier uncertainty")
						}
						if i == 0 {
							originalDeadline = deadline
						} else if !deadline.Equal(originalDeadline) {
							t.Fatal("rate-limit retry extended the original deadline")
						}
					}
				}
				if calls.Load() != 2 {
					t.Fatalf("calls=%d", calls.Load())
				}
			})
		}
	}
}
