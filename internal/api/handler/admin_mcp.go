package handler

import (
	"context"

	apiresponse "github.com/Mininglamp-OSS/octo-marketplace/internal/api/response"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/apierr"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/service"
	"github.com/gin-gonic/gin"
)

type AdminMCPService interface {
	CreateSystem(context.Context, service.Caller, model.CreateRequest) (model.Detail, *apierr.Error)
	ListSystem(context.Context, service.ListParams) (model.ListResponse, *apierr.Error)
	GetSystem(context.Context, string) (model.Detail, *apierr.Error)
	UpdateSystem(context.Context, string, model.PatchRequest) (model.Detail, *apierr.Error)
	DeleteSystem(context.Context, string) *apierr.Error
	Probe(context.Context, service.ProbeRequest) (service.ProbeResponse, *apierr.Error)
}

type AdminMCP struct{ svc AdminMCPService }

func NewAdminMCP(svc AdminMCPService) *AdminMCP { return &AdminMCP{svc: svc} }

// Create godoc
// @Summary Create system MCP server
// @Description Create a system-visible MCP server through the administrator surface.
// @Tags admin_mcp
// @ID admin_mcp.create
// @Accept json
// @Produce json
// @Security Bearer
// @Param body body model.CreateRequest true "System MCP server"
// @Success 201 {object} apiresponse.Data[model.Detail]
// @Failure 400 {object} apiresponse.Error "VALIDATION_ERROR"
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 409 {object} apiresponse.Error "DUPLICATE"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /admin/mcps [post]
func (h *AdminMCP) Create(c *gin.Context) {
	caller, ok := callerFromContext(c)
	if !ok {
		writeError(c, apierr.Unauthorized())
		return
	}
	var req model.CreateRequest
	if err := decodeJSON(c, &req); err != nil {
		writeError(c, err)
		return
	}
	detail, apiErr := h.svc.CreateSystem(c.Request.Context(), caller, req)
	if apiErr != nil {
		writeError(c, apiErr)
		return
	}
	apiresponse.Created(c, detail)
}

// List godoc
// @Summary List system MCP servers
// @Description List system-visible MCP servers using offset pagination.
// @Tags admin_mcp
// @ID admin_mcp.list
// @Accept json
// @Produce json
// @Security Bearer
// @Param keyword query string false "Search keyword"
// @Param category query string false "Category key"
// @Param transport query []string false "Transport filters"
// @Param tag query []string false "Tag filters"
// @Param sort query string false "Sort: relevance, updated"
// @Param page query int false "Page number, default 1"
// @Param page_size query int false "Page size, default 20, max 100"
// @Success 200 {object} apiresponse.OffsetList[model.ListItem]
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /admin/mcps [get]
func (h *AdminMCP) List(c *gin.Context) {
	p, page, pageSize := listParams(c)
	resp, apiErr := h.svc.ListSystem(c.Request.Context(), p)
	if apiErr != nil {
		writeError(c, apiErr)
		return
	}
	apiresponse.Offset(c, resp.Items, resp.Total, page, pageSize)
}

// Get godoc
// @Summary Get system MCP server
// @Description Return one system-visible MCP server for administration.
// @Tags admin_mcp
// @ID admin_mcp.get
// @Accept json
// @Produce json
// @Security Bearer
// @Param mcp_id path string true "MCP ID"
// @Success 200 {object} apiresponse.Data[model.Detail]
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /admin/mcps/{mcp_id} [get]
func (h *AdminMCP) Get(c *gin.Context) {
	detail, apiErr := h.svc.GetSystem(c.Request.Context(), c.Param("mcp_id"))
	if apiErr != nil {
		writeError(c, apiErr)
		return
	}
	apiresponse.OK(c, detail)
}

// Patch godoc
// @Summary Update system MCP server
// @Description Partially update one system-visible MCP server.
// @Tags admin_mcp
// @ID admin_mcp.update
// @Accept json
// @Produce json
// @Security Bearer
// @Param mcp_id path string true "MCP ID"
// @Param body body model.PatchRequest true "MCP changes"
// @Success 200 {object} apiresponse.Data[model.Detail]
// @Failure 400 {object} apiresponse.Error "VALIDATION_ERROR"
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 409 {object} apiresponse.Error "DUPLICATE"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /admin/mcps/{mcp_id} [patch]
func (h *AdminMCP) Patch(c *gin.Context) {
	var req model.PatchRequest
	if err := decodeJSON(c, &req); err != nil {
		writeError(c, err)
		return
	}
	detail, apiErr := h.svc.UpdateSystem(c.Request.Context(), c.Param("mcp_id"), req)
	if apiErr != nil {
		writeError(c, apiErr)
		return
	}
	apiresponse.OK(c, detail)
}

// Delete godoc
// @Summary Delete system MCP server
// @Description Soft-delete a system-visible MCP server.
// @Tags admin_mcp
// @ID admin_mcp.delete
// @Accept json
// @Produce json
// @Security Bearer
// @Param mcp_id path string true "MCP ID"
// @Success 200 {object} apiresponse.Data[apiresponse.EmptyResp]
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /admin/mcps/{mcp_id} [delete]
func (h *AdminMCP) Delete(c *gin.Context) {
	if apiErr := h.svc.DeleteSystem(c.Request.Context(), c.Param("mcp_id")); apiErr != nil {
		writeError(c, apiErr)
		return
	}
	apiresponse.Empty(c)
}

// Probe godoc
// @Summary Probe system MCP server
// @Description Probe a remote MCP connection through the administrator surface without persisting it.
// @Tags admin_mcp
// @ID admin_mcp.probe
// @Accept json
// @Produce json
// @Security Bearer
// @Param body body service.ProbeRequest true "Connection to probe"
// @Success 200 {object} apiresponse.Data[service.ProbeResponse]
// @Failure 400 {object} apiresponse.Error "VALIDATION_ERROR"
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /admin/mcps/_probe [post]
func (h *AdminMCP) Probe(c *gin.Context) {
	var req service.ProbeRequest
	if err := decodeJSON(c, &req); err != nil {
		writeError(c, err)
		return
	}
	resp, apiErr := h.svc.Probe(c.Request.Context(), req)
	if apiErr != nil {
		writeError(c, apiErr)
		return
	}
	if resp.Tools == nil {
		resp.Tools = []model.Tool{}
	}
	apiresponse.OK(c, resp)
}
