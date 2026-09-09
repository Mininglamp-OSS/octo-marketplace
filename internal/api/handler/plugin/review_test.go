package plugin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	pluginsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/plugin"
)

func reviewRequestFixture() *model.PluginReviewRequest {
	reviewer := "admin-1"
	reviewerName := "Adam"
	reason := "needs docs"
	changelog := "notes"
	source := model.ReviewDecisionSourceIM
	reviewed := time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC)
	current := "1.0.0"
	return &model.PluginReviewRequest{
		ID: "review-1", PluginID: "plugin-1", SpaceID: "space-a", TargetScope: "space",
		Status: model.ReviewStatusRejected, Kind: model.ReviewKindUpgrade, Version: "2.0.0",
		Changelog: &changelog, ManifestHash: "sha256:m", PluginHash: "sha256:p",
		ApplicantUID: "user-1", ApplicantName: "Alice",
		ReviewerUID: &reviewer, ReviewerName: &reviewerName, Reason: &reason, DecisionSource: &source,
		SubmittedAt: time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC), ReviewedAt: &reviewed,
		PluginName: "Demo", PluginType: model.PluginTypeSkill,
		PluginIcon: "https://cdn.example.com/icon.png", CurrentVersion: &current,
		ReadmeContent: "# body",
	}
}

func doReview(t *testing.T, f *fakeService, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, bytes.NewReader([]byte(body)))
		req.Header.Set("Content-Type", "application/json")
	}
	testEngine(f).ServeHTTP(rec, req)
	return rec
}

// The octo-web review client is already built against this exact field set. A
// renamed or dropped key is a broken page, so the wire form is asserted key by
// key rather than by round-tripping into the same struct.
func TestReviewDetailWireContract(t *testing.T) {
	f := &fakeService{}
	f.review.request = reviewRequestFixture()
	rec := doReview(t, f, http.MethodGet, "/api/v1/plugins/review_requests/review-1", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var envelope struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body.String())
	}
	want := map[string]any{
		"review_id":       "review-1",
		"plugin_id":       "plugin-1",
		"space_id":        "space-a",
		"target_scope":    "space",
		"status":          "rejected",
		"kind":            "upgrade",
		"version":         "2.0.0",
		"changelog":       "notes",
		"manifest_hash":   "sha256:m",
		"plugin_hash":     "sha256:p",
		"applicant_id":    "user-1",
		"applicant_name":  "Alice",
		"reviewer_id":     "admin-1",
		"reviewer_name":   "Adam",
		"reason":          "needs docs",
		"decision_source": "im",
		"plugin_name":     "Demo",
		"plugin_type":     "skill",
		"plugin_icon":     "https://cdn.example.com/icon.png",
		"current_version": "1.0.0",
		"readme_content":  "# body",
	}
	for key, expected := range want {
		got, ok := envelope.Data[key]
		if !ok {
			t.Errorf("field %q missing from the response", key)
			continue
		}
		if got != expected {
			t.Errorf("field %q = %v, want %v", key, got, expected)
		}
	}
	for _, key := range []string{"submitted_at", "reviewed_at"} {
		if _, ok := envelope.Data[key]; !ok {
			t.Errorf("field %q missing", key)
		}
	}
	// The frozen snapshot bytes must never reach the browser.
	for _, key := range []string{"manifest_json", "plugin_json", "relations_json"} {
		if _, leaked := envelope.Data[key]; leaked {
			t.Errorf("snapshot field %q leaked into the detail response", key)
		}
	}
	if f.review.getID != "review-1" {
		t.Errorf("review id passed to the service = %q", f.review.getID)
	}
}

