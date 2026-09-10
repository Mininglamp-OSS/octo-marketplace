// Package plugin exposes the unified Plugin HTTP API.
package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/api/errcode"
	apiresponse "github.com/Mininglamp-OSS/octo-marketplace/internal/api/response"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/logging"
	marketmiddleware "github.com/Mininglamp-OSS/octo-marketplace/internal/middleware"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	pluginsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/plugin"
	"github.com/gin-gonic/gin"
)

// maxBodyBytes caps a plugin write body. `maxReviewBodyBytes` (review.go) is
// derived from it: a review submit carries the same declared content, so the two
// paths must accept the same size.
const maxBodyBytes = 3 << 20

// Service is the handler-facing boundary over the unified Plugin service.
type Service interface {
	List(context.Context, pluginsvc.Caller, pluginsvc.ListParams) ([]model.Plugin, int64, error)
	Detail(context.Context, pluginsvc.Caller, string, bool) (*pluginsvc.Detail, error)
	Create(context.Context, pluginsvc.Caller, pluginsvc.WriteRequest) (*pluginsvc.Detail, error)
	Update(context.Context, pluginsvc.Caller, string, pluginsvc.WriteRequest) (*pluginsvc.Detail, error)
	Delete(context.Context, pluginsvc.Caller, string) error
	ListVersions(context.Context, pluginsvc.Caller, string, int, int) ([]model.PluginVersion, int64, error)
	Install(context.Context, pluginsvc.Caller, string, pluginsvc.InstallParams) (*pluginsvc.InstallOutcome, error)
	Import(context.Context, pluginsvc.Caller, pluginsvc.ImportParams) (*pluginsvc.Detail, error)
	SkillMarkdown(context.Context, pluginsvc.Caller, string) (string, error)
	OpenSkillPackage(context.Context, pluginsvc.Caller, string) (*pluginsvc.SkillPackageStream, error)
	ListTags(context.Context, pluginsvc.Caller, pluginsvc.TagListParams) ([]model.TagFilter, error)

	// Space review workflow (see review.go).
	SubmitReview(context.Context, pluginsvc.Caller, pluginsvc.ReviewSubmitParams) (*model.PluginReviewRequest, error)
	ListReviews(context.Context, pluginsvc.Caller, string, model.ReviewStatus, int, int) ([]*model.PluginReviewRequest, int64, error)
	GetReview(context.Context, pluginsvc.Caller, string) (*model.PluginReviewRequest, error)
	ApproveReview(context.Context, pluginsvc.Caller, string) (*model.Plugin, error)
	RejectReview(context.Context, pluginsvc.Caller, string, string) error
	CancelReview(context.Context, pluginsvc.Caller, string) error
	GetReviewPolicy(context.Context, pluginsvc.Caller) (model.PluginReviewPolicy, error)
	UpdateReviewPolicy(context.Context, pluginsvc.Caller, bool) (model.PluginReviewPolicy, error)

	// Listing lifecycle (see listing.go). Publish is the single 发布 door: it
	// routes to an immediate listing or a review request based on the Plugin's
	// declared visibility, so no client has to know the rule.
	Publish(context.Context, pluginsvc.Caller, pluginsvc.PublishParams) (*pluginsvc.PublishResult, error)
	Delist(context.Context, pluginsvc.Caller, pluginsvc.DelistParams) (*pluginsvc.Detail, error)
}

// CategoryService is separate because category listing is a read-only operation
// served by the read-side category service rather than the full Plugin service.
type CategoryService interface {
	ListCategories(context.Context, pluginsvc.Caller, string, model.PluginType) ([]model.PluginCategory, error)
}

type Handler struct {
	svc        Service
	categories CategoryService
}

func New(svc Service, categories ...CategoryService) *Handler {
	h := &Handler{svc: svc}
	if len(categories) > 0 {
		h.categories = categories[0]
	}
	return h
}

func (h *Handler) Register(rg *gin.RouterGroup) {
	rg.GET("/plugins", h.List)
	rg.GET("/plugin_categories", h.ListCategories)
	rg.GET("/plugin_tags", h.ListTags)
	plugins := rg.Group("/plugins")
	plugins.GET("/detail", h.Get)
	plugins.POST("/upsert", h.Upsert)
	plugins.POST("/delete", h.Delete)
	plugins.POST("/publish", h.Publish)
	plugins.POST("/delist", h.Delist)
	plugins.GET("/versions", h.ListVersions)
	plugins.POST("/install", h.Install)
	plugins.POST("/import", h.Import)
	plugins.GET("/skill_md", h.SkillMarkdown)
	plugins.GET("/download", h.DownloadSkillPackage)
	// Space review queue. Registered on the same authenticated group; the routes
	// are resourceful (unlike the verb-style legacy plugin routes above) because
	// the octo-web client is already built against these paths.
	reviews := plugins.Group("/review_requests")
	reviews.POST("", h.SubmitReview)
	reviews.GET("", h.ListReviews)
	reviews.GET("/:review_id", h.GetReview)
	reviews.POST("/:review_id/approve", h.ApproveReview)
	reviews.POST("/:review_id/reject", h.RejectReview)
	reviews.POST("/:review_id/cancel", h.CancelReview)
	rg.GET("/plugin_review_policies", h.GetReviewPolicy)
	rg.PATCH("/plugin_review_policies", h.UpdateReviewPolicy)
}

