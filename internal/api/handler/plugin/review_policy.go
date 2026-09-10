package plugin

import (
	"github.com/gin-gonic/gin"

	apiresponse "github.com/Mininglamp-OSS/octo-marketplace/internal/api/response"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

type reviewPolicyResponse = model.PluginReviewPolicy

type updateReviewPolicyRequest struct {
	IsAutoApproveEnabled *bool `json:"is_auto_approve_enabled" binding:"required"`
}

// GetReviewPolicy godoc
// @Summary Get the authenticated Space plugin review policy
// @Description Returns the effective policy for the authenticated Space. A Space without a stored override has automatic approval enabled.
// @Tags plugin
// @ID plugin_review_policy.get
// @Produce json
// @Security Bearer
// @Success 200 {object} apiresponse.Data[reviewPolicyResponse]
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /plugin_review_policies [get]
func (h *Handler) GetReviewPolicy(c *gin.Context) {
	caller, ok := caller(c)
	if !ok {
		unauthorized(c)
		return
	}
	policy, err := h.svc.GetReviewPolicy(c.Request.Context(), caller)
	if err != nil {
		writeServiceError(c, err, "plugin_review_policy.get")
		return
	}
	apiresponse.OK(c, policy)
}

// UpdateReviewPolicy godoc
// @Summary Update the authenticated Space plugin review policy
// @Description Space owners and admins may update the one shared policy for their Space. Existing pending requests are unchanged.
// @Tags plugin
// @ID plugin_review_policy.update
// @Accept json
// @Produce json
// @Security Bearer
// @Param body body updateReviewPolicyRequest true "Effective automatic approval setting"
// @Success 200 {object} apiresponse.Data[reviewPolicyResponse]
// @Failure 400 {object} apiresponse.Error "VALIDATION_ERROR"
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN: Space owner or admin required"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /plugin_review_policies [patch]
func (h *Handler) UpdateReviewPolicy(c *gin.Context) {
	caller, ok := caller(c)
	if !ok {
		unauthorized(c)
		return
	}
	var req updateReviewPolicyRequest
	if !decode(c, &req) {
		return
	}
	if req.IsAutoApproveEnabled == nil {
		validation(c, "is_auto_approve_enabled")
		return
	}
	policy, err := h.svc.UpdateReviewPolicy(c.Request.Context(), caller, *req.IsAutoApproveEnabled)
	if err != nil {
		writeServiceError(c, err, "plugin_review_policy.update")
		return
	}
	apiresponse.OK(c, policy)
}