func TestSubmitReviewPassesBodyThrough(t *testing.T) {
	f := &fakeService{}
	f.review.request = reviewRequestFixture()
	rec := doReview(t, f, http.MethodPost, "/api/v1/plugins/review_requests",
		`{"plugin_id":"plugin-1","version":"2.0.0","changelog":"notes"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if f.review.submitParams.PluginID != "plugin-1" || f.review.submitParams.Version != "2.0.0" || f.review.submitParams.Changelog != "notes" {
		t.Fatalf("params = %+v", f.review.submitParams)
	}
	if f.caller.UID != "user-1" || f.caller.SpaceID != "space-a" {
		t.Fatalf("caller = %+v; identity must come from the authenticator", f.caller)
	}
}

// An unknown field must not fail the request: the shipped client is the contract
// and a stricter decoder than it was tested against is a self-inflicted outage.
func TestSubmitReviewToleratesUnknownFields(t *testing.T) {
	f := &fakeService{}
	f.review.request = reviewRequestFixture()
	rec := doReview(t, f, http.MethodPost, "/api/v1/plugins/review_requests",
		`{"plugin_id":"plugin-1","version":"2.0.0","plugin_name":"Demo"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
}

func TestListReviewsRequiresMode(t *testing.T) {
	tests := []struct {
		name  string
		query string
		want  int
	}{
		{name: "missing mode", query: "", want: http.StatusBadRequest},
		{name: "unknown mode", query: "?mode=everything", want: http.StatusBadRequest},
		{name: "unknown status", query: "?mode=mine&status=fired", want: http.StatusBadRequest},
		{name: "mine", query: "?mode=mine", want: http.StatusOK},
		{name: "space", query: "?mode=space&status=pending", want: http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeService{}
			rec := doReview(t, f, http.MethodGet, "/api/v1/plugins/review_requests"+tt.query, "")
			if rec.Code != tt.want {
				t.Fatalf("status = %d, want %d (%s)", rec.Code, tt.want, rec.Body.String())
			}
		})
	}
}

// An empty page must still be `{"data":[],"pagination":{...}}`, never a null.
func TestListReviewsEmitsPagination(t *testing.T) {
	f := &fakeService{}
	f.review.total = 0
	rec := doReview(t, f, http.MethodGet, "/api/v1/plugins/review_requests?mode=mine&page=2&page_size=5", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var out struct {
		Data       []map[string]any `json:"data"`
		Pagination struct {
			Total    int `json:"total"`
			Page     int `json:"page"`
			PageSize int `json:"page_size"`
		} `json:"pagination"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body.String())
	}
	if out.Data == nil {
		t.Error("data is null, want []")
	}
	if out.Pagination.Page != 2 || out.Pagination.PageSize != 5 {
		t.Fatalf("pagination = %+v", out.Pagination)
	}
	if f.review.listPage != 2 || f.review.listPageSize != 5 {
		t.Fatalf("service page/page_size = %d/%d", f.review.listPage, f.review.listPageSize)
	}
}

// Each service sentinel must land on the status the frontend switches on. Before
// this change ErrReviewForbidden had no branch at all and fell through to 500.
func TestReviewErrorsMapToStatusCodes(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		method string
		path   string
		body   string
		want   int
		code   string
	}{
		{name: "submit conflict", err: pluginsvc.ErrConflict, method: http.MethodPost, path: "/api/v1/plugins/review_requests", body: `{"plugin_id":"p","version":"1"}`, want: http.StatusConflict, code: "CONFLICT"},
		{name: "submit not owner", err: pluginsvc.ErrNotFound, method: http.MethodPost, path: "/api/v1/plugins/review_requests", body: `{"plugin_id":"p","version":"1"}`, want: http.StatusNotFound, code: "NOT_FOUND"},
		{name: "submit invalid", err: pluginsvc.ErrReviewInvalid, method: http.MethodPost, path: "/api/v1/plugins/review_requests", body: `{"plugin_id":"p","version":"1"}`, want: http.StatusBadRequest, code: "VALIDATION_ERROR"},
		{name: "list forbidden", err: pluginsvc.ErrReviewForbidden, method: http.MethodGet, path: "/api/v1/plugins/review_requests?mode=space", want: http.StatusForbidden, code: "FORBIDDEN"},
		{name: "approve forbidden", err: pluginsvc.ErrReviewForbidden, method: http.MethodPost, path: "/api/v1/plugins/review_requests/review-1/approve", body: "{}", want: http.StatusForbidden, code: "FORBIDDEN"},
		{name: "approve conflict", err: pluginsvc.ErrConflict, method: http.MethodPost, path: "/api/v1/plugins/review_requests/review-1/approve", body: "{}", want: http.StatusConflict, code: "CONFLICT"},
		{name: "approve cross-space", err: pluginsvc.ErrNotFound, method: http.MethodPost, path: "/api/v1/plugins/review_requests/review-1/approve", body: "{}", want: http.StatusNotFound, code: "NOT_FOUND"},
		{name: "reject reason required", err: pluginsvc.ErrReasonRequired, method: http.MethodPost, path: "/api/v1/plugins/review_requests/review-1/reject", body: `{"reason":""}`, want: http.StatusBadRequest, code: "VALIDATION_ERROR"},
		{name: "reject forbidden", err: pluginsvc.ErrReviewForbidden, method: http.MethodPost, path: "/api/v1/plugins/review_requests/review-1/reject", body: `{"reason":"no"}`, want: http.StatusForbidden, code: "FORBIDDEN"},
		// The one the applicant sees when their request was decided while the
		// cancel button was on screen. A 404 would say it never existed.
		{name: "cancel already decided", err: pluginsvc.ErrConflict, method: http.MethodPost, path: "/api/v1/plugins/review_requests/review-1/cancel", body: "{}", want: http.StatusConflict, code: "CONFLICT"},
		{name: "cancel not applicant", err: pluginsvc.ErrNotFound, method: http.MethodPost, path: "/api/v1/plugins/review_requests/review-1/cancel", body: "{}", want: http.StatusNotFound, code: "NOT_FOUND"},
		{name: "get cross-space", err: pluginsvc.ErrNotFound, method: http.MethodGet, path: "/api/v1/plugins/review_requests/review-1", want: http.StatusNotFound, code: "NOT_FOUND"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeService{}
			f.review.submitErr = tt.err
			f.review.listErr = tt.err
			f.review.getErr = tt.err
			f.review.approveErr = tt.err
			f.review.rejectErr = tt.err
			f.review.cancelErr = tt.err
			rec := doReview(t, f, tt.method, tt.path, tt.body)
			if rec.Code != tt.want {
				t.Fatalf("status = %d, want %d (%s)", rec.Code, tt.want, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), `"code":"`+tt.code+`"`) {
				t.Fatalf("error code missing %q: %s", tt.code, rec.Body.String())
			}
		})
	}
}

// A cross-Space read must be indistinguishable from a non-existent one: the body
// must not name the request or the Space it lives in.
func TestReviewNotFoundDoesNotRevealExistence(t *testing.T) {
	f := &fakeService{}
	f.review.getErr = pluginsvc.ErrNotFound
	rec := doReview(t, f, http.MethodGet, "/api/v1/plugins/review_requests/review-secret", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d", rec.Code)
	}
	for _, leak := range []string{"review-secret", "space-b", "exists"} {
		if strings.Contains(rec.Body.String(), leak) {
			t.Fatalf("404 body leaked %q: %s", leak, rec.Body.String())
		}
	}
}

func TestRejectReviewForwardsReason(t *testing.T) {
	f := &fakeService{}
	rec := doReview(t, f, http.MethodPost, "/api/v1/plugins/review_requests/review-1/reject", `{"reason":"needs docs"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if f.review.rejectID != "review-1" || f.review.rejectReason != "needs docs" {
		t.Fatalf("reject(%q,%q)", f.review.rejectID, f.review.rejectReason)
	}
}

func TestApproveReviewReturnsThePlugin(t *testing.T) {
	space := "space-a"
	f := &fakeService{}
	f.review.approved = &model.Plugin{ID: "plugin-1", Name: "Demo", Type: model.PluginTypeSkill, SpaceID: &space, Visibility: model.PluginVisibilitySpace, Tags: json.RawMessage(`[]`), Manifest: json.RawMessage(`{}`), Package: json.RawMessage(`{}`)}
	rec := doReview(t, f, http.MethodPost, "/api/v1/plugins/review_requests/review-1/approve", "{}")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Data struct {
			Plugin struct {
				PluginID   string `json:"plugin_id"`
				Visibility string `json:"visibility"`
			} `json:"plugin"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body.String())
	}
	if out.Data.Plugin.PluginID != "plugin-1" || out.Data.Plugin.Visibility != "space" {
		t.Fatalf("approve response = %+v", out.Data.Plugin)
	}
	if f.review.approveID != "review-1" {
		t.Errorf("approve id = %q", f.review.approveID)
	}
}

// The submit body now carries the reviewed content. This is the exact shape the
// frontend sends, so it is asserted field by field on the way in.
func TestSubmitReviewForwardsContentAndRelations(t *testing.T) {
	f := &fakeService{}
	f.review.request = reviewRequestFixture()
	body := `{"plugin_id":"plugin-1","version":"2.0.0","changelog":"notes",` +
		`"manifest_json":{"plugin_name":"Demo"},` +
		`"plugin_json":{"attachments":[]},` +
		`"relations":[{"relation_id":"rel-1","target_plugin_id":"skill-1","relation_type":"expert_skill","sort_order":3,"data":{"k":"v"}}]}`
	rec := doReview(t, f, http.MethodPost, "/api/v1/plugins/review_requests", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	p := f.review.submitParams
	if !strings.Contains(string(p.Manifest), "Demo") {
		t.Errorf("manifest = %s", p.Manifest)
	}
	if !strings.Contains(string(p.Package), "attachments") {
		t.Errorf("package = %s", p.Package)
	}
	if p.Relations == nil {
		t.Fatal("relations were dropped")
	}
	rels := *p.Relations
	if len(rels) != 1 {
		t.Fatalf("relations = %+v", rels)
	}
	if rels[0].ID != "rel-1" || rels[0].TargetPluginID != "skill-1" || rels[0].Type != "expert_skill" || rels[0].SortOrder != 3 {
		t.Fatalf("relation = %+v", rels[0])
	}
	if string(rels[0].Data) != `{"k":"v"}` {
		t.Errorf("relation data = %s", rels[0].Data)
	}
}

// Omitting `relations` must reach the service as a nil pointer (inherit the live
// graph), while an explicit empty array must reach it as a non-nil empty slice
// (clear the graph). Collapsing the two would let a document-only edit silently
// empty an expert team.
func TestSubmitReviewDistinguishesAbsentFromEmptyRelations(t *testing.T) {
	t.Run("absent", func(t *testing.T) {
		f := &fakeService{}
		f.review.request = reviewRequestFixture()
		rec := doReview(t, f, http.MethodPost, "/api/v1/plugins/review_requests",
			`{"plugin_id":"plugin-1","version":"2.0.0","manifest_json":{},"plugin_json":{}}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d", rec.Code)
		}
		if f.review.submitParams.Relations != nil {
			t.Fatal("an absent relations field became an explicit empty graph")
		}
	})
	t.Run("explicit empty", func(t *testing.T) {
		f := &fakeService{}
		f.review.request = reviewRequestFixture()
		rec := doReview(t, f, http.MethodPost, "/api/v1/plugins/review_requests",
			`{"plugin_id":"plugin-1","version":"2.0.0","manifest_json":{},"plugin_json":{},"relations":[]}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d", rec.Code)
		}
		if f.review.submitParams.Relations == nil {
			t.Fatal("an explicit empty relations array became 'inherit the live graph'")
		}
		if len(*f.review.submitParams.Relations) != 0 {
			t.Fatalf("relations = %+v", *f.review.submitParams.Relations)
		}
	})
}

// The two new sentinels must land on statuses a client can branch on: "send me
// the content" is a 400 naming the field, "this is listed, go through review" is
// a 409 with a machine-readable reason.
func TestListedAndContentRequiredErrorsMapToStatusCodes(t *testing.T) {
	t.Run("content required", func(t *testing.T) {
		f := &fakeService{}
		f.review.submitErr = pluginsvc.ErrReviewContentRequired
		rec := doReview(t, f, http.MethodPost, "/api/v1/plugins/review_requests", `{"plugin_id":"p","version":"2.0.0"}`)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400 (%s)", rec.Code, rec.Body.String())
		}
		for _, want := range []string{`"code":"VALIDATION_ERROR"`, `"field":"manifest_json"`} {
			if !strings.Contains(rec.Body.String(), want) {
				t.Fatalf("body missing %s: %s", want, rec.Body.String())
			}
		}
	})
	// A label collision is NOT the generic conflict_reason="state": the client has
	// to know the version field is the thing to change, and the applicant has to be
	// told to resubmit. Folding ErrLabelTaken back into the ErrConflict arm — which
	// is what shipped before the sentinel was split out — leaves the status at 409
	// and only the reason changes, so this asserts the reason and the field, not the
	// code.
	t.Run("label taken", func(t *testing.T) {
		f := &fakeService{}
		f.review.approveErr = pluginsvc.ErrLabelTaken
		rec := doReview(t, f, http.MethodPost, "/api/v1/plugins/review_requests/review-1/approve", `{}`)
		if rec.Code != http.StatusConflict {
			t.Fatalf("status = %d, want 409 (%s)", rec.Code, rec.Body.String())
		}
		for _, want := range []string{`"code":"CONFLICT"`, `"conflict_reason":"label_taken"`, `"field":"version"`} {
			if !strings.Contains(rec.Body.String(), want) {
				t.Fatalf("body missing %s: %s", want, rec.Body.String())
			}
		}
	})
	t.Run("listed requires review", func(t *testing.T) {
		space := "space-a"
		f := &fakeService{err: pluginsvc.ErrListedRequiresReview, detail: &pluginsvc.Detail{Plugin: &model.Plugin{ID: "plugin-1", SpaceID: &space}}}
		body := `{"plugin":{"plugin_id":"plugin-1","plugin_name":"Demo","plugin_type":"skill","visibility":"space","tags":[],"manifest_json":{},"plugin_json":{}},"relations":[]}`
		rec := doReview(t, f, http.MethodPost, "/api/v1/plugins/upsert", body)
		if rec.Code != http.StatusConflict {
			t.Fatalf("status = %d, want 409 (%s)", rec.Code, rec.Body.String())
		}
		for _, want := range []string{`"code":"CONFLICT"`, `"conflict_reason":"listed_requires_review"`} {
			if !strings.Contains(rec.Body.String(), want) {
				t.Fatalf("body missing %s: %s", want, rec.Body.String())
			}
		}
	})
}

// A review submit carries the FULL declared content since the upgrade amendment,
// so its body cap must match /plugins/upsert's. The old 64 KiB cap dated from
// when the submit was "a handful of short strings", and it silently froze upgrades:
// `parse_task_id` only exists for skills, and a direct edit of a listed plugin is
// 409, so a connector/expert/expert_team whose content crossed 64 KiB had no door
// to a new version at all. A payload that upsert accepts must not 413 here.
func TestSubmitReviewAcceptsAnUpsertSizedBody(t *testing.T) {
	if maxReviewBodyBytes != maxBodyBytes {
		t.Fatalf("review cap = %d, upsert cap = %d; the two paths carry the same content and must agree",
			maxReviewBodyBytes, maxBodyBytes)
	}
	f := &fakeService{}
	f.review.request = reviewRequestFixture()
	// Comfortably past the retired 64 KiB cap, comfortably under the shared one.
	body := `{"plugin_id":"plugin-1","version":"2.0.0","manifest_json":{"plugin_name":"Demo"},` +
		`"plugin_json":{"attachments":[{"path":"SKILL.md","content_type":"raw","raw_content":"` +
		strings.Repeat("x", 256<<10) + `"}]}}`
	rec := doReview(t, f, http.MethodPost, "/api/v1/plugins/review_requests", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s; a %d-byte submit was refused", rec.Code, rec.Body.String(), len(body))
	}
	if !strings.Contains(string(f.review.submitParams.Package), "SKILL.md") {
		t.Error("the oversize package never reached the service")
	}
}

// The detail response carries the FROZEN relation graph the reviewer is
// approving — for an expert/expert_team that membership IS the reviewable
// content, and omitting it meant the reviewer could approve a container whose
// members they had never inspected. The list response, by contrast, must stay
// lean and omit the graph per row.
func TestReviewDetailCarriesFrozenRelationsListOmitsThem(t *testing.T) {
	f := &fakeService{}
	base := reviewRequestFixture()
	// RelationsJSON is populated by LoadReviewSnapshot (the detail path). The
	// snapshot round-trips through plugin_review_requests.relations_json as a JSON
	// array of PluginRelation-shaped objects; note the marshal uses Go struct
	// field names by default (no json tags on PluginRelation), so relation_id /
	// target_plugin_id are uppercase keys in storage.
	base.RelationsJSON = json.RawMessage(`[` +
		`{"ID":"rel-1","SourcePluginID":"plugin-1","TargetPluginID":"skill-1","Type":"expert_skill","SortOrder":0,"Data":{"is_leader":false},"Status":1},` +
		`{"ID":"rel-2","SourcePluginID":"plugin-1","TargetPluginID":"skill-2","Type":"expert_skill","SortOrder":1,"Data":null,"Status":1}` +
		`]`)
	f.review.request = base

	t.Run("detail", func(t *testing.T) {
		rec := doReview(t, f, http.MethodGet, "/api/v1/plugins/review_requests/review-1", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
		}
		var envelope struct {
			Data struct {
				Relations []struct {
					RelationID     string          `json:"relation_id"`
					TargetPluginID string          `json:"target_plugin_id"`
					RelationType   string          `json:"relation_type"`
					SortOrder      int             `json:"sort_order"`
					Data           json.RawMessage `json:"data"`
				} `json:"frozen_relations"`
			} `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
			t.Fatalf("decode: %v (%s)", err, rec.Body.String())
		}
		if len(envelope.Data.Relations) != 2 {
			t.Fatalf("frozen_relations = %+v, want 2 edges", envelope.Data.Relations)
		}
		first := envelope.Data.Relations[0]
		if first.RelationID != "rel-1" || first.TargetPluginID != "skill-1" || first.RelationType != "expert_skill" || first.SortOrder != 0 {
			t.Fatalf("first edge = %+v", first)
		}
		// data=null on the second edge must collapse to omitted, not the literal
		// four bytes "null" (relation_json schema rejects the literal null).
		second := envelope.Data.Relations[1]
		if second.RelationID != "rel-2" {
			t.Fatalf("second edge = %+v", second)
		}
		if len(second.Data) != 0 && !bytes.Equal(bytes.TrimSpace(second.Data), []byte("null")) {
			t.Errorf("second edge data = %s, want omitted/null", second.Data)
		}
		// The raw storage column must not leak as a top-level key.
		var raw map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &raw)
		if data, ok := raw["data"].(map[string]any); ok {
			if _, leaked := data["relations_json"]; leaked {
				t.Errorf("raw relations_json leaked into the response")
			}
		}
	})

	t.Run("list omits per-row graph", func(t *testing.T) {
		// The list path uses reviewSelectBase, which does NOT select relations_json
		// at all — simulate that by clearing the fixture's RelationsJSON before the
		// call so the fake mirrors real repository output. This guards against a
		// regression where reviewDTO would blindly project whatever bytes happened
		// to be on the model onto the wire, leaking the graph per list row.
		listReq := *base
		listReq.RelationsJSON = nil
		f.review.request = &listReq
		f.review.items = []*model.PluginReviewRequest{&listReq}
		f.review.total = 1
		rec := doReview(t, f, http.MethodGet, "/api/v1/plugins/review_requests?mode=mine", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
		}
		var envelope struct {
			Data []map[string]any `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
			t.Fatalf("decode: %v (%s)", err, rec.Body.String())
		}
		if len(envelope.Data) != 1 {
			t.Fatalf("rows = %d", len(envelope.Data))
		}
		if _, present := envelope.Data[0]["frozen_relations"]; present {
			t.Errorf("list row carried frozen_relations; the queue must stay lean")
		}
	})
}

// A malformed relations_json in storage must not 500 the detail read — the
// reviewer still needs to see the documents so they can reject. ApproveReview
// will still refuse to apply the corrupt bytes at decision time.
func TestReviewDetailToleratesMalformedFrozenRelations(t *testing.T) {
	f := &fakeService{}
	base := reviewRequestFixture()
	base.RelationsJSON = json.RawMessage(`{not valid json`)
	f.review.request = base
	rec := doReview(t, f, http.MethodGet, "/api/v1/plugins/review_requests/review-1", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var envelope struct {
		Data struct {
			Relations json.RawMessage `json:"frozen_relations"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body.String())
	}
	// Must be present as an empty array, not absent (so the client can iterate
	// unconditionally), never null and never the raw malformed bytes.
	if !bytes.Equal(envelope.Data.Relations, []byte(`[]`)) {
		t.Errorf("frozen_relations = %s, want []", envelope.Data.Relations)
	}
}
