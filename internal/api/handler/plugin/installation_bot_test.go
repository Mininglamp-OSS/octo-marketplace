package plugin

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	marketmiddleware "github.com/Mininglamp-OSS/octo-marketplace/internal/middleware"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	"github.com/gin-gonic/gin"
)

type installationBotResolver struct{}

func (installationBotResolver) ResolveBot(context.Context, string) (model.BotIdentity, error) {
	return model.BotIdentity{BotUID: "bot-1", OwnerUID: "user-1", SpaceID: "space-a"}, nil
}

func TestInstallationRejectsAuthenticatedBotBeforeService(t *testing.T) {
	f := &fakeInstallationService{fakeService: &fakeService{}}
	r := gin.New()
	auth := marketmiddleware.NewAuthenticator(true, nil, model.Identity{}, "", installationBotResolver{})
	New(f).Register(r.Group("/api/v1", auth.Handler()))
	req := installationRequest(installationBody)
	req.Header.Set("Authorization", "Bearer bf_test")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != 403 || f.calls != 0 || !strings.Contains(rec.Body.String(), "安装专家需要使用用户身份") {
		t.Fatalf("code=%d calls=%d body=%s", rec.Code, f.calls, rec.Body.String())
	}
}
