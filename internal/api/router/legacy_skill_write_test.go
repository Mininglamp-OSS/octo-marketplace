package router

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestVersionManagementFlagReachesLegacySkillWriters(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "off", true: "on"}[enabled], func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			cfg := testStorageConfig()
			cfg.LocalDir = t.TempDir()
			cfg.PluginVersionManagementEnabled = enabled
			parseCfg := testParseConfig()
			parseCfg.DevBotMode = true
			r := Public(db, testAuthenticator(), testAdminAuthenticator(), cfg, nil, nil, parseCfg, nil, ReviewConfig{})
			for _, route := range []struct{ method, path string }{
				{http.MethodPost, "/api/v1/bot/skills/publish"},
				{http.MethodPost, "/api/v1/skills"},
				{http.MethodPatch, "/api/v1/skills/foreign-skill"},
				{http.MethodPost, "/api/v1/skill"},
				{http.MethodPut, "/api/v1/skill/foreign-skill"},
			} {
				// Off reaches the old JSON validation, on returns the migration
				// hint before decoding; neither path should touch SQL or storage.
				req := httptest.NewRequest(route.method, route.path, strings.NewReader("{"))
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("Authorization", "Bearer bf_test")
				w := httptest.NewRecorder()
				r.ServeHTTP(w, req)
				if w.Code != http.StatusBadRequest || strings.Contains(w.Body.String(), "versioned_api_required") != enabled {
					t.Fatalf("%s %s: %d %s", route.method, route.path, w.Code, w.Body.String())
				}
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
