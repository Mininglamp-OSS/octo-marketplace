package plugin

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

// Exercise the JSON returned by every review projection, including both list
// modes, so policy approvals cannot masquerade as human decisions on any door.
func TestReviewDecisionAttributionWireContract(t *testing.T) {
	decisions := []struct {
		name       string
		source     model.ReviewDecisionSource
		wireSource string
	}{
		{name: "policy", source: model.ReviewDecisionSourcePolicy, wireSource: "web"},
		{name: "manual web", source: model.ReviewDecisionSourceWeb, wireSource: "web"},
		{name: "manual im", source: model.ReviewDecisionSourceIM, wireSource: "im"},
		{name: "pending"},
	}
	surfaces := []struct {
		name   string
		method string
		path   string
		body   string
		list   bool
	}{
		{name: "detail", method: http.MethodGet, path: "/api/v1/plugins/review_requests/review-1"},
		{name: "space list", method: http.MethodGet, path: "/api/v1/plugins/review_requests?mode=space", list: true},
		{name: "mine list", method: http.MethodGet, path: "/api/v1/plugins/review_requests?mode=mine", list: true},
		{name: "submit", method: http.MethodPost, path: "/api/v1/plugins/review_requests", body: `{"plugin_id":"plugin-1","version":"2.0.0"}`},
	}
	for _, decision := range decisions {
		for _, surface := range surfaces {
			t.Run(decision.name+"/"+surface.name, func(t *testing.T) {
				review := reviewRequestFixture()
				review.Status = model.ReviewStatusApproved
				review.Reason = nil
				review.DecisionSource = &decision.source
				isPolicy := decision.source == model.ReviewDecisionSourcePolicy
				if isPolicy {
					// Production retains the applicant as the triggering actor.
					review.ReviewerUID = &review.ApplicantUID
					review.ReviewerName = &review.ApplicantName
				} else if decision.source == "" {
					review.Status = model.ReviewStatusPending
					review.DecisionSource, review.ReviewerUID, review.ReviewerName, review.ReviewedAt = nil, nil, nil, nil
				}
				f := &fakeService{}
				f.review.request = review
				f.review.items = []*model.PluginReviewRequest{review}
				f.review.total = 1
				rec := doReview(t, f, surface.method, surface.path, surface.body)
				if rec.Code != http.StatusOK {
					t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
				}
				var envelope struct {
					Data json.RawMessage `json:"data"`
				}
				if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
					t.Fatal(err)
				}
				if surface.list {
					var items []json.RawMessage
					if err := json.Unmarshal(envelope.Data, &items); err != nil {
						t.Fatal(err)
					}
					if len(items) != 1 {
						t.Fatalf("list length = %d, want 1", len(items))
					}
					envelope.Data = items[0]
				}
				var data map[string]any
				if err := json.Unmarshal(envelope.Data, &data); err != nil {
					t.Fatal(err)
				}
				want := map[string]any{
					"status": string(review.Status), "applicant_id": "user-1", "applicant_name": "Alice",
				}
				var absent []string
				if decision.wireSource != "" {
					want["decision_source"] = decision.wireSource
					want["reviewed_at"] = review.ReviewedAt.Format(time.RFC3339)
				} else {
					absent = append(absent, "decision_source", "reviewed_at")
				}
				if isPolicy {
					want["is_auto_approved"] = true
				} else {
					absent = append(absent, "is_auto_approved")
				}
				if isPolicy || decision.source == "" {
					absent = append(absent, "reviewer_id", "reviewer_name")
				} else {
					want["reviewer_id"], want["reviewer_name"] = "admin-1", "Adam"
				}
				for key, expected := range want {
					if got := data[key]; got != expected {
						t.Errorf("%s = %v, want %v", key, got, expected)
					}
				}
				for _, key := range absent {
					if value, exists := data[key]; exists {
						t.Errorf("%s must be omitted, got %v", key, value)
					}
				}
				if isPolicy && (review.DecisionSource == nil || *review.DecisionSource != model.ReviewDecisionSourcePolicy ||
					review.ReviewerUID == nil || *review.ReviewerUID != "user-1" ||
					review.ReviewerName == nil || *review.ReviewerName != "Alice") {
					t.Fatal("response projection mutated the internal audit attribution")
				}
			})
		}
	}
}
