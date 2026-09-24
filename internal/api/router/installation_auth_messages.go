package router

import (
	"net/http"

	marketmiddleware "github.com/Mininglamp-OSS/octo-marketplace/internal/middleware"
	"github.com/gin-gonic/gin"
)

// Authentication can abort before the plugin handler runs. Limit the requested
// Chinese display contract to the new installation route; legacy text is intact.
func installationAuthMessages(c *gin.Context) {
	if c.Request.Method == http.MethodPost && c.FullPath() == "/api/v1/plugins/:plugin_id/installations" {
		marketmiddleware.SetAuthErrorMessages(c, map[string]string{
			"AUTH_REQUIRED":        "登录状态无效或已过期，请重新登录。",
			"FORBIDDEN":            "没有权限访问当前空间。",
			"VALIDATION_ERROR":     "请提供有效的空间标识。",
			"UPSTREAM_UNAVAILABLE": "身份认证服务暂时不可用，请稍后重试。",
		})
	}
	c.Next()
}
