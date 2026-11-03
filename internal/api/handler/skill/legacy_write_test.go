package skill

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/middleware"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	skillsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/skill"
	"github.com/gin-gonic/gin"
)

func TestLegacySkillWriteRoutesRequireVersionedAPIs(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, identity := range []bool{true, false} {
		for _, route := range []struct{ method, path string }{
			{http.MethodPost, "/api/v1/skills"},
			{http.MethodPatch, "/api/v1/skills/foreign-skill"},
			{http.MethodPost, "/api/v1/skill"},
			{http.MethodPut, "/api/v1/skill/foreign-skill"},
		} {
			t.Run(route.method+route.path+"/authenticated="+map[bool]string{true: "yes", false: "no"}[identity], func(t *testing.T) {
				r := gin.New()
				v1 := r.Group("/api/v1")
				if identity {
					auth := middleware.NewAuthenticator(false, nil, model.Identity{UID: "owner", Name: "Owner"}, "other-space")
					v1.Use(auth.Handler())
				}
				New(skillsvc.New(nil, nil, nil, nil).WithLegacyWritesDisabled(true)).Register(v1)
				w := httptest.NewRecorder()
				// The route is refused even before parsing a body or reading a foreign ID.
				r.ServeHTTP(w, httptest.NewRequest(route.method, route.path, strings.NewReader("{")))
				if !identity {
					if w.Code != http.StatusUnauthorized {
						t.Fatalf("unauthenticated: %d %s", w.Code, w.Body.String())
					}
					return
				}
				var body struct {
					Error struct {
						Code    string `json:"code"`
						Details struct {
							Reason string `json:"reason"`
						} `json:"details"`
						Hint string `json:"hint"`
					} `json:"error"`
				}
				if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
					t.Fatal(err)
				}
				if w.Code != http.StatusBadRequest || body.Error.Code != "VALIDATION_ERROR" || body.Error.Details.Reason != "versioned_api_required" || !strings.Contains(body.Error.Hint, "Update octo-cli") || !strings.Contains(body.Error.Hint, "/plugins/publish") {
					t.Fatalf("rejection: %d %s", w.Code, w.Body.String())
				}
			})
		}
	}
}
