package plugin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	pluginrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/plugin"
	pluginsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/plugin"
)

type visibilityServiceFake struct {
	*fakeService
	params pluginsvc.VisibilityParams
}

func (f *visibilityServiceFake) ChangeVisibility(_ context.Context, who pluginsvc.Caller, p pluginsvc.VisibilityParams) (*pluginrepo.VisibilityResult, error) {
	f.caller = who
	f.params = p
	return &pluginrepo.VisibilityResult{Plugin: &model.Plugin{ID: p.PluginID, Visibility: model.PluginVisibilityPrivate}, PublishedVersionID: p.ExpectedPublishedVersionID, ReviewID: "review-1", Pending: true}, f.err
}
func TestVisibilityChangeReturnsEffectiveAudienceAndPendingReview(t *testing.T) {
	f := &visibilityServiceFake{fakeService: &fakeService{}}
	rec := httptest.NewRecorder()
	testEngine(f).ServeHTTP(rec, httptest.NewRequest(http.MethodPatch, "/api/v1/plugins/p1/visibility", strings.NewReader(`{"visibility":"space","expected_published_version_id":"release-v1"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if f.params.PluginID != "p1" || f.params.ExpectedPublishedVersionID != "release-v1" || f.params.Visibility != model.PluginVisibilitySpace {
		t.Fatalf("wrong request %+v", f.params)
	}
	for _, part := range []string{`"visibility":"private"`, `"published_version_id":"release-v1"`, `"display_status":"pending_review"`, `"review_id":"review-1"`} {
		if !strings.Contains(rec.Body.String(), part) {
			t.Fatalf("missing %s: %s", part, rec.Body.String())
		}
	}
}
func TestVisibilityChangeDoesNotExposeAnUnauthorizedResource(t *testing.T) {
	f := &visibilityServiceFake{fakeService: &fakeService{err: pluginsvc.ErrNotFound}}
	rec := httptest.NewRecorder()
	testEngine(f).ServeHTTP(rec, httptest.NewRequest(http.MethodPatch, "/api/v1/plugins/p1/visibility", strings.NewReader(`{"visibility":"space","expected_published_version_id":"release-v1"}`)))
	if rec.Code != http.StatusNotFound || strings.Contains(rec.Body.String(), "release-v1") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}
