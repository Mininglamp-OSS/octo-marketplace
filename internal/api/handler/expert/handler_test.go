package expert

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/fleet"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	expertsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/expert"
	"github.com/gin-gonic/gin"
)

// A name collision must surface as 409 with error.code "DUPLICATE" — the code
// the contract (docs/api/expert-v1.md §2) and the Swagger annotations advertise,
// and the one the rest of the platform emits for this case.
func TestWriteServiceErrorNameTakenIsDuplicate(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	writeServiceError(c, expertsvc.ErrNameTaken, "expert.create")

	if w.Code != 409 {
		t.Fatalf("status = %d, want 409", w.Code)
	}
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v (%s)", err, w.Body.String())
	}
	if body.Error.Code != "DUPLICATE" {
		t.Fatalf("error.code = %q, want DUPLICATE", body.Error.Code)
	}
}

func decodeErrCode(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v (%s)", err, w.Body.String())
	}
	return body.Error.Code
}

// An unconfigured fleet client (OCTO_FLEET_URL unset) must surface as a 503
// UPSTREAM_UNAVAILABLE, not a 500 — install depends on a downstream service.
func TestWriteInstallErrorFleetNotConfigured(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	writeInstallError(c, expertsvc.ErrFleetNotConfigured)

	if w.Code != 503 {
		t.Fatalf("status = %d, want 503", w.Code)
	}
	if code := decodeErrCode(t, w); code != "UPSTREAM_UNAVAILABLE" {
		t.Fatalf("error.code = %q, want UPSTREAM_UNAVAILABLE", code)
	}
}

// A capability 4xx (e.g. duplicate expert name) preserves the upstream status
// and maps it to the existing Marketplace error code.
func TestWriteInstallErrorCapability4xxPassthrough(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	writeInstallError(c, &fleet.CapabilityAPIError{Status: http.StatusConflict, Code: "DUPLICATE"})

	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusConflict)
	}
	if code := decodeErrCode(t, w); code != "CONFLICT" {
		t.Fatalf("error.code = %q, want CONFLICT", code)
	}
}

func TestWriteInstallErrorCapabilityForbiddenPreservesPermissionStatus(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	writeInstallError(c, &fleet.CapabilityAPIError{Status: http.StatusForbidden, Code: "FORBIDDEN"})

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusForbidden)
	}
	if code := decodeErrCode(t, w); code != "FORBIDDEN" {
		t.Fatalf("error.code = %q, want FORBIDDEN", code)
	}
}

// A Fleet capability 5xx collapses to 503 UPSTREAM_UNAVAILABLE.
func TestWriteInstallErrorCapability5xxIsUpstream(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	writeInstallError(c, &fleet.CapabilityAPIError{Status: http.StatusInternalServerError, Code: "INTERNAL_ERROR"})

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusServiceUnavailable)
	}
	if code := decodeErrCode(t, w); code != "UPSTREAM_UNAVAILABLE" {
		t.Fatalf("error.code = %q, want UPSTREAM_UNAVAILABLE", code)
	}
}

func TestWriteInstallErrorCapabilityTransportIsUpstream(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	writeInstallError(c, expertsvc.ErrFleetInstallUnavailable)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusServiceUnavailable)
	}
	if code := decodeErrCode(t, w); code != "UPSTREAM_UNAVAILABLE" {
		t.Fatalf("error.code = %q, want UPSTREAM_UNAVAILABLE", code)
	}
}

// ErrNotFound (hidden/absent expert) still maps through the shared catalog
// mapping to a 404.
func TestWriteInstallErrorNotFound(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	writeInstallError(c, expertsvc.ErrNotFound)

	if w.Code != 404 {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

type legacyInstallStore struct {
	expertsvc.Store
	expert *model.Expert
	squad  *model.Squad
}

func (s *legacyInstallStore) GetExpertByID(_ context.Context, id string) (*model.Expert, error) {
	if s.expert != nil && s.expert.ID == id {
		return s.expert, nil
	}
	return nil, expertsvc.ErrNotFound
}

func (s *legacyInstallStore) GetSquadByID(_ context.Context, id string) (*model.Squad, error) {
	if s.squad != nil && s.squad.ID == id {
		return s.squad, nil
	}
	return nil, expertsvc.ErrNotFound
}

func legacyInstallRouter(svc *expertsvc.Service) *gin.Engine {
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("marketplace.identity", model.Identity{UID: "caller", Name: "Caller"})
		c.Set("marketplace.space_id", "space-1")
		c.Next()
	})
	New(svc).Register(router.Group("/v1"))
	return router
}

