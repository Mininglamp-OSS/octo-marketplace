package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"

	apiresponse "github.com/Mininglamp-OSS/octo-marketplace/internal/api/response"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/apierr"
	marketmiddleware "github.com/Mininglamp-OSS/octo-marketplace/internal/middleware"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/service"
	"github.com/gin-gonic/gin"
)

const (
	maxBodyBytes     = 8 << 20
	maxIconBytes     = 2 << 20
	bytesReaderSlack = 1 << 20
)

type MCPService interface {
	Create(context.Context, service.Caller, model.CreateRequest) (model.Detail, *apierr.Error)
	Get(context.Context, service.Caller, string) (model.Detail, *apierr.Error)
	Patch(context.Context, service.Caller, string, model.PatchRequest) (model.Detail, *apierr.Error)
	Delete(context.Context, service.Caller, string) *apierr.Error
	List(context.Context, service.Caller, service.ListParams) (model.ListResponse, *apierr.Error)
	ListMine(context.Context, service.Caller, service.ListParams) (model.ListResponse, *apierr.Error)
	Probe(context.Context, service.ProbeRequest) (service.ProbeResponse, *apierr.Error)
	UploadIcon(context.Context, service.Caller, string, []byte, string) (service.IconResult, *apierr.Error)
}

type MCP struct{ svc MCPService }

func NewMCP(svc MCPService) *MCP { return &MCP{svc: svc} }

// Create godoc
// @Summary Create MCP server
// @Description Create an MCP server owned by the authenticated user in the current Space.
// @Tags mcp
// @ID mcp.create
// @Accept json
// @Produce json
// @Security Bearer
// @Param body body model.CreateRequest true "MCP server"
// @Success 201 {object} apiresponse.Data[model.Detail]
// @Failure 400 {object} apiresponse.Error "VALIDATION_ERROR"
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 409 {object} apiresponse.Error "DUPLICATE"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /mcps [post]
func (h *MCP) Create(c *gin.Context) {
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
	detail, apiErr := h.svc.Create(c.Request.Context(), caller, req)
	if apiErr != nil {
		writeError(c, apiErr)
		return
	}
	apiresponse.Created(c, detail)
}

// List godoc
// @Summary List MCP servers
// @Description List MCP servers visible in the current Space using offset pagination.
// @Tags mcp
// @ID mcp.list
// @Accept json
// @Produce json
// @Security Bearer
// @Param keyword query string false "Search keyword"
// @Param category query string false "Category key"
// @Param transport query []string false "Transport filters"
// @Param visibility query []string false "Visibility filters"
// @Param source query []string false "Source filters: system, space, mine"
// @Param created_by_type query []string false "Provenance filter (human, bot, import); repeatable or comma-separated"
// @Param tag query []string false "Tag filters"
// @Param sort query string false "Sort: relevance, updated"
// @Param page query int false "Page number, default 1"
// @Param page_size query int false "Page size, default 20, max 100"
// @Success 200 {object} apiresponse.OffsetList[model.ListItem]
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /mcps [get]
func (h *MCP) List(c *gin.Context) { h.list(c, false) }

// ListMine godoc
// @Summary List owned MCP servers
// @Description List MCP servers owned by the authenticated user in the current Space.
// @Tags mcp
// @ID mcp.mine.list
// @Accept json
// @Produce json
// @Security Bearer
// @Param keyword query string false "Search keyword"
// @Param category query string false "Category key"
// @Param transport query []string false "Transport filters"
// @Param visibility query []string false "Visibility filters"
// @Param source query []string false "Source filters: system, space, mine"
// @Param created_by_type query []string false "Provenance filter (human, bot, import); repeatable or comma-separated"
// @Param tag query []string false "Tag filters"
// @Param sort query string false "Sort: relevance, updated"
// @Param page query int false "Page number, default 1"
// @Param page_size query int false "Page size, default 20, max 100"
// @Success 200 {object} apiresponse.OffsetList[model.ListItem]
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /mcps/mine [get]
func (h *MCP) ListMine(c *gin.Context) { h.list(c, true) }

