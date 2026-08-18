// Package expert exposes the Expert Marketplace HTTP surface (docs/api/expert-v1.md
// §4): the experts (专家) and squads (专家团) CRUD families plus GET /expert_tags.
// It resolves identity from the token, decodes strict JSON bodies, calls the
// expert service, and maps service sentinels onto the standard OCTO error
// envelope.
package expert

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/api/errcode"
	apiresponse "github.com/Mininglamp-OSS/octo-marketplace/internal/api/response"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/fleet"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/middleware"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	expertrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/expert"
	expertsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/expert"
	"github.com/gin-gonic/gin"
)

const maxBodyBytes = 8 << 20

// Response type aliases so the swag annotations reference concrete generated
// schemas.
type (
	ExpertDetailResp = model.ExpertAgentDetail
	SquadDetailResp  = model.ExpertSquadDetail
	TagListResp      = []model.TagFilter
	CategoryListResp = []model.ExpertCategoryItem
)

// SkillContentResp is the body returned by the skill_md endpoints (doc §3.1):
// the stored SKILL.md text for one skill.
type SkillContentResp struct {
	Content string `json:"content"`
}

// SkillUploadInitReq is the body for POST /expert_skill_uploads: the package
// file name + size to presign an upload for.
type SkillUploadInitReq struct {
	FileName string `json:"file_name"`
	FileSize int64  `json:"file_size"`
}

// SkillUploadInitResp is the presigned-upload handshake (doc §3.1): PUT the raw
// .zip/.skill to presigned_url with the given headers, then echo
// upload_object_key back in the skill on create/update.
type SkillUploadInitResp struct {
	UploadObjectKey string            `json:"upload_object_key"`
	PresignedURL    string            `json:"presigned_url"`
	Method          string            `json:"method"`
	Headers         map[string]string `json:"headers,omitempty"`
	ExpiresIn       int               `json:"expires_in"`
}

// SkillDownloadResp is the body returned by the skill_download endpoints: a
// short-lived presigned GET URL for the skill's stored package.
type SkillDownloadResp struct {
	DownloadURL string `json:"download_url"`
}

// InstallExpertReq is the body for POST /experts/{id}/install: the Loop
// workspace + runtime (from the fleet pickers) to provision the agent in.
type InstallExpertReq struct {
	WorkspaceID string `json:"workspace_id"`
	RuntimeID   string `json:"runtime_id"`
}

// InstallExpertResp is the created Loop agent's id.
type InstallExpertResp struct {
	AgentID string `json:"agent_id"`
}

// InstallSquadReq is the body for POST /squads/{id}/install: the Loop workspace
// + runtime (from the fleet pickers) to provision the squad's member agents in.
type InstallSquadReq struct {
	WorkspaceID string `json:"workspace_id"`
	RuntimeID   string `json:"runtime_id"`
}

// InstallSquadResp is the created Loop squad's id and its leader agent's id.
type InstallSquadResp struct {
	SquadID       string `json:"squad_id"`
	LeaderAgentID string `json:"leader_agent_id"`
}

// Handler serves the expert + squad HTTP surface.
type Handler struct {
	svc *expertsvc.Service
}

// New creates a new expert handler.
func New(svc *expertsvc.Service) *Handler {
	return &Handler{svc: svc}
}

// Register mounts the expert, squad, and tag routes on the given group.
func (h *Handler) Register(rg *gin.RouterGroup) {
	rg.POST("/experts", h.CreateExpert)
	rg.GET("/experts", h.ListExperts)
	rg.GET("/experts/mine", h.ListExpertsMine)
	rg.GET("/experts/:expert_id", h.GetExpert)
	rg.PATCH("/experts/:expert_id", h.PatchExpert)
	rg.DELETE("/experts/:expert_id", h.DeleteExpert)
	rg.POST("/experts/:expert_id/install", h.InstallExpert)

	rg.POST("/squads", h.CreateSquad)
	rg.GET("/squads", h.ListSquads)
	rg.GET("/squads/mine", h.ListSquadsMine)
	rg.GET("/squads/:squad_id", h.GetSquad)
	rg.PATCH("/squads/:squad_id", h.PatchSquad)
	rg.DELETE("/squads/:squad_id", h.DeleteSquad)
	rg.POST("/squads/:squad_id/install", h.InstallSquad)

	rg.GET("/expert_tags", h.ListTags)
	rg.GET("/expert_categories", h.ListCategories)

	// Viewable skill content (doc §3.1): fetch the stored SKILL.md text for a
	// given skill index on an expert, or a squad member's skill.
	rg.GET("/experts/:expert_id/skill_md", h.GetExpertSkillMD)
	rg.GET("/squads/:squad_id/skill_md", h.GetSquadSkillMD)

	// Whole-package skills (doc §3.1): presign an upload for a .zip/.skill
	// package, and presign a download of a stored skill package.
	rg.POST("/expert_skill_uploads", h.InitSkillUpload)
	rg.GET("/experts/:expert_id/skill_download", h.GetExpertSkillDownload)
	rg.GET("/squads/:squad_id/skill_download", h.GetSquadSkillDownload)
}

