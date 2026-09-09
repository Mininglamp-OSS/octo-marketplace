package plugin

import (
	"context"
	"encoding/json"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/notify"
	pluginrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/plugin"
)

// Review half of fakeStore, kept in its own file so the review tests own their
// recording fields without churning the pre-existing fake.
//
// These are recorders, not a MySQL emulator. The single-pending constraint, the
// status CAS, the frozen-relation application and the approve transaction are
// repository guarantees, covered against a real database in internal/db. What
// service tests assert is which scope/params the service passes down, which
// callers it refuses before the repository is reached, and how it maps errors.
type reviewFake struct {
	// Recorded inputs.
	insertScope    pluginrepo.Scope
	insertReq      *model.PluginReviewRequest
	insertSnap     pluginrepo.FrozenSnapshot
	listScope      pluginrepo.Scope
	listFilter     pluginrepo.ReviewListFilter
	snapshotScope  pluginrepo.Scope
	snapReviewer   bool
	getReviewer    bool
	approveParams  pluginrepo.ApproveReviewParams
	approveScope   pluginrepo.Scope
	rejectParams   pluginrepo.RejectReviewParams
	cancelReviewID string
	cancelUID      string
	cancelScope    pluginrepo.Scope
	receiptInserts []*model.CardActionReceipt
	anySpaceCalls  int
	pendingScope   pluginrepo.Scope
	pendingPlugin  string
	pendingCalls   int
	latestScope    pluginrepo.Scope
	latestPlugin   string

	// Canned outputs.
	stored      *model.PluginReviewRequest
	listItems   []*model.PluginReviewRequest
	listTotal   int64
	approved    *model.Plugin
	receipt     *model.CardActionReceipt
	anySpaceReq *model.PluginReviewRequest
	// anySpaceSecond, when set, is returned by the SECOND and later AnySpace
	// reads, so a test can model "the row changed under us" (a lost race).
	anySpaceSecond *model.PluginReviewRequest
	hasPending     bool
	latestID       string
	latestStatus   model.ReviewStatus

	insertErr    error
	getErr       error
	listErr      error
	approveErr   error
	rejectErr    error
	cancelErr    error
	receiptErr   error
	anySpaceErr  error
	insertRecErr error
	pendingErr   error
	latestErr    error
}

func (f *fakeStore) HasPendingReview(_ context.Context, s pluginrepo.Scope, pluginID string) (bool, error) {
	f.review.pendingScope = s
	f.review.pendingPlugin = pluginID
	f.review.pendingCalls++
	if f.review.pendingErr != nil {
		return false, f.review.pendingErr
	}
	return f.review.hasPending, nil
}

func (f *fakeStore) LatestReviewForPlugin(_ context.Context, s pluginrepo.Scope, pluginID string) (string, model.ReviewStatus, error) {
	f.review.latestScope = s
	f.review.latestPlugin = pluginID
	if f.review.latestErr != nil {
		return "", "", f.review.latestErr
	}
	return f.review.latestID, f.review.latestStatus, nil
}

// listingFake records the two listing_state writers. Like the review half these
// are recorders: the state CAS, the placement self-heal and the pending-review
// cancellation are repository guarantees covered against a real database in
// internal/db.
type listingFake struct {
	publishScope  pluginrepo.Scope
	publishParams pluginrepo.PublishParams
	publishCalls  int
	delistScope   pluginrepo.Scope
	delistParams  pluginrepo.DelistParams
	delistCalls   int

	published  *model.Plugin
	delisted   *model.Plugin
	publishErr error
	delistErr  error
}

func (f *fakeStore) PublishPlugin(_ context.Context, s pluginrepo.Scope, p pluginrepo.PublishParams) (*model.Plugin, error) {
	f.listing.publishScope = s
	f.listing.publishParams = p
	f.listing.publishCalls++
	if f.listing.publishErr != nil {
		return nil, f.listing.publishErr
	}
	if f.listing.published != nil {
		return f.listing.published, nil
	}
	// Mirror just enough of the repository: the row comes back listed.
	out := f.plugins[p.PluginID]
	if out == nil {
		return nil, pluginrepo.ErrNotFound
	}
	clone := *out
	clone.ListingState = model.PluginListingStatePublished
	return &clone, nil
}

func (f *fakeStore) DelistPlugin(_ context.Context, s pluginrepo.Scope, p pluginrepo.DelistParams) (*model.Plugin, error) {
	f.listing.delistScope = s
	f.listing.delistParams = p
	f.listing.delistCalls++
	if f.listing.delistErr != nil {
		return nil, f.listing.delistErr
	}
	if f.listing.delisted != nil {
		return f.listing.delisted, nil
	}
	out := f.plugins[p.PluginID]
	if out == nil {
		return nil, pluginrepo.ErrNotFound
	}
	clone := *out
	clone.ListingState = model.PluginListingStateDelisted
	return &clone, nil
}