type relationRequest struct {
	RelationID     string          `json:"relation_id,omitempty"`
	SourcePluginID string          `json:"source_plugin_id,omitempty"`
	TargetPluginID string          `json:"target_plugin_id"`
	RelationType   string          `json:"relation_type"`
	SortOrder      int             `json:"sort_order"`
	Data           json.RawMessage `json:"data,omitempty" swaggertype:"object"`
}

type pluginWriteRequest struct {
	PluginID   string                 `json:"plugin_id,omitempty"`
	PluginName string                 `json:"plugin_name"`
	PluginType model.PluginType       `json:"plugin_type"`
	CategoryID *string                `json:"category_id,omitempty"`
	Tags       []string               `json:"tags"`
	Publisher  string                 `json:"publisher,omitempty"`
	Icon       string                 `json:"icon,omitempty"`
	Visibility model.PluginVisibility `json:"visibility"`
	// Version label as MAJOR.MINOR.PATCH, each part 1-9 digits. Omit to keep the
	// stored label. A plugin whose stored label predates this format stays editable by
	// re-sending that exact label.
	Version      string          `json:"version,omitempty" pattern:"^\\d{1,9}\\.\\d{1,9}\\.\\d{1,9}$"`
	ManifestJSON json.RawMessage `json:"manifest_json" swaggertype:"object"`
	PluginJSON   json.RawMessage `json:"plugin_json" swaggertype:"object"`
}

type upsertRequest struct {
	Plugin    pluginWriteRequest `json:"plugin"`
	Relations []relationRequest  `json:"relations"`
}

func (r upsertRequest) serviceRequest() pluginsvc.WriteRequest {
	relations := make([]pluginsvc.RelationRequest, len(r.Relations))
	for i, x := range r.Relations {
		relations[i] = pluginsvc.RelationRequest{ID: x.RelationID, SourcePluginID: x.SourcePluginID, TargetPluginID: x.TargetPluginID, Type: x.RelationType, SortOrder: x.SortOrder, Data: x.Data}
	}
	return pluginsvc.WriteRequest{Name: r.Plugin.PluginName, Type: r.Plugin.PluginType, CategoryID: r.Plugin.CategoryID, Tags: rawJSON(r.Plugin.Tags), Publisher: r.Plugin.Publisher, Icon: r.Plugin.Icon, Visibility: r.Plugin.Visibility, Version: r.Plugin.Version, Manifest: r.Plugin.ManifestJSON, Package: r.Plugin.PluginJSON, Relations: relations}
}

type deleteRequest struct {
	PluginID string `json:"plugin_id"`
}

type deleteResponse struct {
	PluginID string `json:"plugin_id"`
	Deleted  bool   `json:"deleted"`
}

type pluginResponse struct {
	PluginID     string                   `json:"plugin_id"`
	PluginName   string                   `json:"plugin_name"`
	PluginType   model.PluginType         `json:"plugin_type"`
	IsEmbedded   bool                     `json:"is_embedded"`
	CategoryID   *string                  `json:"category_id,omitempty"`
	Tags         []string                 `json:"tags"`
	Publisher    string                   `json:"publisher,omitempty"`
	OwnerID      string                   `json:"owner_id"`
	SpaceID      *string                  `json:"space_id,omitempty"`
	Visibility   model.PluginVisibility   `json:"visibility"`
	ListingState model.PluginListingState `json:"listing_state"`
	// DisplayStatus is the single DERIVED status a client shows: draft /
	// pending_review / published / rejected / delisted. It folds the listing axis
	// together with the review axis so clients do not have to fetch the review list
	// and join. Distinct from `status`, which is the row's soft-active flag.
	DisplayStatus    model.PluginDisplayStatus `json:"display_status"`
	ReviewID         *string                   `json:"review_id,omitempty"`
	CreatorName      string                    `json:"creator_name"`
	CreatedByType    string                    `json:"created_by_type"`
	CreatedByBotID   *string                   `json:"created_by_bot_id,omitempty"`
	CreatedByBotName *string                   `json:"created_by_bot_name,omitempty"`
	// Icon is the stored write-canonical value clients must echo on updates;
	// IconURL is the resolved display URL (presigned when Icon is an object key).
	Icon             string          `json:"icon,omitempty"`
	IconURL          string          `json:"icon_url,omitempty"`
	ToolCount        *int            `json:"tool_count,omitempty"`
	ViewCount        int             `json:"view_count"`
	InstallCount     int             `json:"install_count"`
	DownloadCount    int             `json:"download_count"`
	ManifestJSON     json.RawMessage `json:"manifest_json" swaggertype:"object"`
	PluginJSON       json.RawMessage `json:"plugin_json" swaggertype:"object"`
	ManifestHash     string          `json:"manifest_hash"`
	PluginHash       string          `json:"plugin_hash"`
	CurrentVersionID *string         `json:"current_version_id,omitempty"`
	CurrentVersion   *string         `json:"current_version,omitempty"`
	Status           int             `json:"status"`
	CreatedAt        time.Time       `json:"created_at" swaggertype:"string,date-time"`
	UpdatedAt        time.Time       `json:"updated_at" swaggertype:"string,date-time"`
}

