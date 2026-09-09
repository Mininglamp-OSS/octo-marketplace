package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	pluginrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/plugin"
)

// reviewApplicant owns the plugin and is a plain Space member.
var reviewApplicant = Caller{UID: "user-1", Name: "Alice", SpaceID: "space-a", RequestID: "request-1", SpaceRole: SpaceRoleMember}

// reviewAdmin is a DIFFERENT account holding the Space admin role. Using one
// account for both roles hides the whole class of owner-vs-reviewer scoping bugs.
var reviewAdmin = Caller{UID: "admin-1", Name: "Adam", SpaceID: "space-a", RequestID: "request-2", SpaceRole: SpaceRoleAdmin}

const reviewPackage = `{"attachments":[{"path":"SKILL.md","content_type":"raw","mime_type":"text/markdown","raw_content":"# Frozen body"}]}`

func reviewFixture(t *testing.T) (*fakeStore, *Service) {
	t.Helper()
	space := "space-a"
	store := &fakeStore{relations: map[string][]model.PluginRelation{}, plugins: map[string]*model.Plugin{
		"plugin-1": {
			ID: "plugin-1", Name: "Demo", Type: model.PluginTypeSkill,
			OwnerUID: "user-1", SpaceID: &space, Visibility: model.PluginVisibilitySpace,
			Manifest:     json.RawMessage(`{"plugin_name":"Demo","description":"manifest fallback"}`),
			Package:      json.RawMessage(reviewPackage),
			ManifestHash: "sha256:m", PluginHash: "sha256:p",
		},
	}}
	store.review.stored = &model.PluginReviewRequest{
		ID: "review-new", PluginID: "plugin-1", SpaceID: "space-a",
		Status: model.ReviewStatusPending, Kind: model.ReviewKindFirst, Version: "1.0.0",
		ApplicantUID: "user-1", ApplicantName: "Alice", PluginName: "Demo",
		PluginType: model.PluginTypeSkill,
	}
	return store, fixedService(store)
}

// --- submit -------------------------------------------------------------------

// The reviewed snapshot must be the draft content at submit time, INCLUDING the
// relation graph: for an expert/expert_team the membership is the reviewable
// content, and freezing only the documents ships the reviewed manifest next to
// whatever the live membership happens to be at approve time.
func TestSubmitReviewFreezesDocumentsAndRelations(t *testing.T) {
	store, svc := reviewFixture(t)
	store.relations["plugin-1"] = []model.PluginRelation{
		{ID: "rel-1", SourcePluginID: "plugin-1", TargetPluginID: "skill-1", Type: "expert_skill", SortOrder: 0, Status: 1},
	}
	if _, err := svc.SubmitReview(context.Background(), reviewApplicant, ReviewSubmitParams{
		PluginID: "plugin-1", Version: "1.0.0", Changelog: "first",
	}); err != nil {
		t.Fatal(err)
	}
	snap := store.review.insertSnap
	if snap.ManifestHash != "sha256:m" || snap.PluginHash != "sha256:p" {
		t.Fatalf("snapshot hashes = %+v", snap)
	}
	if !strings.Contains(string(snap.Manifest), `"Demo"`) {
		t.Fatalf("snapshot manifest = %s", snap.Manifest)
	}
	if len(snap.Relations) != 1 || snap.Relations[0].TargetPluginID != "skill-1" {
		t.Fatalf("snapshot relations = %+v", snap.Relations)
	}
	req := store.review.insertReq
	if req.Version != "1.0.0" || req.Changelog == nil || *req.Changelog != "first" {
		t.Fatalf("request = %+v", req)
	}
	if req.ApplicantUID != "user-1" || req.SpaceID != "space-a" {
		t.Fatalf("applicant/space = %q/%q", req.ApplicantUID, req.SpaceID)
	}
	// kind is derived inside the repository transaction, never taken from a client.
	if req.Kind != model.ReviewKindFirst {
		t.Fatalf("kind = %q; the service must not set it", req.Kind)
	}
	if store.review.insertScope.SpaceID != "space-a" || store.review.insertScope.CallerUID != "user-1" {
		t.Fatalf("insert scope = %+v", store.review.insertScope)
	}
}

// Submitting someone else's plugin must not be possible, and must not confirm
// that the plugin exists.
func TestSubmitReviewRejectsNonOwner(t *testing.T) {
	store, svc := reviewFixture(t)
	stranger := Caller{UID: "user-9", Name: "Eve", SpaceID: "space-a", RequestID: "r"}
	_, err := svc.SubmitReview(context.Background(), stranger, ReviewSubmitParams{PluginID: "plugin-1", Version: "1.0.0"})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("error = %v, want ErrNotFound", err)
	}
	if store.review.insertReq != nil {
		t.Error("non-owner submit reached the repository")
	}
}

// An embedded child versions with its container and is never independently
// reviewable; letting one through would list a bundled skill on its own.
func TestSubmitReviewRejectsEmbeddedChild(t *testing.T) {
	store, svc := reviewFixture(t)
	store.plugins["plugin-1"].IsEmbedded = true
	if _, err := svc.SubmitReview(context.Background(), reviewApplicant, ReviewSubmitParams{PluginID: "plugin-1", Version: "1.0.0"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("error = %v, want ErrNotFound", err)
	}
	if store.review.insertReq != nil {
		t.Error("embedded submit reached the repository")
	}
}

// A private plugin has no org audience to review, and approving its request
// could never list it (ApproveReview would either no-op a published+private row
// or, on a draft, stamp org visibility the author never asked for). Review is a
// `space`-intent gate, so a submission whose declared visibility is not `space`
// is refused before it reaches the repository. Publish routes only `space` here,
// but the direct endpoint must hold the invariant on its own.
func TestSubmitReviewRefusesNonSpaceVisibility(t *testing.T) {
	store, svc := reviewFixture(t)
	store.plugins["plugin-1"].Visibility = model.PluginVisibilityPrivate
	if _, err := svc.SubmitReview(context.Background(), reviewApplicant, ReviewSubmitParams{PluginID: "plugin-1", Version: "1.0.0"}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("error = %v, want ErrInvalidRequest", err)
	}
	if store.review.insertReq != nil {
		t.Error("private-visibility submit reached the repository")
	}
}

func TestSubmitReviewValidatesInput(t *testing.T) {
	tests := []struct {
		name   string
		params ReviewSubmitParams
	}{
		{name: "missing plugin id", params: ReviewSubmitParams{Version: "1.0.0"}},
		{name: "missing version", params: ReviewSubmitParams{PluginID: "plugin-1"}},
		{name: "blank version", params: ReviewSubmitParams{PluginID: "plugin-1", Version: "   "}},
		{name: "oversized version", params: ReviewSubmitParams{PluginID: "plugin-1", Version: strings.Repeat("v", 65)}},
		{name: "version with a space", params: ReviewSubmitParams{PluginID: "plugin-1", Version: "1.0 beta"}},
		{name: "oversized changelog", params: ReviewSubmitParams{PluginID: "plugin-1", Version: "1.0.0", Changelog: strings.Repeat("蟹", 1001)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, svc := reviewFixture(t)
			_, err := svc.SubmitReview(context.Background(), reviewApplicant, tt.params)
			// Validation errors now surface as either ErrReviewInvalid (for empty
			// plugin_id) or a *ReviewFieldError wrapping the cause. Both are 400
			// responses; we assert the error is non-nil and NOT ErrConflict/
			// ErrNotFound/internal, and that it did not reach the repository.
			if err == nil || errors.Is(err, ErrConflict) || errors.Is(err, ErrNotFound) {
				t.Fatalf("error = %v, want a validation error", err)
			}
			var fieldErr *ReviewFieldError
			if !errors.Is(err, ErrReviewInvalid) && !errors.As(err, &fieldErr) {
				t.Fatalf("error = %v, want ErrReviewInvalid or *ReviewFieldError", err)
			}
			if store.review.insertReq != nil {
				t.Error("invalid submit reached the repository")
			}
		})
	}
}

// A conflict from the single-pending index or an already-published label must
// surface as ErrConflict (409), not as a 500.
func TestSubmitReviewMapsRepositoryConflict(t *testing.T) {
	store, svc := reviewFixture(t)
	store.review.insertErr = pluginrepo.ErrConflict
	_, err := svc.SubmitReview(context.Background(), reviewApplicant, ReviewSubmitParams{PluginID: "plugin-1", Version: "1.0.0"})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("error = %v, want ErrConflict", err)
	}
}