func (f *fakeStore) InsertReviewRequest(_ context.Context, s pluginrepo.Scope, req *model.PluginReviewRequest, snap pluginrepo.FrozenSnapshot) error {
	f.review.insertScope = s
	f.review.insertReq = req
	f.review.insertSnap = snap
	if f.review.insertErr != nil {
		return f.review.insertErr
	}
	// The repository assigns the id and derives kind; mirror just enough of that
	// so the service's follow-up read-back has something to key on.
	if req.ID == "" {
		req.ID = "review-new"
	}
	if req.Kind == "" {
		req.Kind = model.ReviewKindFirst
	}
	return nil
}

func (f *fakeStore) GetReviewRequest(_ context.Context, s pluginrepo.Scope, _ string, isReviewer bool) (*model.PluginReviewRequest, error) {
	f.review.snapshotScope = s
	f.review.getReviewer = isReviewer
	if f.review.getErr != nil {
		return nil, f.review.getErr
	}
	return f.review.stored, nil
}

func (f *fakeStore) LoadReviewSnapshot(_ context.Context, s pluginrepo.Scope, _ string, isReviewer bool) (*model.PluginReviewRequest, error) {
	f.review.snapshotScope = s
	f.review.snapReviewer = isReviewer
	if f.review.getErr != nil {
		return nil, f.review.getErr
	}
	return f.review.stored, nil
}

func (f *fakeStore) ListReviewRequests(_ context.Context, s pluginrepo.Scope, filter pluginrepo.ReviewListFilter) ([]*model.PluginReviewRequest, int64, error) {
	f.review.listScope = s
	f.review.listFilter = filter
	if f.review.listErr != nil {
		return nil, 0, f.review.listErr
	}
	return f.review.listItems, f.review.listTotal, nil
}

func (f *fakeStore) ApproveReview(_ context.Context, s pluginrepo.Scope, p pluginrepo.ApproveReviewParams) (*model.Plugin, error) {
	f.review.approveScope = s
	f.review.approveParams = p
	if f.review.approveErr != nil {
		return nil, f.review.approveErr
	}
	if f.review.approved == nil {
		return &model.Plugin{ID: p.ReviewID}, nil
	}
	return f.review.approved, nil
}

func (f *fakeStore) RejectReview(_ context.Context, _ pluginrepo.Scope, p pluginrepo.RejectReviewParams) (json.RawMessage, map[string]struct{}, error) {
	f.review.rejectParams = p
	return nil, nil, f.review.rejectErr
}

func (f *fakeStore) CancelReview(_ context.Context, s pluginrepo.Scope, reviewID, callerUID string) (json.RawMessage, map[string]struct{}, error) {
	f.review.cancelScope = s
	f.review.cancelReviewID = reviewID
	f.review.cancelUID = callerUID
	return nil, nil, f.review.cancelErr
}

func (f *fakeStore) GetReviewRequestAnySpace(_ context.Context, _ string) (*model.PluginReviewRequest, error) {
	f.review.anySpaceCalls++
	if f.review.anySpaceErr != nil {
		return nil, f.review.anySpaceErr
	}
	if f.review.anySpaceCalls > 1 && f.review.anySpaceSecond != nil {
		return f.review.anySpaceSecond, nil
	}
	return f.review.anySpaceReq, nil
}

func (f *fakeStore) GetCardActionReceipt(_ context.Context, _ string) (*model.CardActionReceipt, error) {
	if f.review.receiptErr != nil {
		return nil, f.review.receiptErr
	}
	return f.review.receipt, nil
}

func (f *fakeStore) InsertCardActionReceipt(_ context.Context, rec *model.CardActionReceipt) error {
	f.review.receiptInserts = append(f.review.receiptInserts, rec)
	return f.review.insertRecErr
}

// fakeNotifier stands in for *notify.Client. `roleErr` models the case the
// review path must never conflate with a refusal: the lookup itself failed.
type fakeNotifier struct {
	enabled   bool
	role      *int
	roleErr   error
	notifyIn  []notify.NotifyRequest
	resp      *notify.NotifyResponse
	notifyErr error
}

func (f *fakeNotifier) Enabled() bool { return f.enabled }

func (f *fakeNotifier) MemberRole(context.Context, string, string) (*int, error) {
	if f.roleErr != nil {
		return nil, f.roleErr
	}
	return f.role, nil
}

func (f *fakeNotifier) NotifySpaceAdmins(_ context.Context, req notify.NotifyRequest) (*notify.NotifyResponse, error) {
	f.notifyIn = append(f.notifyIn, req)
	if f.notifyErr != nil {
		return nil, f.notifyErr
	}
	if f.resp == nil {
		return &notify.NotifyResponse{Delivered: []string{"admin-1"}, Filtered: map[string]string{}}, nil
	}
	return f.resp, nil
}

func roleOf(v int) *int { return &v }

// syncBestEffort runs the post-commit hook inline so a test can observe the
// dispatch deterministically. Production uses notify.BestEffort, which detaches.
func syncBestEffort(calls *[]string) func(string, func(context.Context) error) {
	return func(desc string, fn func(context.Context) error) {
		*calls = append(*calls, desc)
		_ = fn(context.Background())
	}
}