// listItemResponse is the list-page projection: it carries the manifest for
// display but never the full plugin_json package.
type listItemResponse struct {
	PluginID   string           `json:"plugin_id"`
	PluginName string           `json:"plugin_name"`
	PluginType model.PluginType `json:"plugin_type"`
	IsEmbedded bool             `json:"is_embedded"`
	CategoryID *string          `json:"category_id,omitempty"`
	Tags       []string         `json:"tags"`
	Publisher  string           `json:"publisher,omitempty"`
	// ListingState / Status / ReviewID are populated for mode=mine only. On the
	// marketplace grid every row is published by construction, so the status would
	// be a constant and the extra per-row lookups buy nothing.
	ListingState     model.PluginListingState  `json:"listing_state"`
	DisplayStatus    model.PluginDisplayStatus `json:"display_status"`
	ReviewID         *string                   `json:"review_id,omitempty"`
	OwnerID          string                    `json:"owner_id"`
	SpaceID          *string                   `json:"space_id,omitempty"`
	Visibility       model.PluginVisibility    `json:"visibility"`
	CreatorName      string                    `json:"creator_name"`
	CreatedByType    string                    `json:"created_by_type"`
	CreatedByBotID   *string                   `json:"created_by_bot_id,omitempty"`
	CreatedByBotName *string                   `json:"created_by_bot_name,omitempty"`
	Icon             string                    `json:"icon,omitempty"`
	IconURL          string                    `json:"icon_url,omitempty"`
	ToolCount        *int                      `json:"tool_count,omitempty"`
	MemberCount      *int                      `json:"member_count,omitempty"`
	ViewCount        int                       `json:"view_count"`
	InstallCount     int                       `json:"install_count"`
	DownloadCount    int                       `json:"download_count"`
	ManifestJSON     json.RawMessage           `json:"manifest_json" swaggertype:"object"`
	ManifestHash     string                    `json:"manifest_hash"`
	PluginHash       string                    `json:"plugin_hash"`
	CurrentVersionID *string                   `json:"current_version_id,omitempty"`
	CurrentVersion   *string                   `json:"current_version,omitempty"`
	Status           int                       `json:"status"`
	CreatedAt        time.Time                 `json:"created_at" swaggertype:"string,date-time"`
	UpdatedAt        time.Time                 `json:"updated_at" swaggertype:"string,date-time"`
}

type relationResponse struct {
	RelationID     string          `json:"relation_id"`
	SourcePluginID string          `json:"source_plugin_id"`
	TargetPluginID string          `json:"target_plugin_id"`
	RelationType   string          `json:"relation_type"`
	SortOrder      int             `json:"sort_order"`
	Data           json.RawMessage `json:"data,omitempty" swaggertype:"object"`
}

type detailResponse struct {
	Plugin    pluginResponse     `json:"plugin"`
	Relations []relationResponse `json:"relations"`
	// RelationResult reports upsert relation target-state synchronization:
	// empty relation_id created, known relation_id with changes updated, live
	// relations omitted from the submission deleted. Absent on reads.
	RelationResult *relationResultResponse `json:"relation_result,omitempty"`
}

type relationResultResponse struct {
	Created []string `json:"created"`
	Updated []string `json:"updated"`
	Deleted []string `json:"deleted"`
}

// versionResponse is the metadata-only projection of one history row. It
// deliberately omits the full manifest/package bytes: the versions LIST would
// otherwise expose every intermediate/rolled-back save's complete content of a
// visible (incl. system) plugin. Callers get the label, hashes, changelog and
// timestamp per row; full content is fetched from the plugin detail.
type versionResponse struct {
	VersionID    string           `json:"version_id"`
	PluginID     string           `json:"plugin_id"`
	Version      string           `json:"version"`
	ManifestHash string           `json:"manifest_hash"`
	PluginHash   string           `json:"plugin_hash"`
	Relations    []map[string]any `json:"relations"`
	Changelog    *string          `json:"changelog,omitempty"`
	CreatedBy    string           `json:"created_by"`
	CreatedAt    time.Time        `json:"created_at" swaggertype:"string,date-time"`
}

type categoryResponse struct {
	CategoryID  string   `json:"category_id"`
	Name        string   `json:"name"`
	IconKey     string   `json:"icon_key,omitempty"`
	PluginTypes []string `json:"plugin_types"`
	SortOrder   int      `json:"sort_order"`
	PluginCount int      `json:"plugin_count"`
}

// List godoc
// @Summary List plugins
// @Description List Plugins visible in the authoritative caller Space using offset pagination.
// @Tags plugin
// @ID plugin.list
// @Accept json
// @Produce json
// @Security Bearer
// @Param scene_code query string true "Marketplace scene code"
// @Param plugin_type query string false "Plugin type. Required unless mode=mine, where omitting it lists every type the caller owns." Enums(expert,expert_team,skill,connector)
// @Param category_id query string false "Category ID (matches the plugin placement category in this scene)"
// @Param tag query []string false "Tag filters; repeatable or comma-separated, a plugin must carry every tag" collectionFormat(multi)
// @Param mode query string false "List mode; mine restricts to plugins owned by the caller" Enums(mine)
// @Param q query string false "Plugin name search query"
// @Param sort query string false "Sort order" Enums(newest,oldest,updated,name,placement,views,installs,downloads,comprehensive)
// @Param page query int false "Page number, default 1"
// @Param page_size query int false "Page size, default 20, max 100"
// @Success 200 {object} apiresponse.OffsetList[listItemResponse]
// @Failure 400 {object} apiresponse.Error "VALIDATION_ERROR"
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /plugins [get]
func (h *Handler) List(c *gin.Context) {
	caller, ok := caller(c)
	if !ok {
		unauthorized(c)
		return
	}
	page, pageSize, ok := pagination(c)
	if !ok {
		validation(c, "pagination")
		return
	}
	sceneCode := strings.TrimSpace(c.Query("scene_code"))
	if sceneCode == "" {
		validation(c, "scene_code")
		return
	}
	// mode is parsed before plugin_type because it decides whether plugin_type is
	// required at all.
	mode := c.Query("mode")
	if mode != "" && mode != "mine" {
		validation(c, "mode")
		return
	}
	// plugin_type is required for the marketplace grid, where a page mixing four
	// kinds of card has no coherent layout. It is OPTIONAL for mode=mine, which
	// backs 我的发布 and needs an 全部 tab listing every kind the caller owns in one
	// table. The service and repository already tolerate an empty type.
	pluginType := model.PluginType(c.Query("plugin_type"))
	if pluginType == "" && mode != "mine" {
		validation(c, "plugin_type")
		return
	}
	items, total, err := h.svc.List(c.Request.Context(), caller, pluginsvc.ListParams{PlacementCode: sceneCode, Type: pluginType, CategoryID: c.Query("category_id"), Tags: splitQuery(c.QueryArray("tag")), Keyword: c.Query("q"), Mine: mode == "mine", Sort: c.Query("sort"), Limit: pageSize, Offset: (page - 1) * pageSize})
	if err != nil {
		writeServiceError(c, err, "plugin.list")
		return
	}
	out := make([]listItemResponse, len(items))
	for i := range items {
		out[i] = listItemDTO(&items[i])
	}
	apiresponse.Offset(c, out, int(total), page, pageSize)
}