// ListCategories godoc
// @Summary List MCP categories
// @Description Return MCP category keys and visible record counts for the current Space.
// @Tags mcp
// @ID mcp_category.list
// @Accept json
// @Produce json
// @Security Bearer
// @Param mode query string false "Scope: \"mine\" restricts counts to caller-owned records"
// @Param created_by_type query []string false "Provenance filter (human, bot, import); repeatable or comma-separated"
// @Success 200 {object} apiresponse.Data[[]model.CategoryFilter]
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /mcp_categories [get]
func (h *MCP) ListCategories(c *gin.Context) {
	caller, ok := callerFromContext(c)
	if !ok {
		writeError(c, apierr.Unauthorized())
		return
	}
	// Categories share the SAME predicates as the list endpoints so counts
	// stay coherent when the two filters combine (issue #894 follow-up: a
	// user narrowing to "Bot 创建" expects every category pill to shrink
	// accordingly). Only `created_by_type` is honoured today — the other
	// filters (keyword/tags/…) are still ignored by design because clicking
	// a pill would zero itself out otherwise.
	params := service.ListParams{
		CreatedByTypes: splitQuery(c.QueryArray("created_by_type")),
	}
	var result model.ListResponse
	var apiErr *apierr.Error
	if c.Query("mode") == "mine" {
		result, apiErr = h.svc.ListMine(c.Request.Context(), caller, params)
	} else {
		result, apiErr = h.svc.List(c.Request.Context(), caller, params)
	}
	if apiErr != nil {
		writeError(c, apiErr)
		return
	}
	apiresponse.OK(c, result.Categories)
}

func (h *MCP) list(c *gin.Context, mine bool) {
	caller, ok := callerFromContext(c)
	if !ok {
		writeError(c, apierr.Unauthorized())
		return
	}
	p, page, pageSize := listParams(c)
	var resp model.ListResponse
	var apiErr *apierr.Error
	if mine {
		resp, apiErr = h.svc.ListMine(c.Request.Context(), caller, p)
	} else {
		resp, apiErr = h.svc.List(c.Request.Context(), caller, p)
	}
	if apiErr != nil {
		writeError(c, apiErr)
		return
	}
	apiresponse.Offset(c, resp.Items, resp.Total, page, pageSize)
}

// Get godoc
// @Summary Get MCP server
// @Description Return one MCP server visible to the caller without exposing stored secrets.
// @Tags mcp
// @ID mcp.get
// @Accept json
// @Produce json
// @Security Bearer
// @Param mcp_id path string true "MCP ID"
// @Success 200 {object} apiresponse.Data[model.Detail]
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /mcps/{mcp_id} [get]
func (h *MCP) Get(c *gin.Context) {
	caller, ok := callerFromContext(c)
	if !ok {
		writeError(c, apierr.Unauthorized())
		return
	}
	detail, apiErr := h.svc.Get(c.Request.Context(), caller, c.Param("mcp_id"))
	if apiErr != nil {
		writeError(c, apiErr)
		return
	}
	apiresponse.OK(c, detail)
}

// Patch godoc
// @Summary Update MCP server
// @Description Partially update an MCP server owned by the authenticated user.
// @Tags mcp
// @ID mcp.update
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
// @Router /mcps/{mcp_id} [patch]
func (h *MCP) Patch(c *gin.Context) {
	caller, ok := callerFromContext(c)
	if !ok {
		writeError(c, apierr.Unauthorized())
		return
	}
	var req model.PatchRequest
	if err := decodeJSON(c, &req); err != nil {
		writeError(c, err)
		return
	}
	detail, apiErr := h.svc.Patch(c.Request.Context(), caller, c.Param("mcp_id"), req)
	if apiErr != nil {
		writeError(c, apiErr)
		return
	}
	apiresponse.OK(c, detail)
}

// Delete godoc
// @Summary Delete MCP server
// @Description Soft-delete an MCP server owned by the caller. Repeated deletion returns not found.
// @Tags mcp
// @ID mcp.delete
// @Accept json
// @Produce json
// @Security Bearer
// @Param mcp_id path string true "MCP ID"
// @Success 200 {object} apiresponse.Data[apiresponse.EmptyResp]
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /mcps/{mcp_id} [delete]
func (h *MCP) Delete(c *gin.Context) {
	caller, ok := callerFromContext(c)
	if !ok {
		writeError(c, apierr.Unauthorized())
		return
	}
	if apiErr := h.svc.Delete(c.Request.Context(), caller, c.Param("mcp_id")); apiErr != nil {
		writeError(c, apiErr)
		return
	}
	apiresponse.Empty(c)
}

