package plugin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	pluginsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/plugin"
)

func TestAdminUpdateRatingAcceptsValueAndNull(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want *int
	}{
		{name: "value", body: `{"rating":5}`, want: handlerIntPointer(5)},
		{name: "null", body: `{"rating":null}`, want: nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeAdminService{detail: &pluginsvc.Detail{Plugin: &model.Plugin{ID: "plugin-1", Rating: tc.want, Tags: json.RawMessage(`[]`), Manifest: json.RawMessage(`{}`), Package: json.RawMessage(`{}`)}}}
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPatch, "/api/v1/admin/plugins/plugin-1/rating", bytes.NewBufferString(tc.body))
			req.Header.Set("Content-Type", "application/json")
			adminTestEngine(f, &fakeAdminCategories{}).ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			if f.ratingID != "plugin-1" || (tc.want == nil) != (f.rating == nil) || (tc.want != nil && *f.rating != *tc.want) {
				t.Fatalf("forwarded id/rating = %q/%v", f.ratingID, f.rating)
			}
			if f.caller.UID != "admin-1" || !f.caller.IsSystemAdmin {
				t.Fatalf("caller=%#v", f.caller)
			}
			if !bytes.Contains(rec.Body.Bytes(), []byte(`"plugin_id":"plugin-1"`)) {
				t.Fatalf("response missing plugin_id: %s", rec.Body.String())
			}
			wantRating := `"rating":null`
			if tc.want != nil {
				wantRating = `"rating":5`
			}
			if !bytes.Contains(rec.Body.Bytes(), []byte(wantRating)) {
				t.Fatalf("response missing rating: %s", rec.Body.String())
			}
		})
	}
}

func TestAdminUpdateRatingMapsEmbeddedNotFound(t *testing.T) {
	f := &fakeAdminService{err: pluginsvc.ErrNotFound}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/admin/plugins/skill-embedded/rating", bytes.NewBufferString(`{"rating":5}`))
	req.Header.Set("Content-Type", "application/json")

	adminTestEngine(f, &fakeAdminCategories{}).ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound || !bytes.Contains(rec.Body.Bytes(), []byte(`"code":"NOT_FOUND"`)) {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestAdminUpdateRatingRejectsMalformedBodies(t *testing.T) {
	for _, body := range []string{`{}`, `{"rating":1.5}`, `{"rating":"5"}`, `{"rating":5,"extra":true}`} {
		f := &fakeAdminService{detail: &pluginsvc.Detail{Plugin: &model.Plugin{ID: "plugin-1"}}}
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPatch, "/api/v1/admin/plugins/plugin-1/rating", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		adminTestEngine(f, &fakeAdminCategories{}).ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("body=%s status=%d response=%s", body, rec.Code, rec.Body.String())
		}
	}
}

func TestPluginListAndDetailDTOExposeNullableRating(t *testing.T) {
	rating := 3
	listJSON, err := json.Marshal(listItemDTO(&model.Plugin{Rating: &rating}))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(listJSON, []byte(`"rating":3`)) {
		t.Fatalf("list DTO missing rating: %s", listJSON)
	}
	detailJSON, err := json.Marshal(pluginDTO(&model.Plugin{}))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(detailJSON, []byte(`"rating":null`)) {
		t.Fatalf("detail DTO must expose unrated as null: %s", detailJSON)
	}
}

func handlerIntPointer(v int) *int { return &v }