// ─── Experts ─────────────────────────────────────────────────────────────────

// CreateExpert godoc
// @Summary Create expert
// @Description Publish a new standalone expert owned by the authenticated caller in the current Space.
// @Tags expert
// @ID expert.create
// @Accept json
// @Produce json
// @Security Bearer
// @Param body body model.ExpertCreateRequest true "Expert"
// @Success 201 {object} apiresponse.Data[ExpertDetailResp]
// @Failure 400 {object} apiresponse.Error "VALIDATION_ERROR"
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 409 {object} apiresponse.Error "DUPLICATE"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /experts [post]
func (h *Handler) CreateExpert(c *gin.Context) {
	caller, ok := callerFromContext(c)
	if !ok {
		unauthorized(c)
		return
	}
	var req model.ExpertCreateRequest
	if !decodeJSON(c, &req) {
		return
	}
	detail, err := h.svc.CreateExpert(c.Request.Context(), caller, req)
	if err != nil {
		writeServiceError(c, err, "expert.create")
		return
	}
	apiresponse.Created(c, detail)
}

// ListExperts godoc
// @Summary List experts
// @Description List experts visible to the caller in the current Space using offset pagination.
// @Tags expert
// @ID expert.list
// @Accept json
// @Produce json
// @Security Bearer
// @Param keyword query string false "Case-insensitive substring over name/summary/category/creator"
// @Param category query []string false "Category id; repeatable or comma-separated; 'all' disables"
// @Param tag query []string false "Tag name filters; AND-combined"
// @Param visibility query []string false "Visibility filters: system/public/private"
// @Param created_by_type query []string false "Provenance filter: human/bot/import"
// @Param sort query string false "Sort: comprehensive, latest, installs, views, updated (else creation-time DESC)"
// @Param page query int false "Page number, default 1"
// @Param page_size query int false "Page size, default 20, max 100"
// @Success 200 {object} apiresponse.OffsetList[model.ExpertAgentListItem]
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /experts [get]
func (h *Handler) ListExperts(c *gin.Context) { h.listExperts(c, false) }

// ListExpertsMine godoc
// @Summary List owned experts
// @Description List experts owned by the authenticated caller in the current Space, regardless of visibility.
// @Tags expert
// @ID expert.mine.list
// @Accept json
// @Produce json
// @Security Bearer
// @Param keyword query string false "Case-insensitive substring over name/summary/category/creator"
// @Param category query []string false "Category id; repeatable or comma-separated; 'all' disables"
// @Param tag query []string false "Tag name filters; AND-combined"
// @Param visibility query []string false "Visibility filters: system/public/private"
// @Param created_by_type query []string false "Provenance filter: human/bot/import"
// @Param sort query string false "Sort: comprehensive, latest, installs, views, updated (else creation-time DESC)"
// @Param page query int false "Page number, default 1"
// @Param page_size query int false "Page size, default 20, max 100"
// @Success 200 {object} apiresponse.OffsetList[model.ExpertAgentListItem]
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /experts/mine [get]
func (h *Handler) ListExpertsMine(c *gin.Context) { h.listExperts(c, true) }

func (h *Handler) listExperts(c *gin.Context, mine bool) {
	caller, ok := callerFromContext(c)
	if !ok {
		unauthorized(c)
		return
	}
	p, page, pageSize := listParams(c)
	var result *expertsvc.ExpertListResult
	var err error
	if mine {
		result, err = h.svc.ListExpertsMine(c.Request.Context(), caller, p)
	} else {
		result, err = h.svc.ListExperts(c.Request.Context(), caller, p)
	}
	if err != nil {
		writeServiceError(c, err, "expert.list")
		return
	}
	apiresponse.Offset(c, result.Items, result.Total, page, pageSize)
}