// UploadIcon godoc
// @Summary Upload MCP icon
// @Description Upload and replace the icon for an MCP server owned by the caller.
// @Tags mcp
// @ID mcp.icon.upload
// @Accept multipart/form-data
// @Produce json
// @Security Bearer
// @Param mcp_id path string true "MCP ID"
// @Param file formData string true "Icon file" format(binary)
// @Success 200 {object} apiresponse.Data[service.IconResult]
// @Failure 400 {object} apiresponse.Error "VALIDATION_ERROR"
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 413 {object} apiresponse.Error "PAYLOAD_TOO_LARGE"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /mcps/{mcp_id}/icon [post]
func (h *MCP) UploadIcon(c *gin.Context) {
	caller, ok := callerFromContext(c)
	if !ok {
		writeError(c, apierr.Unauthorized())
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxIconBytes+bytesReaderSlack)
	file, header, err := c.Request.FormFile("file")
	if err != nil {
		writeError(c, apierr.InvalidRequest("missing or invalid icon file", apierr.Detail{Field: "file", Reason: "required"}))
		return
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxIconBytes+1))
	if err != nil {
		writeError(c, apierr.InvalidRequest("could not read uploaded file"))
		return
	}
	contentType := header.Header.Get("Content-Type")
	result, apiErr := h.svc.UploadIcon(c.Request.Context(), caller, c.Param("mcp_id"), data, contentType)
	if apiErr != nil {
		writeError(c, apiErr)
		return
	}
	apiresponse.OK(c, result)
}

// Probe godoc
// @Summary Probe MCP server
// @Description Probe a remote MCP server connection without persisting the supplied configuration.
// @Tags mcp
// @ID mcp.probe
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
// @Router /mcps/_probe [post]
func (h *MCP) Probe(c *gin.Context) {
	if _, ok := callerFromContext(c); !ok {
		writeError(c, apierr.Unauthorized())
		return
	}
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

func callerFromContext(c *gin.Context) (service.Caller, bool) {
	identity, ok := marketmiddleware.Identity(c)
	if !ok || identity.UID == "" {
		return service.Caller{}, false
	}
	caller := service.Caller{UID: identity.UID, Name: identity.Name, SpaceID: marketmiddleware.SpaceID(c)}
	// If the token was a Bot token, middleware.authenticateBot collapsed the
	// Bot into the owner Identity for authorization but stashed the resolved
	// BotIdentity in the context. Lift it here so the service layer can stamp
	// CreatedByType=bot on new rows (issue #894). A user-token request has no
	// BotIdentity — the two Bot fields stay empty and CreatedByType defaults
	// to human downstream.
	if bot, hasBot := marketmiddleware.BotIdentity(c); hasBot && bot.BotUID != "" {
		caller.BotUID = bot.BotUID
		caller.BotName = bot.BotName
	}
	return caller, true
}

func listParams(c *gin.Context) (service.ListParams, int, int) {
	page := positiveInt(c.Query("page"), 1)
	pageSize := positiveInt(c.Query("page_size"), 20)
	if pageSize > 100 {
		pageSize = 100
	}
	categories := splitQuery(c.QueryArray("category"))
	return service.ListParams{
		Keyword:        strings.TrimSpace(c.Query("keyword")),
		Categories:     categories,
		Tags:           splitQuery(c.QueryArray("tag")),
		Transports:     splitQuery(c.QueryArray("transport")),
		Visibilities:   splitQuery(c.QueryArray("visibility")),
		Sources:        splitQuery(c.QueryArray("source")),
		CreatedByTypes: splitQuery(c.QueryArray("created_by_type")),
		Sort:           strings.TrimSpace(c.Query("sort")),
		Limit:          pageSize,
		Offset:         (page - 1) * pageSize,
	}, page, pageSize
}

func splitQuery(values []string) []string {
	var result []string
	for _, value := range values {
		for _, item := range strings.Split(value, ",") {
			if item = strings.TrimSpace(item); item != "" && item != model.CategoryKeyAll {
				result = append(result, item)
			}
		}
	}
	return result
}

func positiveInt(value string, fallback int) int {
	n, err := strconv.Atoi(value)
	if err != nil || n < 1 {
		return fallback
	}
	return n
}

func decodeJSON(c *gin.Context, dst any) *apierr.Error {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBodyBytes)
	dec := json.NewDecoder(c.Request.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return apierr.InvalidRequest("request body too large")
		}
		if errors.Is(err, io.EOF) {
			return apierr.InvalidRequest("request body is empty")
		}
		return apierr.InvalidRequest("request body is not valid JSON")
	}
	return nil
}

func writeError(c *gin.Context, e *apierr.Error) {
	if e.Status >= http.StatusInternalServerError {
		log.Printf("[handler] %s", e.Error())
	}
	details := map[string]any{}
	if len(e.Details) == 1 {
		details["field"] = e.Details[0].Field
		details["reason"] = e.Details[0].Reason
	} else if len(e.Details) > 1 {
		details["violations"] = e.Details
	}
	apiresponse.Fail(c, e.Status, e.Code, e.Message, details, e.Hint)
}