// Get godoc
// @Summary Get plugin
// @Description Return one Plugin and its visible one-level relations without leaking cross-Space existence.
// @Tags plugin
// @ID plugin.get
// @Accept json
// @Produce json
// @Security Bearer
// @Param plugin_id query string true "Plugin ID"
// @Param include_relations query bool false "Include one-level relations (default true)"
// @Success 200 {object} apiresponse.Data[detailResponse]
// @Failure 400 {object} apiresponse.Error "VALIDATION_ERROR"
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /plugins/detail [get]
func (h *Handler) Get(c *gin.Context) {
	caller, ok := caller(c)
	if !ok {
		unauthorized(c)
		return
	}
	includeRelations := true
	if raw, present := c.GetQuery("include_relations"); present {
		var err error
		includeRelations, err = strconv.ParseBool(raw)
		if err != nil {
			validation(c, "include_relations")
			return
		}
	}
	v, err := h.svc.Detail(c.Request.Context(), caller, c.Query("plugin_id"), includeRelations)
	if err != nil {
		writeServiceError(c, err, "plugin.get")
		return
	}
	apiresponse.OK(c, detailDTO(v))
}

// Upsert godoc
// @Summary Upsert plugin
// @Description Create or replace a Plugin from {plugin,relations}; owner and Space remain server-derived.
// @Tags plugin
// @ID plugin.upsert
// @Accept json
// @Produce json
// @Security Bearer
// @Param body body upsertRequest true "Plugin current state and relations"
// @Success 200 {object} apiresponse.Data[detailResponse]
// @Failure 400 {object} apiresponse.Error "VALIDATION_ERROR"
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 409 {object} apiresponse.Error "CONFLICT"
// @Failure 413 {object} apiresponse.Error "PAYLOAD_TOO_LARGE"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /plugins/upsert [post]
func (h *Handler) Upsert(c *gin.Context) {
	caller, ok := caller(c)
	if !ok {
		unauthorized(c)
		return
	}
	var req upsertRequest
	if !decode(c, &req) {
		return
	}
	var (
		v   *pluginsvc.Detail
		err error
	)
	if strings.TrimSpace(req.Plugin.PluginID) == "" {
		v, err = h.svc.Create(c.Request.Context(), caller, req.serviceRequest())
	} else {
		v, err = h.svc.Update(c.Request.Context(), caller, req.Plugin.PluginID, req.serviceRequest())
	}
	if err != nil {
		writeServiceError(c, err, "plugin.upsert")
		return
	}
	apiresponse.OK(c, detailDTO(v))
}

// Delete godoc
// @Summary Delete plugin
// @Description Soft-delete a caller-owned Plugin; live incoming relations from other Plugins block deletion.
// @Tags plugin
// @ID plugin.delete
// @Accept json
// @Produce json
// @Security Bearer
// @Param body body deleteRequest true "Plugin ID"
// @Success 200 {object} apiresponse.Data[deleteResponse]
// @Failure 400 {object} apiresponse.Error "VALIDATION_ERROR"
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 409 {object} apiresponse.Error "CONFLICT"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /plugins/delete [post]
func (h *Handler) Delete(c *gin.Context) {
	caller, ok := caller(c)
	if !ok {
		unauthorized(c)
		return
	}
	var req deleteRequest
	if !decode(c, &req) {
		return
	}
	if err := h.svc.Delete(c.Request.Context(), caller, req.PluginID); err != nil {
		writeServiceError(c, err, "plugin.delete")
		return
	}
	apiresponse.OK(c, deleteResponse{PluginID: req.PluginID, Deleted: true})
}

// ListVersions godoc
// @Summary List plugin versions
// @Description List a visible Plugin's version history (one immutable snapshot per save, newest first) using offset pagination. Each row is metadata only (label, hashes, changelog, timestamp); full manifest/package content is fetched from the plugin detail, so intermediate/rolled-back saves are not exposed in bulk.
// @Tags plugin
// @ID plugin.version.list
// @Accept json
// @Produce json
// @Security Bearer
// @Param plugin_id query string true "Plugin ID"
// @Param page query int false "Page number, default 1"
// @Param page_size query int false "Page size, default 20, max 100"
// @Success 200 {object} apiresponse.OffsetList[versionResponse]
// @Failure 400 {object} apiresponse.Error "VALIDATION_ERROR"
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /plugins/versions [get]
func (h *Handler) ListVersions(c *gin.Context) {
	caller, ok := caller(c)
	if !ok {
		unauthorized(c)
		return
	}
	page, size, ok := pagination(c)
	if !ok {
		validation(c, "pagination")
		return
	}
	items, total, err := h.svc.ListVersions(c.Request.Context(), caller, c.Query("plugin_id"), size, (page-1)*size)
	if err != nil {
		writeServiceError(c, err, "plugin.version.list")
		return
	}
	out := make([]versionResponse, len(items))
	for i, x := range items {
		out[i] = versionDTO(x)
	}
	apiresponse.Offset(c, out, int(total), page, size)
}