// GetExpert godoc
// @Summary Get expert
// @Description Return one expert visible to the authenticated caller.
// @Tags expert
// @ID expert.get
// @Accept json
// @Produce json
// @Security Bearer
// @Param expert_id path string true "Expert ID"
// @Success 200 {object} apiresponse.Data[ExpertDetailResp]
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /experts/{expert_id} [get]
func (h *Handler) GetExpert(c *gin.Context) {
	caller, ok := callerFromContext(c)
	if !ok {
		unauthorized(c)
		return
	}
	detail, err := h.svc.GetExpert(c.Request.Context(), caller, c.Param("expert_id"))
	if err != nil {
		writeServiceError(c, err, "expert.get")
		return
	}
	apiresponse.OK(c, detail)
}

// InstallExpert godoc
// @Summary Install expert to a Loop workspace/runtime
// @Description Provision the expert as a Loop agent (with its skills) in the chosen workspace/runtime. Aggregates octo-fleet calls on behalf of the caller and rolls back on partial failure.
// @Tags expert
// @ID expert.install
// @Accept json
// @Produce json
// @Security Bearer
// @Param expert_id path string true "Expert ID"
// @Param body body InstallExpertReq true "Target workspace + runtime"
// @Success 200 {object} apiresponse.Data[InstallExpertResp]
// @Failure 400 {object} apiresponse.Error "VALIDATION_ERROR"
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 409 {object} apiresponse.Error "CONFLICT"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Failure 503 {object} apiresponse.Error "UPSTREAM_UNAVAILABLE"
// @Router /experts/{expert_id}/install [post]
func (h *Handler) InstallExpert(c *gin.Context) {
	caller, ok := callerFromContext(c)
	if !ok {
		unauthorized(c)
		return
	}
	var req InstallExpertReq
	if !decodeJSON(c, &req) {
		return
	}
	result, err := h.svc.InstallExpert(c.Request.Context(), caller, c.Param("expert_id"), expertsvc.InstallInput{
		WorkspaceID: strings.TrimSpace(req.WorkspaceID),
		RuntimeID:   strings.TrimSpace(req.RuntimeID),
		SpaceID:     caller.SpaceID,
		// The token is forwarded to fleet; middleware discarded it, so re-read it.
		Token: middleware.Token(c),
	})
	if err != nil {
		writeInstallError(c, err)
		return
	}
	apiresponse.OK(c, InstallExpertResp{AgentID: result.AgentID})
}

// InstallSquad godoc
// @Summary Install squad to a Loop workspace/runtime
// @Description Provision a squad into the chosen workspace/runtime: install each member as a Loop agent (exact duplicate Skill names are first-wins across members), create the squad led by the leader member, write its strategies as instructions with bounded retries, and attach the rest. Aggregates octo-fleet calls on behalf of the caller and rolls back on persistent partial failure.
// @Tags expert
// @ID squad.install
// @Accept json
// @Produce json
// @Security Bearer
// @Param squad_id path string true "Squad ID"
// @Param body body InstallSquadReq true "Target workspace + runtime"
// @Success 200 {object} apiresponse.Data[InstallSquadResp]
// @Failure 400 {object} apiresponse.Error "VALIDATION_ERROR"
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 409 {object} apiresponse.Error "CONFLICT"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Failure 503 {object} apiresponse.Error "UPSTREAM_UNAVAILABLE"
// @Router /squads/{squad_id}/install [post]
func (h *Handler) InstallSquad(c *gin.Context) {
	caller, ok := callerFromContext(c)
	if !ok {
		unauthorized(c)
		return
	}
	var req InstallSquadReq
	if !decodeJSON(c, &req) {
		return
	}
	result, err := h.svc.InstallSquad(c.Request.Context(), caller, c.Param("squad_id"), expertsvc.InstallInput{
		WorkspaceID: strings.TrimSpace(req.WorkspaceID),
		RuntimeID:   strings.TrimSpace(req.RuntimeID),
		SpaceID:     caller.SpaceID,
		// The token is forwarded to fleet; middleware discarded it, so re-read it.
		Token: middleware.Token(c),
	})
	if err != nil {
		writeInstallError(c, err)
		return
	}
	apiresponse.OK(c, InstallSquadResp{SquadID: result.SquadID, LeaderAgentID: result.LeaderAgentID})
}

// GetExpertSkillMD godoc
// @Summary Get expert skill content
// @Description Return the stored SKILL.md text for the expert's skill at index i.
// @Tags expert
// @ID expert.skill_md
// @Produce json
// @Security Bearer
// @Param expert_id path string true "Expert ID"
// @Param i query int false "Skill index (default 0)"
// @Success 200 {object} apiresponse.Data[SkillContentResp]
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /experts/{expert_id}/skill_md [get]
func (h *Handler) GetExpertSkillMD(c *gin.Context) {
	caller, ok := callerFromContext(c)
	if !ok {
		unauthorized(c)
		return
	}
	index, _ := strconv.Atoi(c.Query("i"))
	content, err := h.svc.GetExpertSkillMD(c.Request.Context(), caller, c.Param("expert_id"), index)
	if err != nil {
		writeServiceError(c, err, "expert.skill_md")
		return
	}
	apiresponse.OK(c, SkillContentResp{Content: content})
}

