package plugin

import (
	"context"
	apiresponse "github.com/Mininglamp-OSS/octo-marketplace/internal/api/response"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	pluginrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/plugin"
	pluginsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/plugin"
	"github.com/gin-gonic/gin"
)

type dependencyUpdateService interface {
	DependencyUpdates(context.Context, pluginsvc.Caller, string) (*model.DependencyUpdates, error)
	SkipDependencyUpdates(context.Context, pluginsvc.Caller, string, []model.DependencyUpdateTarget) error
	SubmitDependencyUpdates(context.Context, pluginsvc.Caller, string, pluginsvc.DependencyUpdateSubmitParams) (*pluginrepo.DependencyUpdatePublishResult, error)
}
type dependencySkipResponse struct{}

type dependencySkipRequest struct {
	Targets []model.DependencyUpdateTarget `json:"targets" binding:"required,min=1,max=100,dive"`
}
type dependencySubmitRequest struct {
	ExpectedPublishedVersionID string                         `json:"expected_published_version_id" binding:"required"`
	ExpectedDraftRevision      string                         `json:"expected_draft_revision" binding:"required"`
	Targets                    []model.DependencyUpdateTarget `json:"targets" binding:"required,min=1,max=100,dive"`
	Draft                      upsertRequest                  `json:"draft" binding:"required"`
	Version                    string                         `json:"version" binding:"required"`
	Changelog                  string                         `json:"changelog" binding:"required"`
}

// DependencyUpdates lists adoptable direct formal releases, including skipped ones.
// @Summary Get direct dependency updates for an owned expert or team
// @Description Returns exact source and target released versions. Connectors report configuration changes using target_config_revision; Skills and member Experts use target_version_id. Skipped entries remain available for manual adoption.
// @Tags plugin
// @ID plugin.dependency_update.list
// @Produce json
// @Security Bearer
// @Param plugin_id path string true "Plugin ID"
// @Success 200 {object} apiresponse.Data[model.DependencyUpdates]
// @Failure 400 {object} apiresponse.Error "VALIDATION_ERROR"
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 409 {object} apiresponse.Error "CONFLICT"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /plugins/{plugin_id}/dependency_updates [get]
func (h *Handler) DependencyUpdates(c *gin.Context) {
	who, ok := caller(c)
	if !ok {
		unauthorized(c)
		return
	}
	svc, ok := h.svc.(dependencyUpdateService)
	if !ok {
		writeServiceError(c, pluginsvc.ErrNotFound, "plugin.dependency_update")
		return
	}
	out, err := svc.DependencyUpdates(c.Request.Context(), who, c.Param("plugin_id"))
	if err != nil {
		writeServiceError(c, err, "plugin.dependency_update")
		return
	}
	c.Header("Cache-Control", "private, no-store")
	apiresponse.OK(c, out)
}

// SkipDependencyUpdates persists only the explicitly selected reminder tuples.
// @Summary Skip selected dependency release reminders
// @Description Stores authenticated per-user and per-resource skip records atomically without changing drafts, reviews, content or references.
// @Tags plugin
// @ID plugin.dependency_update.skip
// @Accept json
// @Produce json
// @Security Bearer
// @Param plugin_id path string true "Plugin ID"
// @Param body body dependencySkipRequest true "Dependency target versions"
// @Success 200 {object} apiresponse.Data[dependencySkipResponse]
// @Failure 400 {object} apiresponse.Error "VALIDATION_ERROR"
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 409 {object} apiresponse.Error "CONFLICT"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /plugins/{plugin_id}/dependency_updates/_skip [post]
func (h *Handler) SkipDependencyUpdates(c *gin.Context) {
	who, ok := caller(c)
	if !ok {
		unauthorized(c)
		return
	}
	var body dependencySkipRequest
	if !decode(c, &body) {
		return
	}
	if !pluginrepo.ValidUpdateTargets(body.Targets) {
		validation(c, "targets")
		return
	}
	svc, ok := h.svc.(dependencyUpdateService)
	if !ok {
		writeServiceError(c, pluginsvc.ErrNotFound, "plugin.dependency_update")
		return
	}
	if err := svc.SkipDependencyUpdates(c.Request.Context(), who, c.Param("plugin_id"), body.Targets); err != nil {
		writeServiceError(c, err, "plugin.dependency_update")
		return
	}
	apiresponse.OK(c, map[string]any{})
}

// SubmitDependencyUpdates adopts exact pins and submits their new root version.
// @Summary Adopt dependency releases and submit a new resource version
// @Description Saves the complete draft and publishes or opens organization review in one conditional database transaction. Stale source or draft revisions and unavailable targets fail without partial changes.
// @Tags plugin
// @ID plugin.dependency_update.create
// @Accept json
// @Produce json
// @Security Bearer
// @Param plugin_id path string true "Plugin ID"
// @Param body body dependencySubmitRequest true "Conditional draft and target versions"
// @Success 200 {object} apiresponse.Data[publishResponse]
// @Failure 400 {object} apiresponse.Error "VALIDATION_ERROR"
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 409 {object} apiresponse.Error "CONFLICT"
// @Failure 413 {object} apiresponse.Error "PAYLOAD_TOO_LARGE"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /plugins/{plugin_id}/dependency_updates [post]
func (h *Handler) SubmitDependencyUpdates(c *gin.Context) {
	who, ok := caller(c)
	if !ok {
		unauthorized(c)
		return
	}
	var body dependencySubmitRequest
	if !decode(c, &body) {
		return
	}
	if !pluginrepo.ValidUpdateTargets(body.Targets) {
		validation(c, "targets")
		return
	}
	svc, ok := h.svc.(dependencyUpdateService)
	if !ok {
		writeServiceError(c, pluginsvc.ErrNotFound, "plugin.dependency_update")
		return
	}
	out, err := svc.SubmitDependencyUpdates(c.Request.Context(), who, c.Param("plugin_id"), pluginsvc.DependencyUpdateSubmitParams{ExpectedPublishedVersionID: body.ExpectedPublishedVersionID, ExpectedDraftRevision: body.ExpectedDraftRevision, Targets: body.Targets, Draft: body.Draft.serviceRequest(), Version: body.Version, Changelog: body.Changelog})
	if err != nil {
		writeServiceError(c, err, "plugin.dependency_update")
		return
	}
	resp := publishResponse{PluginID: out.Plugin.ID, ListingState: out.Plugin.ListingState, DisplayStatus: out.Plugin.DisplayStatus(out.Review != nil && out.Review.Status == model.ReviewStatusPending, "")}
	if out.Review != nil {
		resp.ReviewID = &out.Review.ID
	}
	apiresponse.OK(c, resp)
}
