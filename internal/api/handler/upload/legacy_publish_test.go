package upload

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/middleware"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/service/parse"
	skillsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/skill"
	"github.com/gin-gonic/gin"
)

func TestManagedBotSkillPublishRefusesBeforeParse(t *testing.T) {
	for _, tc := range []struct {
		name          string
		identity, bot bool
		body          string
		status        int
	}{
		{"bot", true, true, `{"skill_upload_id":"upload"}`, http.StatusBadRequest},
		{"malformed", true, true, `{`, http.StatusBadRequest},
		{"no identity", false, false, `{}`, http.StatusUnauthorized},
		{"not a bot", true, false, `{}`, http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := gin.New()
			v1 := r.Group("/api/v1")
			if tc.identity {
				auth := middleware.NewAuthenticator(false, nil, model.Identity{UID: "owner", Name: "Owner"}, "space")
				v1.Use(auth.Handler())
			}
			// An uninitialized parser would panic if invoked: rejecting the old
			// endpoint must not start parsing, consume tasks, or access storage/DB.
			h := New(&parse.Service{}, skillsvc.New(nil, nil, nil, nil).WithLegacyWritesDisabled(true), nil)
			h.SetDevBotMode(true)
			h.Register(v1)
			req := httptest.NewRequest(http.MethodPost, "/api/v1/bot/skills/publish", strings.NewReader(tc.body))
			if tc.bot {
				req.Header.Set("Authorization", "Bearer bf_test")
			}
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if w.Code != tc.status {
				t.Fatalf("status: %d %s", w.Code, w.Body.String())
			}
			if tc.status != http.StatusBadRequest {
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
			if body.Error.Code != "VALIDATION_ERROR" || body.Error.Details.Reason != "versioned_api_required" || !strings.Contains(body.Error.Hint, "Update octo-cli") || !strings.Contains(body.Error.Hint, "/plugins/import") || !strings.Contains(body.Error.Hint, "/plugins/publish") {
				t.Fatalf("body: %s", w.Body.String())
			}
		})
	}
}