// GetSquadSkillMD godoc
// @Summary Get squad member skill content
// @Description Return the stored SKILL.md text for a squad member's skill at index i.
// @Tags expert
// @ID squad.skill_md
// @Produce json
// @Security Bearer
// @Param squad_id path string true "Squad ID"
// @Param member query string true "Member key"
// @Param i query int false "Skill index (default 0)"
// @Success 200 {object} apiresponse.Data[SkillContentResp]
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /squads/{squad_id}/skill_md [get]
func (h *Handler) GetSquadSkillMD(c *gin.Context) {
	caller, ok := callerFromContext(c)
	if !ok {
		unauthorized(c)
		return
	}
	index, _ := strconv.Atoi(c.Query("i"))
	content, err := h.svc.GetSquadSkillMD(c.Request.Context(), caller, c.Param("squad_id"), c.Query("member"), index)
	if err != nil {
		writeServiceError(c, err, "squad.skill_md")
		return
	}
	apiresponse.OK(c, SkillContentResp{Content: content})
}

// InitSkillUpload godoc
// @Summary Init skill package upload
// @Description Presign a PUT URL for uploading a .zip/.skill package. PUT the raw file to presigned_url, then send upload_object_key in the skill on create/update.
// @Tags expert
// @ID expert.skill_upload.init
// @Accept json
// @Produce json
// @Security Bearer
// @Param body body SkillUploadInitReq true "Package file name + size"
// @Success 200 {object} apiresponse.Data[SkillUploadInitResp]
// @Failure 400 {object} apiresponse.Error "VALIDATION_ERROR"
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /expert_skill_uploads [post]
func (h *Handler) InitSkillUpload(c *gin.Context) {
	if _, ok := callerFromContext(c); !ok {
		unauthorized(c)
		return
	}
	var req SkillUploadInitReq
	if !decodeJSON(c, &req) {
		return
	}
	init, err := h.svc.InitSkillUpload(c.Request.Context(), req.FileName, req.FileSize)
	if err != nil {
		writeServiceError(c, err, "expert.skill_upload.init")
		return
	}
	apiresponse.OK(c, SkillUploadInitResp{
		UploadObjectKey: init.UploadObjectKey,
		PresignedURL:    init.PresignedURL,
		Method:          init.Method,
		Headers:         init.Headers,
		ExpiresIn:       init.ExpiresIn,
	})
}

// GetExpertSkillDownload godoc
// @Summary Get expert skill package download URL
// @Description Return a short-lived presigned GET URL for the expert's skill package at index i.
// @Tags expert
// @ID expert.skill_download
// @Produce json
// @Security Bearer
// @Param expert_id path string true "Expert ID"
// @Param i query int false "Skill index (default 0)"
// @Success 200 {object} apiresponse.Data[SkillDownloadResp]
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /experts/{expert_id}/skill_download [get]
func (h *Handler) GetExpertSkillDownload(c *gin.Context) {
	caller, ok := callerFromContext(c)
	if !ok {
		unauthorized(c)
		return
	}
	index, _ := strconv.Atoi(c.Query("i"))
	url, err := h.svc.GetExpertSkillDownload(c.Request.Context(), caller, c.Param("expert_id"), index)
	if err != nil {
		writeServiceError(c, err, "expert.skill_download")
		return
	}
	apiresponse.OK(c, SkillDownloadResp{DownloadURL: url})
}

// GetSquadSkillDownload godoc
// @Summary Get squad member skill package download URL
// @Description Return a short-lived presigned GET URL for a squad member's skill package at index i.
// @Tags expert
// @ID squad.skill_download
// @Produce json
// @Security Bearer
// @Param squad_id path string true "Squad ID"
// @Param member query string true "Member key"
// @Param i query int false "Skill index (default 0)"
// @Success 200 {object} apiresponse.Data[SkillDownloadResp]
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /squads/{squad_id}/skill_download [get]
func (h *Handler) GetSquadSkillDownload(c *gin.Context) {
	caller, ok := callerFromContext(c)
	if !ok {
		unauthorized(c)
		return
	}
	index, _ := strconv.Atoi(c.Query("i"))
	url, err := h.svc.GetSquadSkillDownload(c.Request.Context(), caller, c.Param("squad_id"), c.Query("member"), index)
	if err != nil {
		writeServiceError(c, err, "squad.skill_download")
		return
	}
	apiresponse.OK(c, SkillDownloadResp{DownloadURL: url})
}