// ListCategories godoc
// @Summary List plugin categories
// @Description List placement-aware categories backed by Plugins visible in the current Space.
// @Tags plugin
// @ID plugin_category.list
// @Accept json
// @Produce json
// @Security Bearer
// @Param scene_code query string true "Marketplace scene code"
// @Param plugin_type query string true "Plugin type" Enums(expert,expert_team,skill,connector)
// @Success 200 {object} apiresponse.Data[[]categoryResponse]
// @Failure 400 {object} apiresponse.Error "VALIDATION_ERROR"
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /plugin_categories [get]
func (h *Handler) ListCategories(c *gin.Context) {
	caller, ok := caller(c)
	if !ok {
		unauthorized(c)
		return
	}
	placement, typ := strings.TrimSpace(c.Query("scene_code")), model.PluginType(c.Query("plugin_type"))
	if placement == "" {
		validation(c, "scene_code")
		return
	}
	if typ == "" {
		validation(c, "plugin_type")
		return
	}
	if h.categories == nil {
		apiresponse.Internal(c, errors.New("plugin category service is unavailable"), "plugin_category.list")
		return
	}
	items, err := h.categories.ListCategories(c.Request.Context(), caller, placement, typ)
	if err != nil {
		writeServiceError(c, err, "plugin_category.list")
		return
	}
	out := make([]categoryResponse, len(items))
	for i, x := range items {
		out[i] = categoryResponse{CategoryID: x.ID, Name: x.Name, IconKey: x.IconKey, PluginTypes: stringSlice(x.PluginTypes), SortOrder: x.SortOrder, PluginCount: x.PluginCount}
	}
	apiresponse.OK(c, out)
}

// ListTags godoc
// @Summary List plugin tags
// @Description Aggregate free-form tags from Plugins visible to the caller (same visible set as GET /plugins), ordered by descending plugin count with alphabetical tie-break.
// @Tags plugin
// @ID plugin_tag.list
// @Accept json
// @Produce json
// @Security Bearer
// @Param scene_code query string false "Restrict aggregation to Plugins visible in a marketplace scene"
// @Param plugin_type query string false "Plugin type" Enums(expert,expert_team,skill,connector)
// @Param q query string false "Fuzzy tag search (substring)"
// @Param mode query string false "Scope: mine restricts aggregation to caller-owned records"
// @Param limit query int false "Max items returned; default 50, max 100"
// @Success 200 {object} apiresponse.Data[[]model.TagFilter]
// @Failure 400 {object} apiresponse.Error "VALIDATION_ERROR"
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /plugin_tags [get]
func (h *Handler) ListTags(c *gin.Context) {
	caller, ok := caller(c)
	if !ok {
		unauthorized(c)
		return
	}
	limit := 0
	if raw := c.Query("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err == nil && parsed > 0 {
			limit = min(parsed, 100)
		}
	}
	mode := c.Query("mode")
	if mode != "" && mode != "mine" {
		validation(c, "mode")
		return
	}
	tags, err := h.svc.ListTags(c.Request.Context(), caller, pluginsvc.TagListParams{
		PlacementCode: strings.TrimSpace(c.Query("scene_code")),
		Type:          model.PluginType(c.Query("plugin_type")),
		Keyword:       strings.TrimSpace(c.Query("q")),
		Mine:          mode == "mine",
		Limit:         limit,
	})
	if err != nil {
		writeServiceError(c, err, "plugin_tag.list")
		return
	}
	apiresponse.OK(c, tags)
}

func caller(c *gin.Context) (pluginsvc.Caller, bool) {
	identity, ok := marketmiddleware.Identity(c)
	spaceID := marketmiddleware.SpaceID(c)
	if !ok || identity.UID == "" || spaceID == "" {
		return pluginsvc.Caller{}, false
	}
	out := pluginsvc.Caller{UID: identity.UID, Name: identity.Name, SpaceID: spaceID, RequestID: logging.RequestIDFromGin(c), IsSystemAdmin: identity.Role == marketmiddleware.RoleSuperAdmin}
	// The caller's role IN THE SPACE THIS REQUEST IS FOR. Reading it by spaceID
	// rather than taking any single value out of the map is what stops an admin of
	// Space B from acting as a reviewer while operating in Space A. A missing entry
	// is a plain member. A system admin outranks any Space role.
	out.SpaceRole = identity.SpaceRole(spaceID)
	if out.IsSystemAdmin && out.SpaceRole < pluginsvc.SpaceRoleOwner {
		out.SpaceRole = pluginsvc.SpaceRoleOwner
	}
	if bot, ok := marketmiddleware.BotIdentity(c); ok {
		out.BotUID, out.BotName = bot.BotUID, bot.BotName
	}
	return out, true
}
func pagination(c *gin.Context) (int, int, bool) {
	page, size := 1, 20
	var err error
	if raw := c.Query("page"); raw != "" {
		page, err = strconv.Atoi(raw)
		if err != nil || page < 1 {
			return 0, 0, false
		}
	}
	if raw := c.Query("page_size"); raw != "" {
		size, err = strconv.Atoi(raw)
		if err != nil || size < 1 || size > 100 {
			return 0, 0, false
		}
	}
	return page, size, true
}
func decode(c *gin.Context, dst any) bool {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBodyBytes)
	dec := json.NewDecoder(c.Request.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			apiresponse.Fail(c, http.StatusRequestEntityTooLarge, "PAYLOAD_TOO_LARGE", "request body is too large", map[string]any{"max_bytes": maxBodyBytes}, "Reduce the request size.")
			return false
		}
		validation(c, "body")
		return false
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		validation(c, "body")
		return false
	}
	return true
}
func unauthorized(c *gin.Context) {
	apiresponse.Fail(c, http.StatusUnauthorized, errcode.Unauthorized, "authentication required", map[string]any{"reason": "invalid"}, "Authenticate and try again.")
}
func validation(c *gin.Context, field string) {
	apiresponse.Fail(c, http.StatusBadRequest, errcode.BadRequest, "request validation failed", map[string]any{"field": field, "reason": "invalid"}, "Correct the request and try again.")
}

