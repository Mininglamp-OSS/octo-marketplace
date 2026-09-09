package plugin

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	pluginsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/plugin"
)

func publishedDetail(state model.PluginListingState) *pluginsvc.Detail {
	space := "space-a"
	return &pluginsvc.Detail{Plugin: &model.Plugin{
		ID: "plugin-1", Name: "Example", Type: model.PluginTypeSkill,
		OwnerUID: "user-1", SpaceID: &space,
		Visibility: model.PluginVisibilityPrivate, ListingState: state,
	}}
}

// The response has to say which branch fired. Without review_id the client cannot
// tell "listed now" from "waiting for an admin" without a second request, and the
// 发布 button has no idea what toast to show.
func TestPublishWireContractDistinguishesTheTwoBranches(t *testing.T) {
	t.Run("immediate listing", func(t *testing.T) {
		f := &fakeService{}
		f.listing.publishResult = &pluginsvc.PublishResult{Plugin: publishedDetail(model.PluginListingStatePublished)}
		rec := doReview(t, f, http.MethodPost, "/api/v1/plugins/publish", `{"plugin_id":"plugin-1"}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
		}
		data := decodeData(t, rec.Body.Bytes())
		if got := data["listing_state"]; got != "published" {
			t.Errorf("listing_state = %v, want published", got)
		}
		if got := data["display_status"]; got != "published" {
			t.Errorf("display_status = %v, want published", got)
		}
		if _, present := data["review_id"]; present {
			t.Error("review_id is present on an immediate publish; the client will think it needs approval")
		}
		if f.listing.publishParams.PluginID != "plugin-1" {
			t.Errorf("plugin_id passed down = %q", f.listing.publishParams.PluginID)
		}
	})

	t.Run("routed to review", func(t *testing.T) {
		f := &fakeService{}
		f.listing.publishResult = &pluginsvc.PublishResult{
			Plugin: publishedDetail(model.PluginListingStateDraft),
			Review: &model.PluginReviewRequest{ID: "review-9", Status: model.ReviewStatusPending},
		}
		rec := doReview(t, f, http.MethodPost, "/api/v1/plugins/publish", `{"plugin_id":"plugin-1","version":"2.0.0","changelog":"notes"}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
		}
		data := decodeData(t, rec.Body.Bytes())
		if got := data["listing_state"]; got != "draft" {
			t.Errorf("listing_state = %v, want draft — approval is what lists it", got)
		}
		if got := data["display_status"]; got != "pending_review" {
			t.Errorf("display_status = %v, want pending_review", got)
		}
		if got := data["review_id"]; got != "review-9" {
			t.Errorf("review_id = %v, want review-9", got)
		}
		if f.listing.publishParams.Version != "2.0.0" || f.listing.publishParams.Changelog != "notes" {
			t.Errorf("version/changelog not passed down: %+v", f.listing.publishParams)
		}
	})
}

func TestDelistWireContract(t *testing.T) {
	f := &fakeService{}
	f.listing.delistResult = publishedDetail(model.PluginListingStateDelisted)
	rec := doReview(t, f, http.MethodPost, "/api/v1/plugins/delist", `{"plugin_id":"plugin-1","reason":"policy"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	data := decodeData(t, rec.Body.Bytes())
	if got := data["listing_state"]; got != "delisted" {
		t.Errorf("listing_state = %v, want delisted", got)
	}
	if got := data["display_status"]; got != "delisted" {
		t.Errorf("display_status = %v, want delisted", got)
	}
	if f.listing.delistParams.Reason != "policy" {
		t.Errorf("reason passed down = %q", f.listing.delistParams.Reason)
	}
}

// Every listing-lifecycle refusal has to land on the right status code with a
// machine-readable conflict_reason, because the client branches on it: a stale
// view is retryable after a refresh, a permission problem is not.
func TestListingErrorsMapToStatusCodes(t *testing.T) {
	for _, tc := range []struct {
		name         string
		path         string
		err          error
		wantStatus   int
		wantCode     string
		wantConflict string
	}{
		{"publish of an already-listed plugin", "publish", pluginsvc.ErrAlreadyPublished, http.StatusConflict, "CONFLICT", "already_published"},
		{"publish during a pending review", "publish", pluginsvc.ErrReviewPending, http.StatusConflict, "CONFLICT", "review_pending"},
		// 404 rather than 403: a non-owner must not learn the plugin exists.
		{"publish by a non-owner", "publish", pluginsvc.ErrNotFound, http.StatusNotFound, "NOT_FOUND", ""},
		{"delist of an unpublished plugin", "delist", pluginsvc.ErrNotPublished, http.StatusConflict, "CONFLICT", "not_published"},
		{"delist without the reviewer role", "delist", pluginsvc.ErrReviewForbidden, http.StatusForbidden, "FORBIDDEN", ""},
		// Cross-Space: 404, not 403, even for an admin — of a different Space.
		{"delist across Spaces", "delist", pluginsvc.ErrNotFound, http.StatusNotFound, "NOT_FOUND", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeService{}
			if tc.path == "publish" {
				f.listing.publishErr = tc.err
			} else {
				f.listing.delistErr = tc.err
			}
			rec := doReview(t, f, http.MethodPost, "/api/v1/plugins/"+tc.path, `{"plugin_id":"plugin-1"}`)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
			var envelope struct {
				Error struct {
					Code    string         `json:"code"`
					Details map[string]any `json:"details"`
				} `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
				t.Fatalf("decode: %v (%s)", err, rec.Body.String())
			}
			if envelope.Error.Code != tc.wantCode {
				t.Errorf("code = %q, want %q", envelope.Error.Code, tc.wantCode)
			}
			if tc.wantConflict != "" && envelope.Error.Details["conflict_reason"] != tc.wantConflict {
				t.Errorf("conflict_reason = %v, want %q", envelope.Error.Details["conflict_reason"], tc.wantConflict)
			}
			if tc.wantStatus == http.StatusForbidden && envelope.Error.Details["required_role"] != "space_admin" {
				t.Errorf("required_role = %v, want space_admin", envelope.Error.Details["required_role"])
			}
		})
	}
}

func decodeData(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var envelope struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("decode: %v (%s)", err, string(body))
	}
	return envelope.Data
}

// plugin_type is required for the marketplace grid but optional for 我的发布, whose
// 全部 tab lists every kind the caller owns in one table.
func TestListAcceptsAMissingPluginTypeOnlyForModeMine(t *testing.T) {
	for _, tc := range []struct {
		name       string
		query      string
		wantStatus int
	}{
		{"mine without a type", "scene_code=default&mode=mine", http.StatusOK},
		{"mine with a type", "scene_code=default&mode=mine&plugin_type=skill", http.StatusOK},
		{"the grid without a type", "scene_code=default", http.StatusBadRequest},
		{"the grid with a type", "scene_code=default&plugin_type=skill", http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeService{}
			rec := doReview(t, f, http.MethodGet, "/api/v1/plugins?"+tc.query, "")
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if tc.wantStatus == http.StatusBadRequest {
				var envelope struct {
					Error struct {
						Details map[string]any `json:"details"`
					} `json:"error"`
				}
				if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
					t.Fatal(err)
				}
				if envelope.Error.Details["field"] != "plugin_type" {
					t.Errorf("field = %v, want plugin_type", envelope.Error.Details["field"])
				}
			}
		})
	}
}