// @Summary Update expert
// @Description Partially update an expert owned by the authenticated caller.
// @Tags expert
// @ID expert.update
// @Accept json
// @Produce json
// @Security Bearer
// @Param expert_id path string true "Expert ID"
// @Param body body model.ExpertPatchRequest true "Expert changes"
// @Success 200 {object} apiresponse.Data[ExpertDetailResp]
// @Failure 400 {object} apiresponse.Error "VALIDATION_ERROR"
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 409 {object} apiresponse.Error "DUPLICATE"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /experts/{expert_id} [patch]
func (h *Handler) PatchExpert(c *gin.Context) {
	caller, ok := callerFromContext(c)
	if !ok {
		unauthorized(c)
		return
	}
	var req model.ExpertPatchRequest
	if !decodeJSON(c, &req) {
		return
	}
	detail, err := h.svc.PatchExpert(c.Request.Context(), caller, c.Param("expert_id"), req)
	if err != nil {
		writeServiceError(c, err, "expert.update")
		return
	}
	apiresponse.OK(c, detail)
}

// DeleteExpert godoc
// @Summary Delete expert
// @Description Soft-delete an expert owned by the authenticated caller.
// @Tags expert
// @ID expert.delete
// @Accept json
// @Produce json
// @Security Bearer
// @Param expert_id path string true "Expert ID"
// @Success 200 {object} apiresponse.Data[apiresponse.EmptyResp]
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /experts/{expert_id} [delete]
func (h *Handler) DeleteExpert(c *gin.Context) {
	caller, ok := callerFromContext(c)
	if !ok {
		unauthorized(c)
		return
	}
	if err := h.svc.DeleteExpert(c.Request.Context(), caller, c.Param("expert_id")); err != nil {
		writeServiceError(c, err, "expert.delete")
		return
	}
	apiresponse.Empty(c)
}

// ─── Squads ──────────────────────────────────────────────────────────────────

// CreateSquad godoc
// @Summary Create squad
// @Description Publish a new expert squad owned by the authenticated caller in the current Space.
// @Tags expert_squad
// @ID squad.create
// @Accept json
// @Produce json
// @Security Bearer
// @Param body body model.SquadCreateRequest true "Squad"
// @Success 201 {object} apiresponse.Data[SquadDetailResp]
// @Failure 400 {object} apiresponse.Error "VALIDATION_ERROR"
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 409 {object} apiresponse.Error "DUPLICATE"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /squads [post]
func (h *Handler) CreateSquad(c *gin.Context) {
	caller, ok := callerFromContext(c)
	if !ok {
		unauthorized(c)
		return
	}
	var req model.SquadCreateRequest
	if !decodeJSON(c, &req) {
		return
	}
	detail, err := h.svc.CreateSquad(c.Request.Context(), caller, req)
	if err != nil {
		writeServiceError(c, err, "squad.create")
		return
	}
	apiresponse.Created(c, detail)
}

// ListSquads godoc
// @Summary List squads
// @Description List squads visible to the caller in the current Space using offset pagination.
// @Tags expert_squad
// @ID squad.list
// @Accept json
// @Produce json
// @Security Bearer
// @Param keyword query string false "Case-insensitive substring over name/summary/category/creator"
// @Param category query []string false "Category id; repeatable or comma-separated; 'all' disables"
// @Param tag query []string false "Tag name filters; AND-combined"
// @Param visibility query []string false "Visibility filters: system/public/private"
// @Param created_by_type query []string false "Provenance filter: human/bot/import"
// @Param sort query string false "Sort: comprehensive, latest, installs, views, updated (else creation-time DESC)"
// @Param page query int false "Page number, default 1"
// @Param page_size query int false "Page size, default 20, max 100"
// @Success 200 {object} apiresponse.OffsetList[model.ExpertSquadListItem]
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /squads [get]
func (h *Handler) ListSquads(c *gin.Context) { h.listSquads(c, false) }

// ListSquadsMine godoc
// @Summary List owned squads
// @Description List squads owned by the authenticated caller in the current Space, regardless of visibility.
// @Tags expert_squad
// @ID squad.mine.list
// @Accept json
// @Produce json
// @Security Bearer
// @Param keyword query string false "Case-insensitive substring over name/summary/category/creator"
// @Param category query []string false "Category id; repeatable or comma-separated; 'all' disables"
// @Param tag query []string false "Tag name filters; AND-combined"
// @Param visibility query []string false "Visibility filters: system/public/private"
// @Param created_by_type query []string false "Provenance filter: human/bot/import"
// @Param sort query string false "Sort: comprehensive, latest, installs, views, updated (else creation-time DESC)"
// @Param page query int false "Page number, default 1"
// @Param page_size query int false "Page size, default 20, max 100"
// @Success 200 {object} apiresponse.OffsetList[model.ExpertSquadListItem]
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /squads/mine [get]
func (h *Handler) ListSquadsMine(c *gin.Context) { h.listSquads(c, true) }