// splitQuery flattens repeated query values and comma-joined values into one
// trimmed list, mirroring the legacy market list endpoints so both wire styles
// (?tag=a&tag=b and ?tag=a,b) filter identically.
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

// writeServiceError maps a service sentinel onto the wire shape.
//
// Every 409 arm below MUST set a distinct conflict_reason, and the set is
// documented in the spec (cmd/marketplace-api/main.go's @description) because the
// status code alone cannot tell a client whether to cancel a pending request, pick
// a new version label, or simply retry. `state` is the residual for a raced state
// change with no more specific reading — adding a sentinel to that arm rather than
// giving it its own reason is how an actionable failure ends up unactionable, which
// is what happened to the single-pending refusal and to label collisions. Keep the
// three in sync: the sentinel, the reason string, and the @description list.
func writeServiceError(c *gin.Context, err error, operation string) {
	var fieldErr *pluginsvc.ReviewFieldError
	switch {
	case errors.As(err, &fieldErr):
		// Structured field error from the review path: surface the exact field
		// and reason so the browser can show the user which input to fix,
		// instead of collapsing every cause into {"field":"body","reason":"invalid"}.
		apiresponse.Fail(c, http.StatusBadRequest, errcode.BadRequest, "request validation failed", map[string]any{"field": fieldErr.Field, "reason": fieldErr.Reason}, "Correct the request and try again.")
	case errors.Is(err, pluginsvc.ErrInvalidRequest):
		validation(c, "body")
	case errors.Is(err, pluginsvc.ErrReviewInvalid):
		validation(c, "body")
	// A named field error from legacy paths (ErrReasonRequired) still works.
	case errors.Is(err, pluginsvc.ErrReasonRequired):
		validation(c, "reason")
	// Invalid parse task for review submission: name the field.
	case errors.Is(err, pluginsvc.ErrInvalidParseTask):
		apiresponse.Fail(c, http.StatusBadRequest, errcode.BadRequest, "parse task is invalid or already consumed", map[string]any{"field": "parse_task_id", "reason": "invalid_or_consumed"}, "Upload a new package and try again.")
	// An upgrade submission with no content: name the field so the client knows
	// what to send rather than guessing at a generic "body".
	case errors.Is(err, pluginsvc.ErrReviewContentRequired):
		apiresponse.Fail(c, http.StatusBadRequest, errcode.BadRequest, "a listed plugin must be submitted with the reviewed content", map[string]any{"field": "manifest_json", "reason": "required"}, "Send manifest_json and plugin_json, or parse_task_id, with the submission.")
	// Parse task against a non-skill plugin.
	case errors.Is(err, pluginsvc.ErrReviewParseTaskType):
		apiresponse.Fail(c, http.StatusBadRequest, errcode.BadRequest, "parse_task_id is only valid for skill plugins", map[string]any{"field": "parse_task_id", "reason": "only_valid_for_skill_plugins"}, "Use manifest_json/plugin_json for non-skill plugins.")
	// Zip package name mismatch.
	case errors.Is(err, pluginsvc.ErrReviewNameMismatch):
		apiresponse.Fail(c, http.StatusBadRequest, errcode.BadRequest, "the uploaded package name does not match the plugin", map[string]any{"field": "parse_task_id", "reason": "name_mismatch"}, "Upload a package whose name matches the existing plugin.")
	// Names the field so the form can mark the version input rather than
	// reporting a generic bad body.
	case errors.Is(err, pluginsvc.ErrVersionRegressed):
		apiresponse.Fail(c, http.StatusBadRequest, errcode.BadRequest, "version must not go backwards", map[string]any{"field": "version", "reason": "must_not_decrease"}, "Use the current version or a higher one.")
	// Label already in the published set. Same shape as ErrVersionRegressed
	// from the client's perspective (fix the version field); surfaced at approve
	// time when a concurrent writer published the same label while the request
	// sat pending, so it is a 409 Conflict rather than a 400 — the label was
	// valid at submit but is no longer available.
	case errors.Is(err, pluginsvc.ErrLabelTaken):
		apiresponse.Fail(c, http.StatusConflict, errcode.Conflict, "version label is already published", map[string]any{"field": "version", "conflict_reason": "label_taken"}, "Use a different version label; the applicant must resubmit.")
	// A state conflict, not a permission problem: the owner may change this
	// plugin, but only through a review request. Self-delisting is no longer an
	// option, so the hint points at the two things that actually work.
	case errors.Is(err, pluginsvc.ErrListedRequiresReview):
		apiresponse.Fail(c, http.StatusConflict, errcode.Conflict, "a plugin listed to the organization cannot be edited directly", map[string]any{"conflict_reason": "listed_requires_review"}, "Publish a new version through review, or ask a Space admin to delist it first.")
	// Also a state conflict: the pending request has to be resolved or canceled
	// before this particular change is coherent.
	case errors.Is(err, pluginsvc.ErrReviewPending):
		apiresponse.Fail(c, http.StatusConflict, errcode.Conflict, "a review request is pending on this plugin", map[string]any{"conflict_reason": "review_pending"}, "Cancel the pending review request first.")
	// Publish/delist state conflicts. Reported as conflicts rather than silently
	// succeeding so a second button press tells the client its view is stale.
	case errors.Is(err, pluginsvc.ErrAlreadyPublished):
		apiresponse.Fail(c, http.StatusConflict, errcode.Conflict, "plugin is already published", map[string]any{"conflict_reason": "already_published"}, "Refresh the plugin and try again.")
	case errors.Is(err, pluginsvc.ErrNotPublished):
		apiresponse.Fail(c, http.StatusConflict, errcode.Conflict, "plugin is not published", map[string]any{"conflict_reason": "not_published"}, "Only a published plugin can be delisted.")
	// Changing the Space-wide policy requires the same reviewer role as handling
	// individual requests: Space owner or admin.
	case errors.Is(err, pluginsvc.ErrReviewPolicyForbidden):
		apiresponse.Fail(c, http.StatusForbidden, errcode.PermissionDenied, "operation requires the Space owner or admin role", map[string]any{"required_role": "space_admin"}, "Ask a Space owner or admin to perform this action.")
	// Authorization refusal, distinct from "not found": the caller is in the right
	// Space and may know the resource exists, they simply lack the reviewer role.
	// Without this branch a permission error falls through to 500.
	case errors.Is(err, pluginsvc.ErrReviewForbidden):
		apiresponse.Fail(c, http.StatusForbidden, errcode.PermissionDenied, "operation requires the Space owner or admin role", map[string]any{"required_role": "space_admin"}, "Ask a Space owner or admin to perform this action.")
	case errors.Is(err, pluginsvc.ErrNotFound):
		apiresponse.Fail(c, http.StatusNotFound, errcode.NotFound, "plugin not found", map[string]any{"resource": "plugin"}, "Verify the plugin_id and try again.")
	case errors.Is(err, pluginsvc.ErrTooLarge):
		apiresponse.Fail(c, http.StatusRequestEntityTooLarge, errcode.FileTooLarge, "plugin artifact exceeds the size limit", nil, "Reduce the attachment size and try again.")
	case errors.Is(err, pluginsvc.ErrConflict):
		apiresponse.Fail(c, http.StatusConflict, errcode.Conflict, "plugin state conflicts with an existing resource", map[string]any{"conflict_reason": "state"}, "Refresh the resource and try again.")
	// Transient InnoDB lock contention (a deadlock victim, or a lock-wait timeout)
	// between two writers on the same plugin that take the plugin row and its
	// review request in opposite orders — a submit or a decision racing a save,
	// delete, publish or delist. Nothing committed, so it is safe — and expected —
	// to retry. Reported as a retryable conflict rather than a 500 so the client
	// re-issues the request instead of surfacing an internal error. The
	// conflict_reason stays "deadlock" for both server errors: it is one retry
	// instruction to a client, and splitting it would move the wire contract.
	case errors.Is(err, pluginsvc.ErrDeadlock):
		apiresponse.Fail(c, http.StatusConflict, errcode.Conflict, "the request conflicted with a concurrent change", map[string]any{"conflict_reason": "deadlock", "retryable": true}, "Please retry the request.")
	default:
		apiresponse.Internal(c, err, operation)
	}
}
func pluginDTO(p *model.Plugin) pluginResponse {
	if p == nil {
		return pluginResponse{}
	}
	return pluginResponse{PluginID: p.ID, PluginName: p.Name, PluginType: p.Type, IsEmbedded: p.IsEmbedded, CategoryID: p.CategoryID, Tags: stringSlice(p.Tags), Publisher: p.Publisher, OwnerID: p.OwnerUID, SpaceID: p.SpaceID, Visibility: p.Visibility, ListingState: p.ListingState, DisplayStatus: p.DisplayStatus(p.HasPendingReview, p.LatestReviewStatus), ReviewID: optionalID(p.LatestReviewID), CreatorName: p.CreatorName, CreatedByType: p.CreatedByType, CreatedByBotID: p.CreatedByBotUID, CreatedByBotName: p.CreatedByBotName, Icon: p.Icon, IconURL: p.IconURL, ToolCount: connectorCount(p), ViewCount: p.ViewCount, InstallCount: p.InstallCount, DownloadCount: p.DownloadCount, ManifestJSON: normalizedObjectRaw(p.Manifest), PluginJSON: normalizedObjectRaw(p.Package), ManifestHash: p.ManifestHash, PluginHash: p.PluginHash, CurrentVersionID: p.CurrentVersionID, CurrentVersion: p.CurrentVersion, Status: p.Status, CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt}
}
func listItemDTO(p *model.Plugin) listItemResponse {
	if p == nil {
		return listItemResponse{}
	}
	return listItemResponse{PluginID: p.ID, PluginName: p.Name, PluginType: p.Type, IsEmbedded: p.IsEmbedded, CategoryID: p.CategoryID, Tags: stringSlice(p.Tags), Publisher: p.Publisher, OwnerID: p.OwnerUID, SpaceID: p.SpaceID, Visibility: p.Visibility, ListingState: p.ListingState, DisplayStatus: p.DisplayStatus(p.HasPendingReview, p.LatestReviewStatus), ReviewID: optionalID(p.LatestReviewID), CreatorName: p.CreatorName, CreatedByType: p.CreatedByType, CreatedByBotID: p.CreatedByBotUID, CreatedByBotName: p.CreatedByBotName, Icon: p.Icon, IconURL: p.IconURL, ToolCount: connectorCount(p), MemberCount: teamMemberCount(p), ViewCount: p.ViewCount, InstallCount: p.InstallCount, DownloadCount: p.DownloadCount, ManifestJSON: normalizedObjectRaw(p.Manifest), ManifestHash: p.ManifestHash, PluginHash: p.PluginHash, CurrentVersionID: p.CurrentVersionID, CurrentVersion: p.CurrentVersion, Status: p.Status, CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt}
}