func TestLegacyInstallEndpointsKeepTheirHTTPResponsesThroughCapabilityInstall(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name, path     string
		idempotencyKey string
		wantKey        string
		expert         *model.Expert
		squad          *model.Squad
		want           map[string]string
	}{
		{
			name:    "expert without caller key",
			path:    "/v1/experts/exp-1/install",
			wantKey: "install-key",
			expert: &model.Expert{
				ID: "exp-1", Name: "Code Helper", Visibility: model.VisibilityPublic, SpaceID: "space-1",
				Instruction: "Write code.",
			},
			want: map[string]string{"agent_id": "fleet-agent-1"},
		},
		{
			name:           "expert with opaque caller key",
			path:           "/v1/experts/exp-1/install",
			idempotencyKey: "marketplace/expert-install:v1+opaque_key",
			wantKey:        "marketplace/expert-install:v1+opaque_key",
			expert: &model.Expert{
				ID: "exp-1", Name: "Code Helper", Visibility: model.VisibilityPublic, SpaceID: "space-1",
				Instruction: "Write code.",
			},
			want: map[string]string{"agent_id": "fleet-agent-1"},
		},
		{
			name:    "squad without caller key",
			path:    "/v1/squads/squad-1/install",
			wantKey: "install-key",
			squad: &model.Squad{
				ID: "squad-1", Name: "Delivery Squad", Visibility: model.VisibilityPublic, SpaceID: "space-1",
				Members: []model.SquadMember{{MemberKey: "leader", Name: "Lead", IsLeader: true, Instruction: "Lead work."}},
			},
			want: map[string]string{"squad_id": "fleet-team-1", "leader_agent_id": "fleet-agent-1"},
		},
		{
			name:           "squad with opaque caller key",
			path:           "/v1/squads/squad-1/install",
			idempotencyKey: "marketplace/squad-install:v1+opaque_key",
			wantKey:        "marketplace/squad-install:v1+opaque_key",
			squad: &model.Squad{
				ID: "squad-1", Name: "Delivery Squad", Visibility: model.VisibilityPublic, SpaceID: "space-1",
				Members: []model.SquadMember{{MemberKey: "leader", Name: "Lead", IsLeader: true, Instruction: "Lead work."}},
			},
			want: map[string]string{"squad_id": "fleet-team-1", "leader_agent_id": "fleet-agent-1"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var paths []string
			var forwardedToken, forwardedSpace, forwardedWorkspace, forwardedKey string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				paths = append(paths, r.Method+" "+r.URL.Path)
				if r.Method != http.MethodPost || r.URL.Path != "/v1/capabilities/install" {
					http.NotFound(w, r)
					return
				}
				forwardedToken = r.Header.Get("Token")
				forwardedSpace = r.Header.Get("X-Space-Id")
				forwardedWorkspace = r.Header.Get("X-Workspace-Id")
				forwardedKey = r.Header.Get("Idempotency-Key")
				var request struct {
					Definition fleet.CapabilityDefinition `json:"definition"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					http.Error(w, "invalid request", http.StatusBadRequest)
					return
				}
				result := fleet.CapabilityInstallResult{Type: "expert", ExpertID: "fleet-agent-1"}
				for i, member := range request.Definition.Experts {
					id := "fleet-agent-" + strconv.Itoa(i+1)
					result.Experts = append(result.Experts, fleet.CapabilityExpertResult{ExpertID: id, Name: member.Name})
				}
				if request.Definition.ExpertTeam != nil {
					result.Type = "expert_team"
					result.ExpertID = ""
					result.ExpertTeamID = "fleet-team-1"
					for _, member := range request.Definition.ExpertTeam.Members {
						if !member.IsLeader {
							continue
						}
						for i, expert := range request.Definition.Experts {
							if expert.Name == member.ExpertName {
								result.LeaderExpertID = "fleet-agent-" + strconv.Itoa(i+1)
							}
						}
					}
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{"data": result})
			}))
			defer server.Close()

			store := &legacyInstallStore{expert: tc.expert, squad: tc.squad}
			svc := expertsvc.New(store, nil, func() string { return "install-key" }).WithFleet(fleet.New(server.URL))
			router := legacyInstallRouter(svc)
			body := strings.NewReader(`{"workspace_id":"workspace-1","runtime_id":"runtime-1"}`)
			request := httptest.NewRequest(http.MethodPost, tc.path, body)
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Authorization", "Bearer legacy-caller-token")
			if tc.idempotencyKey != "" {
				request.Header.Set("Idempotency-Key", tc.idempotencyKey)
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)

			if response.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200: %s", response.Code, response.Body.String())
			}
			if len(paths) != 1 || paths[0] != "POST /v1/capabilities/install" {
				t.Fatalf("Fleet calls = %v, want one capability transaction", paths)
			}
			if forwardedToken != "legacy-caller-token" || forwardedSpace != "space-1" || forwardedWorkspace != "workspace-1" {
				t.Fatalf("Fleet authorization/target = token:%q space:%q workspace:%q", forwardedToken, forwardedSpace, forwardedWorkspace)
			}
			if forwardedKey != tc.wantKey {
				t.Fatalf("Fleet Idempotency-Key = %q, want %q", forwardedKey, tc.wantKey)
			}
			var responseBody struct {
				Data map[string]string `json:"data"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &responseBody); err != nil {
				t.Fatalf("decode legacy response: %v (%s)", err, response.Body.String())
			}
			for field, want := range tc.want {
				if responseBody.Data[field] != want {
					t.Fatalf("response data.%s = %q, want %q (full response %s)", field, responseBody.Data[field], want, response.Body.String())
				}
			}
		})
	}
}

func TestLegacyVersionedWriteGuardPreservesAuthAndReturnsUpgradeHint(t *testing.T) {
	svc := expertsvc.New(nil, nil, nil).WithLegacyWritesDisabled(true)
	for _, path := range []struct{ method, url string }{{"POST", "/v1/experts"}, {"PATCH", "/v1/experts/id"}, {"POST", "/v1/squads"}, {"PATCH", "/v1/squads/id"}} {
		r := legacyInstallRouter(svc)
		w := httptest.NewRecorder()
		req := httptest.NewRequest(path.method, path.url, strings.NewReader(`{}`))
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)
		if w.Code != 400 || !strings.Contains(w.Body.String(), "versioned_api_required") || !strings.Contains(w.Body.String(), "Update octo-cli") {
			t.Fatalf("%s %s: %d %s", path.method, path.url, w.Code, w.Body.String())
		}
		unauth := gin.New()
		New(svc).Register(unauth.Group("/v1"))
		w = httptest.NewRecorder()
		unauth.ServeHTTP(w, httptest.NewRequest(path.method, path.url, strings.NewReader(`{}`)))
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("unauthenticated call: %d", w.Code)
		}
	}
}