func (h *Handler) listSquads(c *gin.Context, mine bool) {
	caller, ok := callerFromContext(c)
	if !ok {
		unauthorized(c)
		return
	}
	p, page, pageSize := listParams(c)
	var result *expertsvc.SquadListResult
	var err error
	if mine {
		result, err = h.svc.ListSquadsMine(c.Request.Context(), caller, p)
	} else {
		result, err = h.svc.ListSquads(c.Request.Context(), caller, p)
	}
	if err != nil {
		writeServiceError(c, err, "squad.list")
		return
	}
	apiresponse.Offset(c, result.Items, result.Total, page, pageSize)
}

// GetSquad godoc
// @Summary Get squad
// @Description Return one squad visible to the authenticated caller.
// @Tags expert_squad
// @ID squad.get
// @Accept json
// @Produce json
// @Security Bearer
// @Param squad_id path string true "Squad ID"
// @Success 200 {object} apiresponse.Data[SquadDetailResp]
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /squads/{squad_id} [get]
func (h *Handler) GetSquad(c *gin.Context) {
	caller, ok := callerFromContext(c)
	if !ok {
		unauthorized(c)
		return
	}
	detail, err := h.svc.GetSquad(c.Request.Context(), caller, c.Param("squad_id"))
	if err != nil {
		writeServiceError(c, err, "squad.get")
		return
	}
	apiresponse.OK(c, detail)
}

// PatchSquad godoc
// @Summary Update squad
// @Description Partially update a squad owned by the authenticated caller. Sending members replaces the whole array.
// @Tags expert_squad
// @ID squad.update
// @Accept json
// @Produce json
// @Security Bearer
// @Param squad_id path string true "Squad ID"
// @Param body body model.SquadPatchRequest true "Squad changes"
// @Success 200 {object} apiresponse.Data[SquadDetailResp]
// @Failure 400 {object} apiresponse.Error "VALIDATION_ERROR"
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 409 {object} apiresponse.Error "DUPLICATE"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /squads/{squad_id} [patch]
func (h *Handler) PatchSquad(c *gin.Context) {
	caller, ok := callerFromContext(c)
	if !ok {
		unauthorized(c)
		return
	}
	var req model.SquadPatchRequest
	if !decodeJSON(c, &req) {
		return
	}
	detail, err := h.svc.PatchSquad(c.Request.Context(), caller, c.Param("squad_id"), req)
	if err != nil {
		writeServiceError(c, err, "squad.update")
		return
	}
	apiresponse.OK(c, detail)
}

// DeleteSquad godoc
// @Summary Delete squad
// @Description Soft-delete a squad owned by the authenticated caller.
// @Tags expert_squad
// @ID squad.delete
// @Accept json
// @Produce json
// @Security Bearer
// @Param squad_id path string true "Squad ID"
// @Success 200 {object} apiresponse.Data[apiresponse.EmptyResp]
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /squads/{squad_id} [delete]
func (h *Handler) DeleteSquad(c *gin.Context) {
	caller, ok := callerFromContext(c)
	if !ok {
		unauthorized(c)
		return
	}
	if err := h.svc.DeleteSquad(c.Request.Context(), caller, c.Param("squad_id")); err != nil {
		writeServiceError(c, err, "squad.delete")
		return
	}
	apiresponse.Empty(c)
}

// ─── Tags ────────────────────────────────────────────────────────────────────

// ListTags godoc
// @Summary List expert tags
// @Description Aggregate tag suggestions from records visible to the caller in the current Space, sorted by descending row count. kind selects experts (agent) or squads (squad).
// @Tags expert
// @ID expert_tag.list
// @Accept json
// @Produce json
// @Security Bearer
// @Param kind query string false "agent (default) aggregates experts; squad aggregates squads"
// @Param q query string false "Case-insensitive substring match on tag name"
// @Param limit query int false "Max items, default 50, clamped to [1,100]"
// @Param mode query string false "Scope: mine restricts to caller-owned rows"
// @Success 200 {object} apiresponse.Data[TagListResp]
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /expert_tags [get]
func (h *Handler) ListTags(c *gin.Context) {
	caller, ok := callerFromContext(c)
	if !ok {
		unauthorized(c)
		return
	}
	entity := expertrepo.EntityExpert
	if c.Query("kind") == "squad" {
		entity = expertrepo.EntitySquad
	}
	tags, err := h.svc.ListTags(c.Request.Context(), caller, entity,
		strings.TrimSpace(c.Query("q")), parseTagLimit(c.Query("limit")), c.Query("mode") == "mine")
	if err != nil {
		writeServiceError(c, err, "expert_tag.list")
		return
	}
	apiresponse.OK(c, tags)
}