// connectorCount and teamMemberCount emit typed counts only for the plugin
// type they describe, so a zero on other types reads as absent, not empty.
func connectorCount(p *model.Plugin) *int {
	if p.Type != model.PluginTypeConnector {
		return nil
	}
	count := p.ToolCount
	return &count
}
func teamMemberCount(p *model.Plugin) *int {
	if p.Type != model.PluginTypeExpertTeam {
		return nil
	}
	count := p.MemberCount
	return &count
}
func detailDTO(d *pluginsvc.Detail) detailResponse {
	if d == nil {
		return detailResponse{Relations: []relationResponse{}}
	}
	rels := make([]relationResponse, len(d.Relations))
	for i, x := range d.Relations {
		sourceType := x.SourcePluginType
		if sourceType == "" && d.Plugin != nil && x.SourcePluginID == d.Plugin.ID {
			sourceType = d.Plugin.Type
		}
		rels[i] = relationResponse{RelationID: x.ID, SourcePluginID: x.SourcePluginID, TargetPluginID: x.TargetPluginID, RelationType: x.Type, SortOrder: x.SortOrder, Data: normalizedObjectRaw(x.Data)}
	}
	out := detailResponse{Plugin: pluginDTO(d.Plugin), Relations: rels}
	if d.RelationResult != nil {
		out.RelationResult = &relationResultResponse{Created: d.RelationResult.Created, Updated: d.RelationResult.Updated, Deleted: d.RelationResult.Deleted}
	}
	return out
}
func versionDTO(x model.PluginVersion) versionResponse {
	return versionResponse{VersionID: x.ID, PluginID: x.PluginID, Version: x.Version, ManifestHash: x.ManifestHash, PluginHash: x.PluginHash, Relations: versionRelationSlice(x.Relations), Changelog: x.Changelog, CreatedBy: x.CreatedBy, CreatedAt: x.CreatedAt}
}

