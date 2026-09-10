package plugin

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	pluginsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/plugin"
)

func TestReviewPolicyEndpointsUseAuthenticatedSpaceAndStandardEnvelope(t *testing.T) {
	f := &fakeService{reviewPolicy: model.PluginReviewPolicy{IsAutoApproveEnabled: true}}
	identity := model.Identity{UID: "owner-1", Name: "Owner", SpaceRoles: map[string]int{"space-a": model.SpaceRoleOwner}}
	engine := engineWithIdentity(identity, f)

	get := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/plugin_review_policies", nil)
	req.Header.Set("X-Space-Id", "space-a")
	engine.ServeHTTP(get, req)
	if get.Code != http.StatusOK || !strings.Contains(get.Body.String(), `"is_auto_approve_enabled":true`) {
		t.Fatalf("GET status=%d body=%s", get.Code, get.Body.String())
	}

	patch := httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPatch, "/api/v1/plugin_review_policies", bytes.NewBufferString(`{"is_auto_approve_enabled":false}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Space-Id", "space-a")
	engine.ServeHTTP(patch, req)
	if patch.Code != http.StatusOK || f.reviewPolicySet == nil || *f.reviewPolicySet {
		t.Fatalf("PATCH status=%d body=%s value=%v", patch.Code, patch.Body.String(), f.reviewPolicySet)
	}
	if f.caller.SpaceID != "space-a" || f.caller.SpaceRole != 2 {
		t.Fatalf("caller=%#v", f.caller)
	}
}

func TestReviewPolicyHandlersRemainInOpenAPICoverageScope(t *testing.T) {
	data, err := os.ReadFile("../../../../Makefile")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte("internal/api/handler/plugin/review_policy.go")) {
		t.Fatal("OPENAPI_SCAN_DIRS omits review_policy.go")
	}
}

func TestUpdateReviewPolicyRequiresBoolean(t *testing.T) {
	f := &fakeService{}
	identity := model.Identity{UID: "owner-1", SpaceRoles: map[string]int{"space-a": model.SpaceRoleOwner}}
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/plugin_review_policies", bytes.NewBufferString(`{}`))
	req.Header.Set("Content-Type", "application/json")
	engineWithIdentity(identity, f).ServeHTTP(recorder, req)
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), `"code":"VALIDATION_ERROR"`) {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestUpdateReviewPolicyReviewerOnlyError(t *testing.T) {
	f := &fakeService{err: pluginsvc.ErrReviewPolicyForbidden}
	identity := model.Identity{UID: "member-1", SpaceRoles: map[string]int{"space-a": model.SpaceRoleMember}}
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/plugin_review_policies", bytes.NewBufferString(`{"is_auto_approve_enabled":false}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Space-Id", "space-a")
	engineWithIdentity(identity, f).ServeHTTP(recorder, req)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), `"required_role":"space_admin"`) ||
		!strings.Contains(recorder.Body.String(), `"message":"operation requires the Space owner or admin role"`) {
		t.Fatalf("body=%s", recorder.Body.String())
	}
}
