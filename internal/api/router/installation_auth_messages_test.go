package router

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	marketmiddleware "github.com/Mininglamp-OSS/octo-marketplace/internal/middleware"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	"github.com/gin-gonic/gin"
)

func TestInstallationAuthErrorsAreChineseWithoutChangingLegacy(t *testing.T) {
	identity := model.Identity{UID: "user-1", ContextIncluded: true, Spaces: []string{"space-a"}}
	for _, tc := range []struct {
		name, token, space string
		resolver           stubResolver
		status             int
		message            string
	}{
		{name: "missing_token", status: 401, message: "登录状态无效或已过期，请重新登录。"},
		{name: "expired", token: "token", resolver: stubResolver{}, status: 401, message: "登录状态无效或已过期，请重新登录。"},
		{name: "missing_space", token: "token", resolver: stubResolver{identity: identity}, status: 400, message: "请提供有效的空间标识。"},
		{name: "forbidden_space", token: "token", space: "space-b", resolver: stubResolver{identity: identity}, status: 403, message: "没有权限访问当前空间。"},
		{name: "unavailable", token: "token", resolver: stubResolver{err: errors.New("internal auth detail")}, status: 503, message: "身份认证服务暂时不可用，请稍后重试。"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := marketmiddleware.NewAuthenticator(true, tc.resolver, model.Identity{}, "")
			r := gin.New()
			v1 := r.Group("/api/v1", installationAuthMessages, a.Handler())
			for _, path := range []string{"/plugins/:plugin_id/installations", "/plugins/install"} {
				v1.POST(path, func(*gin.Context) { t.Error("rejected request reached handler") })
			}
			for _, path := range []string{"/api/v1/plugins/expert-1/installations", "/api/v1/plugins/install"} {
				req := httptest.NewRequest(http.MethodPost, path, nil)
				req.Header.Set("Token", tc.token)
				req.Header.Set("X-Space-Id", tc.space)
				rec := httptest.NewRecorder()
				r.ServeHTTP(rec, req)
				var out struct {
					Error struct {
						Message string `json:"message"`
					} `json:"error"`
				}
				if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
					t.Fatal(err)
				}
				if rec.Code != tc.status {
					t.Fatalf("code=%d want=%d", rec.Code, tc.status)
				}
				if strings.HasSuffix(path, "/installations") {
					if out.Error.Message != tc.message {
						t.Fatalf("message=%q", out.Error.Message)
					}
				} else if strings.ContainsAny(out.Error.Message, "登录权限空间服务") {
					t.Fatal("legacy auth text changed")
				}
			}
		})
	}
}
