package plugin

import (
	"github.com/gin-gonic/gin"

	apiresponse "github.com/Mininglamp-OSS/octo-marketplace/internal/api/response"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	pluginsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/plugin"
)

type publishRequest struct {
	PluginID string `json:"plugin_id"`
	// Version and Changelog are used only when the publish needs organization
	// review; both are ignored for a 仅自己可见 Plugin, which lists immediately.
	// Version defaults to the draft's current version.
	Version   string `json:"version"`
	Changelog string `json:"changelog"`
}

type publishResponse struct {
	PluginID      string                    `json:"plugin_id"`
	ListingState  model.PluginListingState  `json:"listing_state"`
	DisplayStatus model.PluginDisplayStatus `json:"display_status"`
	// ReviewID is present only when the publish opened a review request, which is
	// how the client knows which branch fired without a second call.
	ReviewID *string `json:"review_id,omitempty"`
}

type delistRequest struct {
	PluginID string `json:"plugin_id"`
	Reason   string `json:"reason"`
}

type delistResponse struct {
	PluginID      string                    `json:"plugin_id"`
	ListingState  model.PluginListingState  `json:"listing_state"`
	DisplayStatus model.PluginDisplayStatus `json:"display_status"`
}

// Publish godoc
// @Summary Publish plugin
// @Description Publish a caller-owned Plugin. The Plugin's visibility decides what that means: a private Plugin is listed immediately, while an organization-visible Plugin opens a review request and stays a draft until a Space admin approves it. There is no separate submit-for-review endpoint.
// @Tags plugin
// @ID plugin.publish
// @Accept json
// @Produce json
// @Security Bearer
// @Param body body publishRequest true "Plugin ID, with an optional version label and changelog for the review branch"
// @Success 200 {object} apiresponse.Data[publishResponse]
// @Failure 400 {object} apiresponse.Error "VALIDATION_ERROR"
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 409 {object} apiresponse.Error "CONFLICT"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /plugins/publish [post]
//
// No 403 is declared. Publish is owner-only with no role gate — a non-owner gets
// 404 rather than 403, because confirming that a Plugin exists to somebody who
// cannot see it is a leak. Declaring an unreachable status would only make
// clients write dead code.
func (h *Handler) Publish(c *gin.Context) {
	caller, ok := caller(c)
	if !ok {
		unauthorized(c)
		return
	}
	var req publishRequest
	if !decode(c, &req) {
		return
	}
	result, err := h.svc.Publish(c.Request.Context(), caller, pluginsvc.PublishParams{
		PluginID:  req.PluginID,
		Version:   req.Version,
		Changelog: req.Changelog,
	})
	if err != nil {
		writeServiceError(c, err, "plugin.publish")
		return
	}
	out := publishResponse{
		PluginID:     result.Plugin.Plugin.ID,
		ListingState: result.Plugin.Plugin.ListingState,
	}
	if result.Review != nil {
		out.ReviewID = &result.Review.ID
		out.DisplayStatus = result.Plugin.Plugin.DisplayStatus(true, result.Review.Status)
	} else {
		out.DisplayStatus = result.Plugin.Plugin.DisplayStatus(false, "")
	}
	apiresponse.OK(c, out)
}

// Delist godoc
// @Summary Delist plugin
// @Description Take a published Plugin out of the marketplace. Requires the Space owner or admin role — the same authority as approving a review request. Only an organization-visible, standalone Plugin can be delisted: a private Plugin is not organization content and an embedded child is listed and un-listed by its container, so both answer 404 rather than confirming they exist. Any pending review request on the Plugin is canceled. The Plugin stays editable and can be published again by its owner.
// @Tags plugin
// @ID plugin.delist
// @Accept json
// @Produce json
// @Security Bearer
// @Param body body delistRequest true "Plugin ID and an optional reason"
// @Success 200 {object} apiresponse.Data[delistResponse]
// @Failure 400 {object} apiresponse.Error "VALIDATION_ERROR"
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 409 {object} apiresponse.Error "CONFLICT"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /plugins/delist [post]
func (h *Handler) Delist(c *gin.Context) {
	caller, ok := caller(c)
	if !ok {
		unauthorized(c)
		return
	}
	var req delistRequest
	if !decode(c, &req) {
		return
	}
	detail, err := h.svc.Delist(c.Request.Context(), caller, pluginsvc.DelistParams{
		PluginID: req.PluginID,
		Reason:   req.Reason,
	})
	if err != nil {
		writeServiceError(c, err, "plugin.delist")
		return
	}
	apiresponse.OK(c, delistResponse{
		PluginID:     detail.Plugin.ID,
		ListingState: detail.Plugin.ListingState,
		// Delist cancels any pending request in the same transaction, so there is
		// never an open review to fold in here.
		DisplayStatus: detail.Plugin.DisplayStatus(false, ""),
	})
}
