package plugin

import (
	"context"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	pluginsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/plugin"
)

// Review half of fakeService. Kept in its own file so the review handler tests
// own their recording fields without churning the pre-existing fake.
type reviewServiceFake struct {
	submitParams pluginsvc.ReviewSubmitParams
	listMode     string
	listStatus   model.ReviewStatus
	listPage     int
	listPageSize int
	getID        string
	approveID    string
	rejectID     string
	rejectReason string
	cancelID     string

	request  *model.PluginReviewRequest
	items    []*model.PluginReviewRequest
	total    int64
	approved *model.Plugin

	submitErr  error
	listErr    error
	getErr     error
	approveErr error
	rejectErr  error
	cancelErr  error
}

func (f *fakeService) SubmitReview(_ context.Context, c pluginsvc.Caller, p pluginsvc.ReviewSubmitParams) (*model.PluginReviewRequest, error) {
	f.caller, f.review.submitParams = c, p
	if f.review.submitErr != nil {
		return nil, f.review.submitErr
	}
	return f.review.request, nil
}

func (f *fakeService) ListReviews(_ context.Context, c pluginsvc.Caller, mode string, status model.ReviewStatus, page, pageSize int) ([]*model.PluginReviewRequest, int64, error) {
	f.caller = c
	f.review.listMode, f.review.listStatus = mode, status
	f.review.listPage, f.review.listPageSize = page, pageSize
	if f.review.listErr != nil {
		return nil, 0, f.review.listErr
	}
	return f.review.items, f.review.total, nil
}

func (f *fakeService) GetReview(_ context.Context, c pluginsvc.Caller, reviewID string) (*model.PluginReviewRequest, error) {
	f.caller, f.review.getID = c, reviewID
	if f.review.getErr != nil {
		return nil, f.review.getErr
	}
	return f.review.request, nil
}

func (f *fakeService) ApproveReview(_ context.Context, c pluginsvc.Caller, reviewID string) (*model.Plugin, error) {
	f.caller, f.review.approveID = c, reviewID
	if f.review.approveErr != nil {
		return nil, f.review.approveErr
	}
	return f.review.approved, nil
}

func (f *fakeService) RejectReview(_ context.Context, c pluginsvc.Caller, reviewID, reason string) error {
	f.caller, f.review.rejectID, f.review.rejectReason = c, reviewID, reason
	return f.review.rejectErr
}

func (f *fakeService) CancelReview(_ context.Context, c pluginsvc.Caller, reviewID string) error {
	f.caller, f.review.cancelID = c, reviewID
	return f.review.cancelErr
}

// fakeCardActionService records what the signed callback handed the service.
type fakeCardActionService struct {
	calls  int
	event  string
	oper   string
	choice string
	review string

	result *pluginsvc.CardActionResult
	err    error
}

func (f *fakeCardActionService) DecideReviewFromCard(_ context.Context, eventID, operatorUID, decision, reviewID string) (*pluginsvc.CardActionResult, error) {
	f.calls++
	f.event, f.oper, f.choice, f.review = eventID, operatorUID, decision, reviewID
	if f.err != nil {
		return nil, f.err
	}
	return f.result, nil
}