// ─── Categories ──────────────────────────────────────────────────────────────

// ListCategories godoc
// @Summary List expert categories
// @Description Return every expert category with the number of records of the requested kind visible to the caller in the current Space, ordered by sort_order. kind selects experts (agent) or squads (squad); categories with no visible records report count 0.
// @Tags expert
// @ID expert_category.list
// @Accept json
// @Produce json
// @Security Bearer
// @Param kind query string false "agent (default) counts experts; squad counts squads"
// @Success 200 {object} apiresponse.Data[CategoryListResp]
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /expert_categories [get]
func (h *Handler) ListCategories(c *gin.Context) {
	caller, ok := callerFromContext(c)
	if !ok {
		unauthorized(c)
		return
	}
	kind := expertrepo.EntityExpert
	if c.Query("kind") == "squad" {
		kind = expertrepo.EntitySquad
	}
	items, err := h.svc.ListCategories(c.Request.Context(), caller, kind)
	if err != nil {
		writeServiceError(c, err, "expert_category.list")
		return
	}
	apiresponse.OK(c, items)
}

func callerFromContext(c *gin.Context) (expertsvc.Caller, bool) {
	identity, ok := middleware.Identity(c)
	if !ok || identity.UID == "" {
		return expertsvc.Caller{}, false
	}
	caller := expertsvc.Caller{UID: identity.UID, Name: identity.Name, SpaceID: middleware.SpaceID(c)}
	if bot, hasBot := middleware.BotIdentity(c); hasBot && bot.BotUID != "" {
		caller.BotUID = bot.BotUID
		caller.BotName = bot.BotName
	}
	return caller, true
}

// listParams parses the shared list query params (doc §4.2). The category "all"
// sentinel disables the category filter and is stripped here.
func listParams(c *gin.Context) (expertsvc.ListParams, int, int) {
	page := positiveInt(c.Query("page"), 1)
	pageSize := positiveInt(c.Query("page_size"), 20)
	if pageSize > 100 {
		pageSize = 100
	}
	return expertsvc.ListParams{
		Keyword:        strings.TrimSpace(c.Query("keyword")),
		Categories:     splitCategoryQuery(c.QueryArray("category")),
		Tags:           splitQuery(c.QueryArray("tag")),
		Visibilities:   splitQuery(c.QueryArray("visibility")),
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
			if item = strings.TrimSpace(item); item != "" {
				result = append(result, item)
			}
		}
	}
	return result
}

func splitCategoryQuery(values []string) []string {
	filtered := splitQuery(values)
	kept := filtered[:0]
	for _, item := range filtered {
		if item == model.CategoryKeyAll {
			continue
		}
		kept = append(kept, item)
	}
	return kept
}

func positiveInt(value string, fallback int) int {
	n, err := strconv.Atoi(value)
	if err != nil || n < 1 {
		return fallback
	}
	return n
}

func parseTagLimit(raw string) int {
	const (
		def      = 50
		maxLimit = 100
	)
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return def
	}
	if n > maxLimit {
		return maxLimit
	}
	return n
}

// decodeJSON strictly decodes the request body (size-capped, unknown fields
// rejected). On failure it writes a 400 VALIDATION_ERROR and returns false.
func decodeJSON(c *gin.Context, dst any) bool {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBodyBytes)
	dec := json.NewDecoder(c.Request.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		var maxErr *http.MaxBytesError
		switch {
		case errors.As(err, &maxErr):
			validationError(c, "request body too large")
		case errors.Is(err, io.EOF):
			validationError(c, "request body is empty")
		default:
			validationError(c, "invalid request body")
		}
		return false
	}
	return true
}

func unauthorized(c *gin.Context) {
	apiresponse.Fail(c, http.StatusUnauthorized, errcode.Unauthorized, "unauthorized", nil, "")
}

func validationError(c *gin.Context, message string) {
	apiresponse.Fail(c, http.StatusBadRequest, errcode.BadRequest, message, nil, "")
}