// --- notification dispatch ------------------------------------------------------

// Nothing about the card may run before the response: the submit has already
// committed, and an octo-server round trip on the request goroutine puts a
// remote timeout in front of every submit.
func TestSubmitReviewDispatchesCardOnlyThroughTheBestEffortHook(t *testing.T) {
	store, svc := reviewFixture(t)
	notifier := &fakeNotifier{enabled: true}
	var deferred []func(context.Context) error
	var descs []string
	svc = svc.WithNotify(notifier, func(desc string, fn func(context.Context) error) {
		descs = append(descs, desc)
		deferred = append(deferred, fn)
	})

	if _, err := svc.SubmitReview(context.Background(), reviewApplicant, ReviewSubmitParams{PluginID: "plugin-1", Version: "2.0.0", Changelog: "notes"}); err != nil {
		t.Fatal(err)
	}
	if len(notifier.notifyIn) != 0 {
		t.Fatal("the card was dispatched on the request path, not post-commit")
	}
	if len(deferred) != 1 || descs[0] != "review.submit" {
		t.Fatalf("best-effort hooks = %v", descs)
	}
	if err := deferred[0](context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(notifier.notifyIn) != 1 {
		t.Fatalf("notify calls = %d", len(notifier.notifyIn))
	}
	sent := notifier.notifyIn[0]
	if sent.SpaceID != "space-a" || sent.ActorUID != "user-1" || sent.Service != "marketplace" {
		t.Fatalf("notify request = %+v", sent)
	}
	if sent.ApprovalCard == nil || sent.ApprovalCard.ActionType != CardActionTypeReviewDecision {
		t.Fatalf("approval card = %+v", sent.ApprovalCard)
	}
	if got := sent.ApprovalCard.Data["review_id"]; got != "review-new" {
		t.Errorf("card review_id = %q", got)
	}
	if got := sent.ApprovalCard.Data["plugin_id"]; got != "plugin-1" {
		t.Errorf("card plugin_id = %q", got)
	}
	_ = store
}

// A notifier failure is logged and dropped: the request is already committed and
// the web queue is the source of truth.
func TestSubmitReviewSucceedsWhenNotificationFails(t *testing.T) {
	_, svc := reviewFixture(t)
	notifier := &fakeNotifier{enabled: true, notifyErr: errors.New("octo-server down")}
	var calls []string
	svc = svc.WithNotify(notifier, syncBestEffort(&calls))
	out, err := svc.SubmitReview(context.Background(), reviewApplicant, ReviewSubmitParams{PluginID: "plugin-1", Version: "1.0.0"})
	if err != nil {
		t.Fatalf("submit failed because notification failed: %v", err)
	}
	if out == nil || out.ID != "review-new" {
		t.Fatalf("request = %+v", out)
	}
	if len(calls) != 1 {
		t.Fatalf("best-effort calls = %v", calls)
	}
}

func TestSubmitReviewSkipsDispatchWhenNotifyDisabled(t *testing.T) {
	_, svc := reviewFixture(t)
	notifier := &fakeNotifier{enabled: false}
	var calls []string
	svc = svc.WithNotify(notifier, syncBestEffort(&calls))
	if _, err := svc.SubmitReview(context.Background(), reviewApplicant, ReviewSubmitParams{PluginID: "plugin-1", Version: "1.0.0"}); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 0 || len(notifier.notifyIn) != 0 {
		t.Fatalf("dispatched with notify disabled: %v", calls)
	}
}

// --- list / get -----------------------------------------------------------------

func TestListReviewsMineIsApplicantScoped(t *testing.T) {
	store, svc := reviewFixture(t)
	if _, _, err := svc.ListReviews(context.Background(), reviewApplicant, "mine", "", 1, 20); err != nil {
		t.Fatal(err)
	}
	f := store.review.listFilter
	if f.ApplicantUID != "user-1" {
		t.Errorf("ApplicantUID = %q, want user-1", f.ApplicantUID)
	}
	if f.SpaceID != "space-a" {
		t.Errorf("SpaceID = %q; mode=mine must still carry the Space predicate", f.SpaceID)
	}
}

// mode=space is the reviewer queue and must be refused to a plain member.
func TestListReviewsSpaceRequiresReviewerRole(t *testing.T) {
	store, svc := reviewFixture(t)
	_, _, err := svc.ListReviews(context.Background(), reviewApplicant, "space", "", 1, 20)
	if !errors.Is(err, ErrReviewForbidden) {
		t.Fatalf("member error = %v, want ErrReviewForbidden", err)
	}
	if store.review.listFilter.SpaceID != "" {
		t.Error("forbidden list reached the repository")
	}
	if _, _, err := svc.ListReviews(context.Background(), reviewAdmin, "space", "", 1, 20); err != nil {
		t.Fatalf("admin error = %v, want nil", err)
	}
	if got := store.review.listFilter.ApplicantUID; got != "" {
		t.Errorf("ApplicantUID = %q; mode=space must not filter by applicant", got)
	}
}

func TestListReviewsRejectsUnknownMode(t *testing.T) {
	_, svc := reviewFixture(t)
	if _, _, err := svc.ListReviews(context.Background(), reviewApplicant, "everything", "", 1, 20); !errors.Is(err, ErrReviewInvalid) {
		t.Fatalf("error = %v, want ErrReviewInvalid", err)
	}
	if _, _, err := svc.ListReviews(context.Background(), reviewApplicant, "", "", 1, 20); !errors.Is(err, ErrReviewInvalid) {
		t.Fatalf("empty mode error = %v, want ErrReviewInvalid", err)
	}
}

// Reviewers see every request in the Space; applicants only their own. The flag
// drives the repository predicate, so getting it wrong is a disclosure bug.
func TestGetReviewPassesReviewerFlag(t *testing.T) {
	store, svc := reviewFixture(t)
	if _, err := svc.GetReview(context.Background(), reviewApplicant, "review-1"); err != nil {
		t.Fatal(err)
	}
	if store.review.snapReviewer {
		t.Error("member was treated as a reviewer")
	}
	if _, err := svc.GetReview(context.Background(), reviewAdmin, "review-1"); err != nil {
		t.Fatal(err)
	}
	if !store.review.snapReviewer {
		t.Error("admin was not treated as a reviewer")
	}
}

// readme_content is the body a reviewer actually decides on. Nothing populating
// it means every reviewer approves a blank page.
func TestGetReviewPopulatesReadmeFromTheFrozenSnapshot(t *testing.T) {
	store, svc := reviewFixture(t)
	store.review.stored.PluginJSON = json.RawMessage(reviewPackage)
	store.review.stored.ManifestJSON = json.RawMessage(`{"description":"manifest fallback"}`)
	out, err := svc.GetReview(context.Background(), reviewAdmin, "review-1")
	if err != nil {
		t.Fatal(err)
	}
	if out.ReadmeContent != "# Frozen body" {
		t.Fatalf("readme_content = %q, want the frozen SKILL.md", out.ReadmeContent)
	}
}

// A package with no markdown entry (a connector) still needs a body, and the
// manifest description is the only text there is.
func TestGetReviewFallsBackToTheManifestDescription(t *testing.T) {
	store, svc := reviewFixture(t)
	store.review.stored.PluginJSON = json.RawMessage(`{"attachments":[]}`)
	store.review.stored.ManifestJSON = json.RawMessage(`{"description":"manifest fallback"}`)
	out, err := svc.GetReview(context.Background(), reviewAdmin, "review-1")
	if err != nil {
		t.Fatal(err)
	}
	if out.ReadmeContent != "manifest fallback" {
		t.Fatalf("readme_content = %q", out.ReadmeContent)
	}
}

// --- decisions --------------------------------------------------------------------

func TestApproveRejectRequireReviewerRole(t *testing.T) {
	store, svc := reviewFixture(t)
	if _, err := svc.ApproveReview(context.Background(), reviewApplicant, "review-1"); !errors.Is(err, ErrReviewForbidden) {
		t.Fatalf("approve as member = %v, want ErrReviewForbidden", err)
	}
	if err := svc.RejectReview(context.Background(), reviewApplicant, "review-1", "no"); !errors.Is(err, ErrReviewForbidden) {
		t.Fatalf("reject as member = %v, want ErrReviewForbidden", err)
	}
	if store.review.approveParams.ReviewID != "" || store.review.rejectParams.ReviewID != "" {
		t.Error("forbidden decision reached the repository")
	}
}

// The role must be the caller's role in THIS Space. Caller.SpaceRole is resolved
// per request for the Space being acted in, so an owner of another Space arrives
// here as a member.
func TestReviewerRoleComesFromTheCallerNotTheNotifier(t *testing.T) {
	store, svc := reviewFixture(t)
	// A notifier that would answer "owner" for anyone must not be consulted on the
	// web path: otherwise review is broken wherever IM is unconfigured, and
	// authorized by the wrong source wherever it is.
	svc = svc.WithNotify(&fakeNotifier{enabled: true, role: roleOf(SpaceRoleOwner)}, nil)
	if _, err := svc.ApproveReview(context.Background(), reviewApplicant, "review-1"); !errors.Is(err, ErrReviewForbidden) {
		t.Fatalf("approve = %v; the notifier granted the web path a role", err)
	}
	if store.review.approveParams.ReviewID != "" {
		t.Error("notifier-granted approval reached the repository")
	}
}

// A system admin outranks the Space model.
func TestSystemAdminIsAlwaysAReviewer(t *testing.T) {
	_, svc := reviewFixture(t)
	sysadmin := Caller{UID: "root", Name: "Root", SpaceID: "space-a", SpaceRole: SpaceRoleMember, IsSystemAdmin: true}
	if _, err := svc.ApproveReview(context.Background(), sysadmin, "review-1"); err != nil {
		t.Fatalf("system admin approve = %v", err)
	}
}

func TestApproveReviewStampsWebDecisionSource(t *testing.T) {
	store, svc := reviewFixture(t)
	space := "space-a"
	// What the approve transaction commits: the plugin is now org-visible.
	store.review.approved = &model.Plugin{
		ID: "plugin-1", Name: "Demo", Type: model.PluginTypeSkill,
		OwnerUID: "user-1", SpaceID: &space, Visibility: model.PluginVisibilitySpace,
	}
	out, err := svc.ApproveReview(context.Background(), reviewAdmin, "review-1")
	if err != nil {
		t.Fatal(err)
	}
	// The service must hand back the committed row as-is. Anything that re-derived
	// or clamped visibility on the way out would report a listing the market does
	// not have — the repository is the only authority on what approval wrote.
	if out == nil || out.Visibility != model.PluginVisibilitySpace {
		t.Fatalf("approved plugin = %+v, want visibility=space", out)
	}
	p := store.review.approveParams
	if p.ReviewerUID != "admin-1" || p.ReviewerName != "Adam" {
		t.Errorf("reviewer = %q/%q", p.ReviewerUID, p.ReviewerName)
	}
	if p.DecisionSource != model.ReviewDecisionSourceWeb {
		t.Errorf("decision source = %q, want web", p.DecisionSource)
	}
	if p.RequestID != "request-2" {
		t.Errorf("request id = %q, want the caller's", p.RequestID)
	}
	if store.review.approveScope.SpaceID != "space-a" {
		t.Errorf("approve scope = %+v", store.review.approveScope)
	}
}

// A web reject must carry an operator-supplied reason: the applicant needs to
// know what to fix, and only the IM path substitutes a default.
func TestRejectReviewRequiresReason(t *testing.T) {
	store, svc := reviewFixture(t)
	for _, reason := range []string{"", "   ", "\t\n"} {
		if err := svc.RejectReview(context.Background(), reviewAdmin, "review-1", reason); !errors.Is(err, ErrReasonRequired) {
			t.Fatalf("reason %q error = %v, want ErrReasonRequired", reason, err)
		}
	}
	if store.review.rejectParams.ReviewID != "" {
		t.Error("reasonless reject reached the repository")
	}
	if err := svc.RejectReview(context.Background(), reviewAdmin, "review-1", "  needs docs  "); err != nil {
		t.Fatal(err)
	}
	if got := store.review.rejectParams.Reason; got != "needs docs" {
		t.Errorf("reason = %q, want trimmed", got)
	}
	if store.review.rejectParams.DecisionSource != model.ReviewDecisionSourceWeb {
		t.Errorf("decision source = %q, want web", store.review.rejectParams.DecisionSource)
	}
}

func TestRejectReviewBoundsReasonLength(t *testing.T) {
	_, svc := reviewFixture(t)
	if err := svc.RejectReview(context.Background(), reviewAdmin, "review-1", strings.Repeat("蟹", 1001)); !errors.Is(err, ErrReviewInvalid) {
		t.Fatalf("error = %v, want ErrReviewInvalid", err)
	}
}

// Cancel is the applicant's own withdrawal; the repository enforces ownership, so
// the service must hand it the caller's uid rather than a reviewer identity, and
// must not gate it on the reviewer role.
func TestCancelReviewPassesCallerUIDAndNeedsNoRole(t *testing.T) {
	store, svc := reviewFixture(t)
	if err := svc.CancelReview(context.Background(), reviewApplicant, "review-1"); err != nil {
		t.Fatal(err)
	}
	if store.review.cancelUID != "user-1" || store.review.cancelReviewID != "review-1" {
		t.Fatalf("cancel(%q,%q)", store.review.cancelReviewID, store.review.cancelUID)
	}
	if store.review.cancelScope.SpaceID != "space-a" {
		t.Errorf("cancel scope = %+v", store.review.cancelScope)
	}
}

// An already-decided request must be a conflict, not a 404 that tells the
// applicant their just-approved request vanished.
func TestCancelReviewSurfacesConflict(t *testing.T) {
	store, svc := reviewFixture(t)
	store.review.cancelErr = pluginrepo.ErrConflict
	if err := svc.CancelReview(context.Background(), reviewApplicant, "review-1"); !errors.Is(err, ErrConflict) {
		t.Fatalf("error = %v, want ErrConflict", err)
	}
}

// --- card protocol ------------------------------------------------------------------

// Our status vocabulary and the card protocol's differ; emitting ours makes
// octo-server reject the entire response as an invalid enum.
func TestCardStateMapsToProtocolEnum(t *testing.T) {
	tests := []struct {
		status model.ReviewStatus
		want   string
	}{
		{model.ReviewStatusPending, "pending"},
		{model.ReviewStatusApproved, "approved"},
		{model.ReviewStatusRejected, "denied"},
		{model.ReviewStatusCanceled, "cancelled"},
		{model.ReviewStatus("nonsense"), "pending"},
	}
	for _, tt := range tests {
		if got := cardState(tt.status); got != tt.want {
			t.Errorf("cardState(%q) = %q, want %q", tt.status, got, tt.want)
		}
	}
}

func TestDecideReviewFromCardValidatesInput(t *testing.T) {
	tests := []struct {
		name                              string
		eventID, operator, decision, rvid string
		wantErr                           error
	}{
		{name: "missing event id", operator: "admin-1", decision: "approve", rvid: "review-1", wantErr: ErrCardBadDecision},
		{name: "missing operator", eventID: "1", decision: "approve", rvid: "review-1", wantErr: ErrCardBadDecision},
		{name: "missing review id", eventID: "1", operator: "admin-1", decision: "approve", wantErr: ErrCardBadDecision},
		{name: "unknown decision", eventID: "1", operator: "admin-1", decision: "obliterate", rvid: "review-1", wantErr: ErrCardBadDecision},
		{name: "empty decision", eventID: "1", operator: "admin-1", rvid: "review-1", wantErr: ErrCardBadDecision},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, svc := reviewFixture(t)
			_, err := svc.DecideReviewFromCard(context.Background(), tt.eventID, tt.operator, tt.decision, tt.rvid)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

// A replayed event_id must return the stored response verbatim without touching
// the review again — at-least-once delivery must not double-apply a decision.
func TestDecideReviewFromCardReplaysStoredResponse(t *testing.T) {
	store, svc := reviewFixture(t)
	store.review.receipt = &model.CardActionReceipt{
		EventID:        "42",
		ReviewID:       "review-1",
		StoredResponse: `{"disposition":"applied","state":"approved","requester_uid":"user-1"}`,
	}
	out, err := svc.DecideReviewFromCard(context.Background(), "42", "admin-1", "approve", "review-1")
	if err != nil {
		t.Fatal(err)
	}
	if out.Disposition != "applied" || out.State != "approved" || out.Requester != "user-1" {
		t.Fatalf("replayed result = %+v", out)
	}
	if store.review.approveParams.ReviewID != "" {
		t.Error("replay re-applied the decision")
	}
	if len(store.review.receiptInserts) != 0 {
		t.Error("replay wrote a second receipt")
	}
}

// The receipt is keyed on event_id alone, but the response it stores describes ONE
// review. If octo-server ever reuses an event_id across two card actions (or a
// caller crafts one), replaying the stored answer would report the wrong review's
// outcome as if it were this one's. The review_id in the signed path is
// cross-checked against the receipt for exactly that reason.
func TestDecideReviewFromCardRefusesAReceiptForADifferentReview(t *testing.T) {
	store, svc := reviewFixture(t)
	store.review.receipt = &model.CardActionReceipt{
		EventID:        "42",
		ReviewID:       "review-OTHER",
		StoredResponse: `{"disposition":"applied","state":"approved","requester_uid":"user-1"}`,
	}
	out, err := svc.DecideReviewFromCard(context.Background(), "42", "admin-1", "approve", "review-1")
	if !errors.Is(err, ErrCardBadDecision) {
		t.Fatalf("mismatched receipt = (%+v, %v), want ErrCardBadDecision", out, err)
	}
	if store.review.approveParams.ReviewID != "" {
		t.Error("the mismatched replay applied a decision")
	}
	if len(store.review.receiptInserts) != 0 {
		t.Error("the mismatched replay wrote a receipt")
	}
}

// operator_uid is an identity assertion, not a grant. A uid that is no longer an
// admin is refused in-band.
func TestDecideReviewFromCardRefusesNonAdminOperator(t *testing.T) {
	for _, tt := range []struct {
		name string
		role *int
	}{
		{name: "not a member at all", role: nil},
		{name: "plain member", role: roleOf(SpaceRoleMember)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store, svc := reviewFixture(t)
			store.review.anySpaceReq = &model.PluginReviewRequest{
				ID: "review-1", PluginID: "plugin-1", SpaceID: "space-a",
				Status: model.ReviewStatusPending, ApplicantUID: "user-1", PluginName: "Demo",
			}
			svc = svc.WithNotify(&fakeNotifier{enabled: true, role: tt.role}, nil)
			out, err := svc.DecideReviewFromCard(context.Background(), "42", "stranger", "approve", "review-1")
			if err != nil {
				t.Fatal(err)
			}
			if out.Disposition != "forbidden" || out.State != "pending" {
				t.Fatalf("result = %+v, want forbidden/pending", out)
			}
			if store.review.approveParams.ReviewID != "" {
				t.Error("unverified operator mutated the review")
			}
			if len(store.review.receiptInserts) != 0 {
				t.Error("refusal wrote a receipt; a later legitimate retry would be swallowed")
			}
		})
	}
}

// THE defect this whole distinction exists for: "octo-server is unreachable" and
// "you are not an admin" are not the same answer. A 200 forbidden is acked and
// never redelivered, so a transient lookup failure reported that way silently
// discards a real admin's click.
func TestDecideReviewFromCardRetriesWhenTheRoleLookupFails(t *testing.T) {
	for _, tt := range []struct {
		name     string
		notifier *fakeNotifier
	}{
		{name: "lookup error", notifier: &fakeNotifier{enabled: true, roleErr: errors.New("octo-server down")}},
		{name: "lookup unconfigured", notifier: &fakeNotifier{enabled: false}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store, svc := reviewFixture(t)
			store.review.anySpaceReq = &model.PluginReviewRequest{
				ID: "review-1", PluginID: "plugin-1", SpaceID: "space-a",
				Status: model.ReviewStatusPending, ApplicantUID: "user-1", PluginName: "Demo",
			}
			svc = svc.WithNotify(tt.notifier, nil)
			out, err := svc.DecideReviewFromCard(context.Background(), "42", "admin-1", "approve", "review-1")
			if err == nil {
				t.Fatalf("result = %+v, want a retryable error", out)
			}
			if out != nil {
				t.Fatalf("a fault must not also produce a body: %+v", out)
			}
			if len(store.review.receiptInserts) != 0 {
				t.Error("a retryable fault wrote a receipt, so the retry would replay it")
			}
		})
	}
}

// An already-decided request reports the authoritative terminal state so the
// second admin's card renders correctly instead of the event being retried.
func TestDecideReviewFromCardReportsAlreadyDecided(t *testing.T) {
	store, svc := reviewFixture(t)
	store.review.anySpaceReq = &model.PluginReviewRequest{
		ID: "review-1", PluginID: "plugin-1", SpaceID: "space-a",
		Status: model.ReviewStatusRejected, ApplicantUID: "user-1", PluginName: "Demo",
	}
	svc = svc.WithNotify(&fakeNotifier{enabled: true, role: roleOf(SpaceRoleOwner)}, nil)

	out, err := svc.DecideReviewFromCard(context.Background(), "42", "admin-1", "approve", "review-1")
	if err != nil {
		t.Fatal(err)
	}
	if out.Disposition != "conflict" {
		t.Fatalf("disposition = %q, want conflict", out.Disposition)
	}
	if out.State != "denied" {
		t.Fatalf("state = %q, want the protocol spelling of rejected", out.State)
	}
	// approved/denied are the states octo-server's decoder requires a requester on
	// (cardactiondispatch/contract.go); cardConflict reaches both, so it must carry
	// one or the callback response is rejected and the event redelivered.
	if out.Requester != "user-1" {
		t.Fatalf("requester_uid = %q, want the applicant", out.Requester)
	}
	if store.review.approveParams.ReviewID != "" {
		t.Error("already-decided request was mutated")
	}
}

// A vanished review is terminal, not a server fault: reporting it in-band stops
// octo-server from retrying the event into the DLQ.
//
// The state is `cancelled`, not `pending`. All three request-is-gone branches
// (this lookup, the apply-time ErrNotFound, and cardConflict's re-read) report one
// physical condition, so a card renderer keyed on `state` must not see two
// different answers for it; and every path that removes the plugin cancels its
// request in the same transaction, so cancelled is the accurate one. No requester
// is knowable here — the row that holds the applicant is the row that is gone —
// which is legal precisely because cancelled is outside octo-server's
// requester-required set.
func TestDecideReviewFromCardReportsMissingReview(t *testing.T) {
	store, svc := reviewFixture(t)
	store.review.anySpaceErr = pluginrepo.ErrNotFound
	out, err := svc.DecideReviewFromCard(context.Background(), "42", "admin-1", "approve", "review-gone")
	if err != nil {
		t.Fatal(err)
	}
	if out.Disposition != "not_found" || out.State != "cancelled" {
		t.Fatalf("result = %+v, want not_found/cancelled", out)
	}
	if len(store.review.receiptInserts) != 0 {
		t.Error("missing review wrote a receipt")
	}
}

// An IM deny cannot carry a typed reason (the card template has no text input),
// so the server substitutes the documented default and stamps the source.
func TestDecideReviewFromCardDenyUsesDefaultReasonAndIMSource(t *testing.T) {
	store, svc := reviewFixture(t)
	store.review.anySpaceReq = &model.PluginReviewRequest{
		ID: "review-1", PluginID: "plugin-1", SpaceID: "space-a",
		Status: model.ReviewStatusPending, ApplicantUID: "user-1", PluginName: "Demo",
	}
	svc = svc.WithNotify(&fakeNotifier{enabled: true, role: roleOf(SpaceRoleAdmin)}, nil)

	out, err := svc.DecideReviewFromCard(context.Background(), "42", "admin-1", "deny", "review-1")
	if err != nil {
		t.Fatal(err)
	}
	if out.Disposition != "applied" || out.State != "denied" {
		t.Fatalf("result = %+v", out)
	}
	p := store.review.rejectParams
	if p.Reason != model.DefaultIMDenyReason {
		t.Errorf("reason = %q, want the default IM deny reason", p.Reason)
	}
	if p.DecisionSource != model.ReviewDecisionSourceIM {
		t.Errorf("decision source = %q, want im", p.DecisionSource)
	}
	// The receipt is what makes a redelivery idempotent.
	if len(store.review.receiptInserts) != 1 {
		t.Fatalf("receipts written = %d, want 1", len(store.review.receiptInserts))
	}
	rec := store.review.receiptInserts[0]
	if rec.EventID != "42" || rec.ReviewID != "review-1" || rec.Decision != "deny" {
		t.Errorf("receipt = %+v", rec)
	}
	var stored CardActionResult
	if err := json.Unmarshal([]byte(rec.StoredResponse), &stored); err != nil {
		t.Fatalf("stored response not decodable: %v", err)
	}
	if stored.State != "denied" || stored.Requester != "user-1" {
		t.Errorf("stored response = %+v", stored)
	}
}

// Two concurrent IM clicks with distinct event_ids: the loser must be told the
// committed outcome, re-read so it reflects the WINNER's decision rather than
// the stale row this callback started from.
func TestDecideReviewFromCardLoserSeesCommittedOutcome(t *testing.T) {
	store, svc := reviewFixture(t)
	store.review.anySpaceReq = &model.PluginReviewRequest{
		ID: "review-1", PluginID: "plugin-1", SpaceID: "space-a",
		Status: model.ReviewStatusPending, ApplicantUID: "user-1", PluginName: "Demo",
	}
	// The re-read after losing the CAS sees the winner's terminal row.
	store.review.anySpaceSecond = &model.PluginReviewRequest{
		ID: "review-1", PluginID: "plugin-1", SpaceID: "space-a",
		Status: model.ReviewStatusApproved, ApplicantUID: "user-1", PluginName: "Demo",
	}
	store.review.approveErr = pluginrepo.ErrConflict
	svc = svc.WithNotify(&fakeNotifier{enabled: true, role: roleOf(SpaceRoleOwner)}, nil)

	out, err := svc.DecideReviewFromCard(context.Background(), "99", "admin-2", "approve", "review-1")
	if err != nil {
		t.Fatalf("loser returned a hard error: %v", err)
	}
	if out.Disposition != "conflict" {
		t.Fatalf("disposition = %q, want conflict", out.Disposition)
	}
	if out.State != "approved" {
		t.Fatalf("state = %q, want the winner's committed outcome", out.State)
	}
	if out.Requester != "user-1" {
		t.Errorf("requester_uid = %q", out.Requester)
	}
	if len(store.review.receiptInserts) != 0 {
		t.Error("the loser wrote a receipt for a decision it did not make")
	}
}

// --- submitted content --------------------------------------------------------

// reviewValidManifest/Package are a canonicalizable document pair for the skill
// fixture, so submitted content goes through the real write-path validation.
func reviewValidDocs(name string) (manifest, pkg string) {
	manifest = `{"$schema":"cowork-plugin-manifest-2.0.json","plugin_name":"Demo","plugin_type":"skill","name":"demo","description":"` + name + `","labels":[],"examples":[]}`
	pkg = `{"$schema":"cowork-plugin-package-2.0.json","attachments":[{"path":"SKILL.md","content_type":"raw","mime_type":"text/markdown","raw_content":"# ` + name + `"}]}`
	return manifest, pkg
}

// For an already-listed plugin the live row IS what the org reads, so freezing it
// would make the review a formality over content that already shipped. Keyed on
// listing_state, not visibility: a space-intent DRAFT also has visibility=space
// and must still be allowed to submit contentlessly.
func TestSubmitReviewRequiresContentForAnUpgrade(t *testing.T) {
	store, svc := reviewFixture(t)
	store.plugins["plugin-1"].Visibility = model.PluginVisibilitySpace
	store.plugins["plugin-1"].ListingState = model.PluginListingStatePublished
	_, err := svc.SubmitReview(context.Background(), reviewApplicant, ReviewSubmitParams{PluginID: "plugin-1", Version: "2.0.0"})
	if !errors.Is(err, ErrReviewContentRequired) {
		t.Fatalf("error = %v, want ErrReviewContentRequired", err)
	}
	if store.review.insertReq != nil {
		t.Error("a contentless upgrade reached the repository")
	}
}

// TestSubmitReviewAllowsAContentlessSpaceIntentDraft is the twin of the test
// above and the reason it had to be re-keyed: an author who declares 仅本组织可见
// on a draft submits a row that already carries visibility=space. Under the old
// visibility test that submit was refused as "a listed plugin with no content",
// which is every first org listing.
func TestSubmitReviewAllowsAContentlessSpaceIntentDraft(t *testing.T) {
	store, svc := reviewFixture(t)
	store.plugins["plugin-1"].Visibility = model.PluginVisibilitySpace
	store.plugins["plugin-1"].ListingState = model.PluginListingStateDraft
	if _, err := svc.SubmitReview(context.Background(), reviewApplicant, ReviewSubmitParams{PluginID: "plugin-1", Version: "1.0.0"}); err != nil {
		t.Fatalf("a space-intent draft could not submit its own row: %v", err)
	}
	if store.review.insertSnap.PluginHash != "sha256:p" {
		t.Fatalf("snapshot = %+v; a contentless first listing snapshots the draft row", store.review.insertSnap)
	}
}

// A private draft is nobody else's business, so snapshotting the row is honest
// and content stays optional.
func TestSubmitReviewAllowsAContentlessFirstListing(t *testing.T) {
	store, svc := reviewFixture(t)
	if _, err := svc.SubmitReview(context.Background(), reviewApplicant, ReviewSubmitParams{PluginID: "plugin-1", Version: "1.0.0"}); err != nil {
		t.Fatal(err)
	}
	if store.review.insertSnap.PluginHash != "sha256:p" {
		t.Fatalf("snapshot = %+v; a contentless first listing snapshots the draft row", store.review.insertSnap)
	}
}

// Half a document set cannot be reviewed, and filling the other half from the
// live row would smuggle unreviewed content into the snapshot.
func TestSubmitReviewRejectsPartialContent(t *testing.T) {
	manifest, pkg := reviewValidDocs("v2")
	for _, tt := range []struct {
		name             string
		manifest, pkgStr string
	}{
		{name: "manifest only", manifest: manifest},
		{name: "package only", pkgStr: pkg},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store, svc := reviewFixture(t)
			store.plugins["plugin-1"].Visibility = model.PluginVisibilitySpace
			_, err := svc.SubmitReview(context.Background(), reviewApplicant, ReviewSubmitParams{
				PluginID: "plugin-1", Version: "2.0.0",
				Manifest: json.RawMessage(tt.manifest), Package: json.RawMessage(tt.pkgStr),
			})
			if err == nil {
				t.Fatal("expected error for partial content")
			}
			// Partial content surfaces as a field error on manifest_json (or
			// another validation error); the exact sentinel has changed from
			// plain ErrReviewInvalid to a structured *ReviewFieldError, but it
			// must still be a 400-class validation error and must not reach the
			// repository.
			if errors.Is(err, ErrConflict) || errors.Is(err, ErrNotFound) {
				t.Fatalf("error = %v, want a validation error", err)
			}
			if store.review.insertReq != nil {
				t.Error("a partial submission reached the repository")
			}
		})
	}
}