func rawJSON(value any) json.RawMessage {
	raw, _ := json.Marshal(value)
	return raw
}

func normalizedObjectRaw(raw json.RawMessage) json.RawMessage {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var out map[string]any
	if dec.Decode(&out) != nil || out == nil {
		return json.RawMessage(`{}`)
	}
	return rawJSON(out)
}

func stringSlice(raw json.RawMessage) []string {
	var out []string
	if len(raw) == 0 || json.Unmarshal(raw, &out) != nil || out == nil {
		return []string{}
	}
	return out
}

func contentDisposition(name string) string {
	name = path.Base(strings.ReplaceAll(name, "\\", "/"))
	name = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || r == '"' || r == '\\' {
			return '_'
		}
		return r
	}, name)
	if name == "" || name == "." {
		name = "download"
	}
	// filename= carries an ASCII-only fallback (non-ASCII runes are not legal in
	// the quoted form), and filename*=UTF-8'' carries the percent-encoded original
	// for RFC 5987/6266-aware clients, so a non-ASCII plugin name is not emitted
	// raw inside filename="…".
	ascii := strings.Map(func(r rune) rune {
		if r > 0x7f {
			return '_'
		}
		return r
	}, name)
	return `attachment; filename="` + ascii + `"; filename*=UTF-8''` + rfc5987Escape(name)
}

// rfc5987Escape percent-encodes a UTF-8 string for an ext-value (RFC 5987),
// leaving only the attr-char set unescaped.
func rfc5987Escape(s string) string {
	const upperhex = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') ||
			strings.IndexByte("!#$&+-.^_`|~", c) >= 0 {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(upperhex[c>>4])
		b.WriteByte(upperhex[c&0x0f])
	}
	return b.String()
}

func versionRelationSlice(raw json.RawMessage) []map[string]any {
	var relations []model.PluginRelation
	if len(raw) == 0 || json.Unmarshal(raw, &relations) != nil || relations == nil {
		return []map[string]any{}
	}
	out := make([]map[string]any, 0, len(relations))
	for _, relation := range relations {
		out = append(out, map[string]any{
			"relation_id":      relation.ID,
			"source_plugin_id": relation.SourcePluginID,
			"target_plugin_id": relation.TargetPluginID,
			"relation_type":    relation.Type,
			"sort_order":       relation.SortOrder,
			"data":             normalizedObjectRaw(relation.Data),
		})
	}
	return out
}

// optionalID omits an empty derived id from the response rather than sending "".
func optionalID(id string) *string {
	if id == "" {
		return nil
	}
	return &id
}