// writeServiceError maps service sentinels onto wire codes (doc §2). NotFound→
// 404, Forbidden→403, NameTaken→409 DUPLICATE, the invalid* family→400, else a
// logged internal 500.
func writeServiceError(c *gin.Context, err error, operation string) {
	switch {
	case errors.Is(err, expertsvc.ErrNotFound):
		apiresponse.Fail(c, http.StatusNotFound, errcode.NotFound, "not found", nil, "")
	case errors.Is(err, expertsvc.ErrForbidden):
		apiresponse.Fail(c, http.StatusForbidden, errcode.PermissionDenied, "only the owner may modify this record", nil, "")
	case errors.Is(err, expertsvc.ErrNameTaken):
		apiresponse.Fail(c, http.StatusConflict, errcode.Duplicate, "a record with this name already exists", nil, "")
	case errors.Is(err, expertsvc.ErrCategoryNotFound):
		apiresponse.Fail(c, http.StatusBadRequest, errcode.BadRequest, "category not found", nil, "")
	case errors.Is(err, expertsvc.ErrInvalidVisibility):
		apiresponse.Fail(c, http.StatusBadRequest, errcode.BadRequest, "visibility must be public or private", nil, "")
	case errors.Is(err, expertsvc.ErrVisibilityNotAllowed):
		apiresponse.Fail(c, http.StatusBadRequest, errcode.BadRequest, "visibility is stamped by the server on this surface", nil, "Omit visibility; admin create always publishes system records.")
	case errors.Is(err, expertsvc.ErrInvalidMCPConfig):
		apiresponse.Fail(c, http.StatusBadRequest, errcode.BadRequest, "mcp_config must be valid JSON within the size limit", nil, "")
	case errors.Is(err, expertsvc.ErrInvalidMembers):
		apiresponse.Fail(c, http.StatusBadRequest, errcode.BadRequest, "members are missing or malformed", nil, "")
	case errors.Is(err, expertsvc.ErrInvalidRequest):
		apiresponse.Fail(c, http.StatusBadRequest, errcode.BadRequest, "request failed validation", nil, "")
	default:
		apiresponse.Internal(c, err, operation)
	}
}

// writeInstallError maps the extra failure modes of the install aggregation on
// top of the shared sentinels: an unconfigured fleet → 503, and a fleet
// *APIError → its own status (4xx surfaced verbatim, 5xx/transport collapsed to
// UPSTREAM_UNAVAILABLE so a fleet hiccup never masquerades as a client fault).
func writeInstallError(c *gin.Context, err error) {
	if errors.Is(err, expertsvc.ErrFleetNotConfigured) {
		apiresponse.Fail(c, http.StatusServiceUnavailable, errcode.UpstreamUnavailable, "loop service is not configured", nil, "")
		return
	}
	if errors.Is(err, expertsvc.ErrInstallTooLarge) {
		apiresponse.Fail(c, http.StatusBadRequest, errcode.BadRequest, "install exceeds resource limits", nil, "")
		return
	}
	var apiErr *fleet.APIError
	if errors.As(err, &apiErr) {
		if apiErr.Status >= 400 && apiErr.Status < 500 {
			apiresponse.Fail(c, apiErr.Status, installErrCode(apiErr.Status), fleetErrorMessage(apiErr), nil, "")
			return
		}
		apiresponse.Fail(c, http.StatusServiceUnavailable, errcode.UpstreamUnavailable, "loop service is unavailable", nil, "")
		return
	}
	// ErrNotFound / ErrInvalidRequest and the rest share the catalog mapping.
	writeServiceError(c, err, "expert.install")
}

// installErrCode maps a fleet 4xx status to the marketplace wire error code.
func installErrCode(status int) string {
	switch status {
	case http.StatusUnauthorized:
		return errcode.Unauthorized
	case http.StatusForbidden:
		return errcode.PermissionDenied
	case http.StatusNotFound:
		return errcode.NotFound
	case http.StatusConflict:
		return errcode.Conflict
	default:
		return errcode.BadRequest
	}
}

// fleetErrorMessage returns fleet's message, capped so an upstream string can't
// bloat our envelope, with a safe fallback.
func fleetErrorMessage(e *fleet.APIError) string {
	msg := strings.TrimSpace(e.Message)
	if msg == "" {
		return "loop service rejected the request"
	}
	// Cap on a rune boundary — fleet returns Chinese text, so a byte slice could
	// split a multi-byte rune and emit a U+FFFD.
	const maxBytes = 200
	if len(msg) <= maxBytes {
		return msg
	}
	end := maxBytes
	for end > 0 && !utf8.RuneStart(msg[end]) {
		end--
	}
	return msg[:end]
}