// The submitted content is frozen on the REQUEST. Nothing about the plugin row
// changes at submit time — that is the whole point of the upgrade flow.
func TestSubmitReviewFreezesSubmittedContentWithoutTouchingThePlugin(t *testing.T) {
	store, svc := reviewFixture(t)
	store.plugins["plugin-1"].Visibility = model.PluginVisibilitySpace
	store.plugins["plugin-1"].Name = "Demo"
	store.plugins["plugin-1"].Tags = json.RawMessage(`[]`)
	beforeManifest := string(store.plugins["plugin-1"].Manifest)
	beforePackage := string(store.plugins["plugin-1"].Package)
	beforeHash := store.plugins["plugin-1"].PluginHash

	manifest, pkg := reviewValidDocs("v2 body")
	if _, err := svc.SubmitReview(context.Background(), reviewApplicant, ReviewSubmitParams{
		PluginID: "plugin-1", Version: "2.0.0",
		Manifest: json.RawMessage(manifest), Package: json.RawMessage(pkg),
	}); err != nil {
		t.Fatal(err)
	}
	snap := store.review.insertSnap
	if !strings.Contains(string(snap.Package), "v2 body") {
		t.Fatalf("snapshot package = %s, want the submitted content", snap.Package)
	}
	if snap.PluginHash == beforeHash {
		t.Fatal("snapshot hash equals the live hash; the submitted content was ignored")
	}
	// Submit is read-only on the plugin.
	if store.update != nil || store.create != nil {
		t.Fatal("submit wrote to the plugin row")
	}
	live := store.plugins["plugin-1"]
	if string(live.Manifest) != beforeManifest || string(live.Package) != beforePackage || live.PluginHash != beforeHash {
		t.Fatal("the plugin row changed at submit time; the org would see unreviewed content")
	}
}

