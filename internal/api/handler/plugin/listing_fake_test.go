package plugin

import (
	"context"

	pluginsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/plugin"
)

// Listing half of fakeService: the publish/delist wire contract. The routing
// decision (private lists immediately, space opens a review) is a SERVICE rule
// with its own tests; what these record is what the handler passed down and what
// it rendered from the result.
type listingServiceFake struct {
	publishParams pluginsvc.PublishParams
	delistParams  pluginsvc.DelistParams

	publishResult *pluginsvc.PublishResult
	delistResult  *pluginsvc.Detail

	publishErr error
	delistErr  error
}

func (f *fakeService) Publish(_ context.Context, c pluginsvc.Caller, p pluginsvc.PublishParams) (*pluginsvc.PublishResult, error) {
	f.caller, f.listing.publishParams = c, p
	if f.listing.publishErr != nil {
		return nil, f.listing.publishErr
	}
	return f.listing.publishResult, nil
}

func (f *fakeService) Delist(_ context.Context, c pluginsvc.Caller, p pluginsvc.DelistParams) (*pluginsvc.Detail, error) {
	f.caller, f.listing.delistParams = c, p
	if f.listing.delistErr != nil {
		return nil, f.listing.delistErr
	}
	return f.listing.delistResult, nil
}