// A malformed manifest must be a 400 at submit, not a surprise when a reviewer
// clicks approve.
func TestSubmitReviewValidatesSubmittedContent(t *testing.T) {
	_, pkg := reviewValidDocs("v2")
	for _, tt := range []struct {
		name             string
		manifest, pkgStr string
	}{
		{name: "manifest is not an object", manifest: `[]`, pkgStr: pkg},
		{name: "manifest fails the contract", manifest: `{"nonsense":true}`, pkgStr: pkg},
		{name: "manifest name does not match the plugin row", pkgStr: pkg,
			manifest: `{"$schema":"cowork-plugin-manifest-2.0.json","plugin_name":"Something Else","plugin_type":"skill","name":"demo","description":"d","labels":[],"examples":[]}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store, svc := reviewFixture(t)
			store.plugins["plugin-1"].Visibility = model.PluginVisibilitySpace
			store.plugins["plugin-1"].Tags = json.RawMessage(`[]`)
			_, err := svc.SubmitReview(context.Background(), reviewApplicant, ReviewSubmitParams{
				PluginID: "plugin-1", Version: "2.0.0",
				Manifest: json.RawMessage(tt.manifest), Package: json.RawMessage(tt.pkgStr),
			})
			if err == nil {
				t.Fatal("malformed content was accepted")
			}
			if store.review.insertReq != nil {
				t.Error("malformed content reached the repository")
			}
		})
	}
}

// Relations follow target-state semantics when submitted, and are INHERITED when
// the field is absent — a client editing only documents must not be able to empty
// an expert team by forgetting the key.
func TestSubmitReviewRelationsAreTargetStateButOptional(t *testing.T) {
	space := "space-a"
	newFixture := func(t *testing.T) (*fakeStore, *Service) {
		t.Helper()
		store, svc := reviewFixture(t)
		store.plugins["plugin-1"].Type = model.PluginTypeExpert
		store.plugins["plugin-1"].Visibility = model.PluginVisibilitySpace
		store.plugins["skill-1"] = &model.Plugin{
			ID: "skill-1", Name: "Bundled", Type: model.PluginTypeSkill,
			OwnerUID: "user-1", SpaceID: &space, Visibility: model.PluginVisibilityPrivate,
		}
		store.relations["plugin-1"] = []model.PluginRelation{
			{ID: "rel-1", SourcePluginID: "plugin-1", TargetPluginID: "skill-1", Type: "expert_skill", Status: 1},
		}
		return store, svc
	}
	manifest := `{"$schema":"cowork-plugin-manifest-2.0.json","plugin_name":"Demo","plugin_type":"expert","name":"demo","description":"d","labels":[],"examples":[]}`
	pkg := `{"$schema":"cowork-plugin-package-2.0.json","attachments":[{"path":"AGENTS.md","content_type":"raw","mime_type":"text/markdown","raw_content":"# demo"}]}`

	t.Run("absent inherits the live graph", func(t *testing.T) {
		store, svc := newFixture(t)
		if _, err := svc.SubmitReview(context.Background(), reviewApplicant, ReviewSubmitParams{
			PluginID: "plugin-1", Version: "2.0.0",
			Manifest: json.RawMessage(manifest), Package: json.RawMessage(pkg),
		}); err != nil {
			t.Fatal(err)
		}
		rels := store.review.insertSnap.Relations
		if len(rels) != 1 || rels[0].TargetPluginID != "skill-1" {
			t.Fatalf("relations = %+v; an omitted field must inherit the live graph", rels)
		}
	})
	t.Run("explicit empty clears the graph", func(t *testing.T) {
		store, svc := newFixture(t)
		empty := []RelationRequest{}
		if _, err := svc.SubmitReview(context.Background(), reviewApplicant, ReviewSubmitParams{
			PluginID: "plugin-1", Version: "2.0.0",
			Manifest: json.RawMessage(manifest), Package: json.RawMessage(pkg),
			Relations: &empty,
		}); err != nil {
			t.Fatal(err)
		}
		if got := store.review.insertSnap.Relations; len(got) != 0 {
			t.Fatalf("relations = %+v, want empty", got)
		}
	})
	t.Run("an invalid relation is refused", func(t *testing.T) {
		store, svc := newFixture(t)
		bad := []RelationRequest{{TargetPluginID: "skill-1", Type: "expert_team_expert"}}
		if _, err := svc.SubmitReview(context.Background(), reviewApplicant, ReviewSubmitParams{
			PluginID: "plugin-1", Version: "2.0.0",
			Manifest: json.RawMessage(manifest), Package: json.RawMessage(pkg),
			Relations: &bad,
		}); !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("error = %v, want ErrInvalidRequest", err)
		}
		if store.review.insertReq != nil {
			t.Error("an invalid relation reached the repository")
		}
	})
}

// The icon column holds a storage object key for an uploaded icon; handing that
// to a browser is a 404. It must go through the same resolveIcon path the plugin
// list uses, on BOTH the list and the detail read.
func TestReviewIconIsResolvedToADisplayURL(t *testing.T) {
	store, _ := reviewFixture(t)
	blobs := &importStorage{objects: map[string][]byte{}}
	svc := New(store, blobs).WithRuntime(func() string { return "review-new" }, func() time.Time {
		return time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC)
	})
	store.review.stored.PluginIcon = "icons/demo.png"
	store.review.stored.PluginJSON = json.RawMessage(reviewPackage)
	store.review.listItems = []*model.PluginReviewRequest{
		{ID: "review-1", PluginID: "plugin-1", SpaceID: "space-a", PluginIcon: "icons/demo.png"},
	}
	store.review.listTotal = 1

	detail, err := svc.GetReview(context.Background(), reviewAdmin, "review-1")
	if err != nil {
		t.Fatal(err)
	}
	if detail.PluginIcon == "icons/demo.png" {
		t.Fatal("detail plugin_icon is the raw storage key; it will 404 in the browser")
	}
	if !strings.HasPrefix(detail.PluginIcon, "https://cdn.invalid/") {
		t.Fatalf("detail plugin_icon = %q, want a resolved URL", detail.PluginIcon)
	}
	items, _, err := svc.ListReviews(context.Background(), reviewAdmin, "space", "", 1, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].PluginIcon == "icons/demo.png" {
		t.Fatalf("list plugin_icon = %+v; the raw storage key leaked", items)
	}
	// An http(s) icon and an empty one pass through untouched.
	store.review.stored.PluginIcon = "https://cdn.example.com/i.png"
	if out, err := svc.GetReview(context.Background(), reviewAdmin, "review-1"); err != nil || out.PluginIcon != "https://cdn.example.com/i.png" {
		t.Fatalf("absolute icon = %q (%v)", out.PluginIcon, err)
	}
}

// ErrVersionRegressed from the approve/reject repo call is PERMANENT: the
// plugin's current_version moved past the frozen label between card delivery
// and click, and nothing can lower it back. It must NOT be returned as an
// error (which would surface as HTTP 503 and cause octo-server to redeliver
// forever); instead it is a terminal in-band refusal so the card renders an
// actionable reason and the applicant can cancel+resubmit.
//
// Disposition trade-off: we reuse "forbidden" rather than inventing a value.
// None of the four documented enums (applied/replayed/conflict/not_found)
// fits cleanly — the request is still pending (no other admin decided it),
// was not applied, is not a replay of an existing receipt, and is not
// not_found — so "forbidden" (the existing terminal-refusal slot) is the
// closest fit. display.reason carries a machine-readable actionable label
// ("version moved past; resubmit required") for a future richer renderer; if
// octo-server later adds a dedicated "stale" disposition this branch can be
// switched over without wire changes elsewhere.
func TestDecideReviewFromCardVersionRegressedIsATerminalRefusal(t *testing.T) {
	store, svc := reviewFixture(t)
	store.review.anySpaceReq = &model.PluginReviewRequest{
		ID: "review-1", PluginID: "plugin-1", SpaceID: "space-a",
		Status: model.ReviewStatusPending, ApplicantUID: "user-1", PluginName: "Demo",
	}
	store.review.approveErr = pluginrepo.ErrVersionRegressed
	svc = svc.WithNotify(&fakeNotifier{enabled: true, role: roleOf(SpaceRoleOwner)}, nil)

	out, err := svc.DecideReviewFromCard(context.Background(), "77", "admin-1", "approve", "review-1")
	if err != nil {
		t.Fatalf("version regressed returned an error (would become 503 and loop forever): %v", err)
	}
	if out == nil {
		t.Fatal("expected a handled CardActionResult, got nil")
	}
	if out.Disposition != "forbidden" {
		t.Errorf("disposition = %q, want forbidden (terminal refusal slot)", out.Disposition)
	}
	if out.State != "pending" {
		t.Errorf("state = %q, want pending (request was NOT settled by this click)", out.State)
	}
	if out.Requester != "user-1" {
		t.Errorf("requester_uid = %q, want the applicant so octo-server can DM them", out.Requester)
	}
	if got := out.Display["reason"]; got != "version_moved_past_resubmit_required" {
		t.Errorf("display.reason = %q, want actionable resubmit label", got)
	}
	if out.Display["title"] != "Demo" {
		t.Errorf("display.title = %q", out.Display["title"])
	}
	// No receipt is written for a refused click — cancel+resubmit generates a
	// fresh review_id/card, and writing a receipt here would permanently
	// swallow any retry against a corrected version.
	if len(store.review.receiptInserts) != 0 {
		t.Error("refusal wrote a receipt, so a later retry against a corrected version would replay as this refusal")
	}
}

// Regression guard: ErrDeadlock MUST stay retryable. A deadlock-victim abort
// is transient — the transaction rolled back and did NOT settle the request —
// so returning a handled body would ack the event and silently discard a real
// admin's decision. The handler turns this error into HTTP 503 so octo-server
// redelivers.
func TestDecideReviewFromCardDeadlockStaysRetryable(t *testing.T) {
	store, svc := reviewFixture(t)
	store.review.anySpaceReq = &model.PluginReviewRequest{
		ID: "review-1", PluginID: "plugin-1", SpaceID: "space-a",
		Status: model.ReviewStatusPending, ApplicantUID: "user-1", PluginName: "Demo",
	}
	store.review.approveErr = pluginrepo.ErrDeadlock
	svc = svc.WithNotify(&fakeNotifier{enabled: true, role: roleOf(SpaceRoleOwner)}, nil)

	out, err := svc.DecideReviewFromCard(context.Background(), "78", "admin-1", "approve", "review-1")
	if err == nil {
		t.Fatalf("deadlock returned a handled result %+v; want a retryable error (503)", out)
	}
	if !errors.Is(err, ErrDeadlock) {
		t.Errorf("error = %v, want ErrDeadlock so the handler maps it to 503", err)
	}
	if out != nil {
		t.Errorf("a fault must not also produce a body: %+v", out)
	}
	if len(store.review.receiptInserts) != 0 {
		t.Error("a retryable fault wrote a receipt, so the retry would replay it")
	}
}

// reReadErrorStore wraps a Store and forces GetReviewRequestAnySpace to
// return secondErr on every call after the first. This models "initial load
// succeeded, re-read inside cardConflict failed" — the exact scenario
// cardConflict used to swallow, answering with a self-contradictory
// conflict/pending 200 that octo-server would never redeliver.
type reReadErrorStore struct {
	Store
	calls     int
	secondErr error
}

func (s *reReadErrorStore) GetReviewRequestAnySpace(ctx context.Context, id string) (*model.PluginReviewRequest, error) {
	s.calls++
	if s.calls > 1 {
		return nil, s.secondErr
	}
	return s.Store.GetReviewRequestAnySpace(ctx, id)
}

// cardConflict must NOT swallow re-read errors at the pre-apply status gate
// (req.Status != pending). When it can't re-read the settled row, returning a
// stale conflict/pending body is self-contradictory (conflict means already
// settled, but the body says pending) AND is a 200 that octo-server will
// never redeliver. It must propagate the error so the handler answers 503.
func TestCardConflictReReadErrorAtStatusGateIsRetryable(t *testing.T) {
	store, svc := reviewFixture(t)
	store.review.anySpaceReq = &model.PluginReviewRequest{
		ID: "review-1", PluginID: "plugin-1", SpaceID: "space-a",
		Status: model.ReviewStatusRejected, ApplicantUID: "user-1", PluginName: "Demo",
	}
	svc = svc.WithNotify(&fakeNotifier{enabled: true, role: roleOf(SpaceRoleOwner)}, nil)
	svc.repo = &reReadErrorStore{Store: svc.repo, secondErr: errors.New("transient db failure")}

	out, err := svc.DecideReviewFromCard(context.Background(), "79", "admin-1", "approve", "review-1")
	if err == nil {
		t.Fatalf("cardConflict on re-read failure returned a stale result %+v; want a retryable error (503)", out)
	}
	if out != nil {
		t.Errorf("a fault must not also produce a body: %+v", out)
	}
}

// Same invariant on the CAS-loss branch: after ApproveReview returns
// ErrConflict, a failed re-read must propagate as an error, not a stale
// conflict/pending body.
func TestCardConflictReReadErrorAfterCasLossIsRetryable(t *testing.T) {
	store, svc := reviewFixture(t)
	store.review.anySpaceReq = &model.PluginReviewRequest{
		ID: "review-1", PluginID: "plugin-1", SpaceID: "space-a",
		Status: model.ReviewStatusPending, ApplicantUID: "user-1", PluginName: "Demo",
	}
	store.review.approveErr = pluginrepo.ErrConflict
	svc = svc.WithNotify(&fakeNotifier{enabled: true, role: roleOf(SpaceRoleOwner)}, nil)
	wrapped := &reReadErrorStore{Store: svc.repo, secondErr: errors.New("transient db failure")}
	svc.repo = wrapped

	out, err := svc.DecideReviewFromCard(context.Background(), "80", "admin-1", "approve", "review-1")
	if err == nil {
		t.Fatalf("cardConflict after CAS loss with re-read failure returned a stale result %+v; want a retryable error (503)", out)
	}
	if out != nil {
		t.Errorf("a fault must not also produce a body: %+v", out)
	}
	if wrapped.calls < 2 {
		t.Errorf("expected at least two AnySpace calls (initial load + cardConflict re-read), got %d", wrapped.calls)
	}
}

// ErrLabelTaken must reach the card path as a TERMINAL refusal, not as a
// conflict. The repository split it out of ErrConflict precisely because
// cardConflict treats ErrConflict as "another admin already settled this" and
// renders the request as decided — which for a label collision is false (the row
// is still pending) and discards the admin's click with no way to retry. Reverting
// this arm to the cardConflict route is the mutation this test exists to catch.
func TestDecideReviewFromCardLabelTakenIsATerminalRefusal(t *testing.T) {
	store, svc := reviewFixture(t)
	store.review.anySpaceReq = &model.PluginReviewRequest{
		ID: "review-1", PluginID: "plugin-1", SpaceID: "space-a",
		Status: model.ReviewStatusPending, ApplicantUID: "user-1", PluginName: "Demo",
	}
	store.review.approveErr = pluginrepo.ErrLabelTaken
	svc = svc.WithNotify(&fakeNotifier{enabled: true, role: roleOf(SpaceRoleOwner)}, nil)

	out, err := svc.DecideReviewFromCard(context.Background(), "81", "admin-1", "approve", "review-1")
	if err != nil {
		t.Fatalf("label collision returned an error (would become 503 and loop forever): %v", err)
	}
	if out == nil {
		t.Fatal("expected a handled CardActionResult, got nil")
	}
	if out.Disposition != "forbidden" || out.State != "pending" {
		t.Fatalf("result = %+v, want forbidden/pending; conflict/<settled> would tell the admin someone else decided a request that is still open", out)
	}
	if got := out.Display["reason"]; got != "version_label_taken_resubmit_required" {
		t.Errorf("display.reason = %q, want the label-collision label distinguishable from version_moved_past", got)
	}
	if out.Requester != "user-1" {
		t.Errorf("requester_uid = %q, want the applicant", out.Requester)
	}
	if len(store.review.receiptInserts) != 0 {
		t.Error("refusal wrote a receipt, so a retry after the applicant resubmits would replay this refusal")
	}
}

// A plugin deleted or delisted between the request lock and the approve body
// leaves the request gone for good. The answer must be terminal — an error here
// becomes 503 and octo-server redelivers forever against a row that cannot come
// back — and it must report the same state the other two request-is-gone branches
// report, so a renderer keyed on `state` shows one thing for one fact.
func TestDecideReviewFromCardApplyTimeNotFoundIsTerminal(t *testing.T) {
	store, svc := reviewFixture(t)
	store.review.anySpaceReq = &model.PluginReviewRequest{
		ID: "review-1", PluginID: "plugin-1", SpaceID: "space-a",
		Status: model.ReviewStatusPending, ApplicantUID: "user-1", PluginName: "Demo",
	}
	store.review.approveErr = pluginrepo.ErrNotFound
	svc = svc.WithNotify(&fakeNotifier{enabled: true, role: roleOf(SpaceRoleOwner)}, nil)

	out, err := svc.DecideReviewFromCard(context.Background(), "82", "admin-1", "approve", "review-1")
	if err != nil {
		t.Fatalf("a vanished request returned an error (503 -> infinite redelivery): %v", err)
	}
	if out.Disposition != "not_found" || out.State != "cancelled" {
		t.Fatalf("result = %+v, want not_found/cancelled", out)
	}
	// Unlike the initial-lookup branch, the applicant IS known here — the request
	// was loaded before the apply — so it is carried. cancelled sits outside
	// octo-server's requester-required set, so this is belt-and-braces rather than
	// mandatory; it is pinned so the field does not silently disappear if that set
	// ever widens.
	if out.Requester != "user-1" {
		t.Errorf("requester_uid = %q, want the applicant that was already loaded", out.Requester)
	}
	if len(store.review.receiptInserts) != 0 {
		t.Error("a terminal not_found wrote a receipt")
	}
}

// Same invariant on cardConflict's re-read: ErrNotFound there is permanent and
// must not fall through to the retryable-error branch, which would loop as 503.
// The distinction matters — a re-read that fails TRANSIENTLY must still be an
// error (pinned by TestCardConflictReReadErrorAfterCasLossIsRetryable), so only
// the not-found case may short-circuit.
func TestCardConflictNotFoundOnReReadIsTerminal(t *testing.T) {
	store, svc := reviewFixture(t)
	store.review.anySpaceReq = &model.PluginReviewRequest{
		ID: "review-1", PluginID: "plugin-1", SpaceID: "space-a",
		Status: model.ReviewStatusPending, ApplicantUID: "user-1", PluginName: "Demo",
	}
	store.review.approveErr = pluginrepo.ErrConflict
	svc = svc.WithNotify(&fakeNotifier{enabled: true, role: roleOf(SpaceRoleOwner)}, nil)
	svc.repo = &reReadErrorStore{Store: svc.repo, secondErr: pluginrepo.ErrNotFound}

	out, err := svc.DecideReviewFromCard(context.Background(), "83", "admin-1", "approve", "review-1")
	if err != nil {
		t.Fatalf("a soft-deleted request on re-read returned an error (503 -> infinite redelivery): %v", err)
	}
	if out.Disposition != "not_found" || out.State != "cancelled" {
		t.Fatalf("result = %+v, want not_found/cancelled", out)
	}
	if out.Requester != "user-1" {
		t.Errorf("requester_uid = %q, want the applicant from the request already in hand", out.Requester)
	}
}
