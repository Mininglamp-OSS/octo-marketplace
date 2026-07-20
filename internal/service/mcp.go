// Package service holds the MCP catalog business rules: request validation,
// secret redaction (doc §5), the flat-write -> domain -> nested-read mapping
// (doc §3.3), visibility judgement (doc §4.2/§4.4), and uniqueness. It depends
// on a Store abstraction over the repository so the rules are unit-testable
// without a database. Handlers call this layer; this layer never touches HTTP.
package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/apierr"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/id"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/repository"
)

const (
	defaultLimit = 20
	maxLimit     = 100
)

// Store is the persistence surface the service needs. *repository.Repository
// satisfies it; tests provide a fake.
type Store interface {
	Create(ctx context.Context, m *model.MCP) error
	Update(ctx context.Context, m *model.MCP) error
	GetByID(ctx context.Context, id string) (*model.MCP, error)
	SoftDelete(ctx context.Context, id string, now time.Time) error
	List(ctx context.Context, f repository.ListFilter) ([]model.MCP, int, []model.CategoryFilter, error)
	// SystemNameExists / SystemSlugExists guard the admin Create/Update path
	// against duplicate name/slug across live visibility=system rows. The
	// DB UNIQUE index does not catch these because system rows carry
	// space_id=NULL and MySQL treats NULL columns as distinct in a unique
	// index.
	SystemNameExists(ctx context.Context, name, exceptID string) (bool, error)
	SystemSlugExists(ctx context.Context, slug, exceptID string) (bool, error)
}

// Caller is the resolved identity + Space for a request, stamped server-side
// from the Octo token and X-Space-Id (doc §1). The service never trusts body
// identity fields.
type Caller struct {
	UID     string
	Name    string
	SpaceID string
}

// Service implements the MCP catalog operations.
type Service struct {
	store             Store
	now               func() time.Time
	icons             IconStore
	iconCfg           IconConfig
	probeAllowPrivate bool
}

// IconStore is the object-storage surface the icon upload needs. *blob.S3Client
// satisfies it; tests provide a fake. It is separate from Store so the catalog
// rules stay testable without a bucket, and nil when storage is unconfigured
// (icon upload then returns a 400 rather than panicking).
type IconStore interface {
	Put(ctx context.Context, key, contentType string, data []byte) (url string, err error)
}

// IconConfig carries the object-storage layout for icons: the partition segment
// and the max upload size. Populated from config in main.
type IconConfig struct {
	Partition   string
	MaxBytes    int64
	ContentType string // stored content type; icons are normalized to image/png
}

// New returns a Service backed by store. The clock is injectable for tests.
func New(store Store) *Service {
	return &Service{store: store, now: time.Now}
}

// WithProbeAllowPrivate permits MCP probes to reach private network targets.
// It is intended only for trusted, self-hosted deployments and defaults false.
func (s *Service) WithProbeAllowPrivate(allow bool) *Service {
	s.probeAllowPrivate = allow
	return s
}

// WithIconStore attaches the object-storage uploader + layout so POST
// /mcps/{id}/icon works. When never called (storage unconfigured), UploadIcon
// returns a 400 explaining icon storage is disabled.
func (s *Service) WithIconStore(store IconStore, cfg IconConfig) *Service {
	s.icons = store
	if cfg.MaxBytes <= 0 {
		cfg.MaxBytes = defaultIconMaxBytes
	}
	if cfg.Partition == "" {
		cfg.Partition = "mcp"
	}
	if cfg.ContentType == "" {
		cfg.ContentType = "image/png"
	}
	s.iconCfg = cfg
	return s
}

const defaultIconMaxBytes = 2 << 20 // 2 MiB

// ListParams carries the query parameters for the two list endpoints.
type ListParams struct {
	Keyword              string
	Categories           []string
	Tags                 []string
	Transports           []string
	Visibilities         []string
	Sources              []string
	VerificationStatuses []string
	Sort                 string
	Limit                int
	Offset               int
}

// Create validates + normalizes a flat create body, redacts secrets, stamps
// identity, and persists a new record. Returns the nested detail (doc §4.1).
func (s *Service) Create(ctx context.Context, caller Caller, req model.CreateRequest) (model.Detail, *apierr.Error) {
	m, apiErr := s.buildFromCreate(caller, req)
	if apiErr != nil {
		return model.Detail{}, apiErr
	}
	if err := s.store.Create(ctx, m); err != nil {
		return model.Detail{}, mapStoreError(err)
	}
	return detailForCaller(m, caller), nil
}

// Get returns a single record's detail if visible to the caller, else 404
// (doc §4.4 — never a leaky 403).
func (s *Service) Get(ctx context.Context, caller Caller, mcpID string) (model.Detail, *apierr.Error) {
	m, apiErr := s.loadVisible(ctx, caller, mcpID)
	if apiErr != nil {
		return model.Detail{}, apiErr
	}
	return detailForCaller(m, caller), nil
}

// Patch applies a partial update. Only the owner may mutate; a non-owner (or a
// record in another Space) is indistinguishable from "not found" for reads,
// but a visible-yet-not-owned record yields forbidden (doc §4.5).
func (s *Service) Patch(ctx context.Context, caller Caller, mcpID string, req model.PatchRequest) (model.Detail, *apierr.Error) {
	m, apiErr := s.loadVisible(ctx, caller, mcpID)
	if apiErr != nil {
		return model.Detail{}, apiErr
	}
	if m.OwnerUID != caller.UID {
		return model.Detail{}, apierr.Forbidden()
	}

	if apiErr := s.applyPatch(m, req); apiErr != nil {
		return model.Detail{}, apiErr
	}
	m.UpdatedAt = s.now()

	if err := s.store.Update(ctx, m); err != nil {
		return model.Detail{}, mapStoreError(err)
	}
	return detailForCaller(m, caller), nil
}

// Delete soft-deletes a record. Owner only; the same visibility gate applies.
func (s *Service) Delete(ctx context.Context, caller Caller, mcpID string) *apierr.Error {
	m, apiErr := s.loadVisible(ctx, caller, mcpID)
	if apiErr != nil {
		return apiErr
	}
	if m.OwnerUID != caller.UID {
		return apierr.Forbidden()
	}
	if err := s.store.SoftDelete(ctx, mcpID, s.now()); err != nil {
		return mapStoreError(err)
	}
	return nil
}

// List returns the visible-to-caller set inside the current Space (doc §4.2).
func (s *Service) List(ctx context.Context, caller Caller, p ListParams) (model.ListResponse, *apierr.Error) {
	return s.list(ctx, caller, p, false)
}

// ListMine returns records owned by the caller in the current Space (doc §4.3).
func (s *Service) ListMine(ctx context.Context, caller Caller, p ListParams) (model.ListResponse, *apierr.Error) {
	return s.list(ctx, caller, p, true)
}

// IconResult is what UploadIcon returns to the handler: the public URL the
// client should store + render. Mirrors the frontend-agreed { url } contract
// (LSC-80 #1).
type IconResult struct {
	URL     string `json:"icon_url"`
	Version int    `json:"version"`
}

// UploadIcon stores a new icon for an owned MCP in object storage and points
// the record's icon column at the public URL (bug #1: icons move off base64).
// Owner-only, same visibility gate as Patch. The version counter is bumped so
// the storage key is unique per upload and clients can cache-bust. data is the
// already-read image bytes; contentType is advisory (icons are stored as PNG).
func (s *Service) UploadIcon(ctx context.Context, caller Caller, mcpID string, data []byte, contentType string) (IconResult, *apierr.Error) {
	if s.icons == nil {
		return IconResult{}, apierr.InvalidRequest("icon storage is not configured")
	}
	if len(data) == 0 {
		return IconResult{}, apierr.InvalidRequest("icon file is empty",
			apierr.Detail{Field: "file", Reason: "required"})
	}
	if int64(len(data)) > s.iconCfg.MaxBytes {
		return IconResult{}, apierr.InvalidRequest(
			fmt.Sprintf("icon exceeds the %d byte limit", s.iconCfg.MaxBytes),
			apierr.Detail{Field: "file", Reason: "too_large"})
	}

	m, apiErr := s.loadVisible(ctx, caller, mcpID)
	if apiErr != nil {
		return IconResult{}, apiErr
	}
	if m.OwnerUID != caller.UID {
		return IconResult{}, apierr.Forbidden()
	}

	version := m.IconVersion + 1
	key := iconKey(s.iconCfg.Partition, m.ID, version)
	url, err := s.icons.Put(ctx, key, s.iconCfg.ContentType, data)
	if err != nil {
		// The bucket error (endpoint, creds, network) must not leak to the
		// client; the handler logs the internal 500 cause.
		return IconResult{}, apierr.Internal()
	}

	m.Icon = url
	m.IconVersion = version
	m.UpdatedAt = s.now()
	if err := s.store.Update(ctx, m); err != nil {
		return IconResult{}, mapStoreError(err)
	}
	return IconResult{URL: url, Version: version}, nil
}

// iconKey builds the object-storage key for an icon:
// mcp_icon/{partition}/{mcp_id}/{version}.png (LSC-81 #1).
func iconKey(partition, mcpID string, version int) string {
	return fmt.Sprintf("mcp_icon/%s/%s/%d.png", partition, mcpID, version)
}

// ─── Admin surface ─────────────────────────────────────────────────────────
// Only reachable through the admin middleware (see internal/middleware/admin.go).
// The public /market/api/v1/* paths never call these.

// CreateSystem publishes a platform-provided record. Unlike Create, the
// visibility is always model.VisibilitySystem, space_id is empty (rows carry
// NULL in the DB), and any visibility supplied in the request body is
// ignored — the admin channel is the only path that can mint system MCPs.
//
// The uniqueness constraint is (owner_uid, space_id, name) among live rows.
// For system MCPs space_id=NULL, so two different admin identities can pick
// the same name; and the same admin identity cannot reuse a name across
// active rows. Callers are expected to use a stable admin uid so the second
// constraint acts as "no dupes across system entries" in practice.
func (s *Service) CreateSystem(ctx context.Context, caller Caller, req model.CreateRequest) (model.Detail, *apierr.Error) {
	req.Visibility = model.VisibilitySystem
	sysCaller := caller
	// System rows carry NULL space_id (docs/api/mcp-v1.md §3.1 visibility
	// notes). The buildSystemFromCreate path forces the record's SpaceID to
	// "" regardless of what the middleware handed us.
	m, apiErr := s.buildSystemFromCreate(sysCaller, req)
	if apiErr != nil {
		return model.Detail{}, apiErr
	}
	// Service-level uniqueness pre-check for system rows. The DB unique
	// index (owner_uid, space_id, name_live) does not fire because
	// space_id is NULL and MySQL treats NULL columns as distinct — two
	// rows with the same (owner_uid, name) both pass. Same for slug.
	if apiErr := s.checkSystemDupes(ctx, m.Name, m.Slug, ""); apiErr != nil {
		return model.Detail{}, apiErr
	}
	if err := s.store.Create(ctx, m); err != nil {
		return model.Detail{}, mapStoreError(err)
	}
	return m.ToDetail(), nil
}

// ListSystem returns every visibility=system record across all Spaces. Used
// by the admin console to browse existing platform-provided MCPs.
// Pagination + keyword + category filters apply identically to the public
// list surface (doc §4.2), except the visibility rule collapses to
// "visibility = system".
func (s *Service) ListSystem(ctx context.Context, p ListParams) (model.ListResponse, *apierr.Error) {
	filter := repository.ListFilter{
		Keyword:    p.Keyword,
		Categories: p.Categories, Tags: p.Tags, Transports: p.Transports,
		Visibilities: p.Visibilities, Sources: p.Sources, VerificationStatuses: p.VerificationStatuses, Sort: p.Sort,
		Limit:      clampLimit(p.Limit),
		Offset:     clampOffset(p.Offset),
		SystemOnly: true,
	}
	records, total, cats, err := s.store.List(ctx, filter)
	if err != nil {
		return model.ListResponse{}, mapStoreError(err)
	}
	items := make([]model.ListItem, 0, len(records))
	for i := range records {
		item := records[i].ToListItem()
		enrichListItem(&item, &records[i], p.Keyword, "")
		items = append(items, item)
	}
	if cats == nil {
		cats = []model.CategoryFilter{{Key: model.CategoryKeyAll, Count: total}}
	}
	return model.ListResponse{Items: items, Total: total, Categories: cats}, nil
}

// GetSystem returns the full detail of a visibility=system record. Any other
// visibility is reported as not_found so the admin surface cannot be used to
// enumerate Space-scoped records via ID guessing.
func (s *Service) GetSystem(ctx context.Context, mcpID string) (model.Detail, *apierr.Error) {
	m, apiErr := s.loadSystem(ctx, mcpID)
	if apiErr != nil {
		return model.Detail{}, apiErr
	}
	return m.ToDetail(), nil
}

// UpdateSystem applies a partial update to a visibility=system record. Unlike
// the public Patch, ownership is not checked — any authenticated admin may
// edit any system MCP. The record's visibility is pinned to system: a caller
// cannot use PATCH to promote a system row to public/private (which would
// silently strip it from the admin list and grant it a fictitious Space).
func (s *Service) UpdateSystem(ctx context.Context, mcpID string, req model.PatchRequest) (model.Detail, *apierr.Error) {
	m, apiErr := s.loadSystem(ctx, mcpID)
	if apiErr != nil {
		return model.Detail{}, apiErr
	}
	if req.Visibility != nil && *req.Visibility != model.VisibilitySystem {
		return model.Detail{}, apierr.InvalidVisibility()
	}
	req.Visibility = nil // never let applyPatch mutate visibility on a system row.
	if apiErr := s.applyPatch(m, req); apiErr != nil {
		return model.Detail{}, apiErr
	}
	// Same pre-check as CreateSystem — the DB UNIQUE cannot detect
	// name/slug collisions among system rows (NULL space_id defeats the
	// composite index). Runs AFTER applyPatch so we compare the final
	// values, and passes the current id so a no-op rename doesn't self-
	// collide.
	if apiErr := s.checkSystemDupes(ctx, m.Name, m.Slug, m.ID); apiErr != nil {
		return model.Detail{}, apiErr
	}
	m.UpdatedAt = s.now()
	if err := s.store.Update(ctx, m); err != nil {
		return model.Detail{}, mapStoreError(err)
	}
	return m.ToDetail(), nil
}

// DeleteSystem soft-deletes a visibility=system record. Non-system rows yield
// not_found, matching GetSystem/UpdateSystem semantics.
func (s *Service) DeleteSystem(ctx context.Context, mcpID string) *apierr.Error {
	if _, apiErr := s.loadSystem(ctx, mcpID); apiErr != nil {
		return apiErr
	}
	if err := s.store.SoftDelete(ctx, mcpID, s.now()); err != nil {
		return mapStoreError(err)
	}
	return nil
}

// checkSystemDupes fires the service-level uniqueness guard the admin path
// needs because MySQL's UNIQUE ignores NULL space_id — see doc string on
// Store.SystemNameExists / SystemSlugExists. Pass exceptID="" for Create,
// or the record id for Update so a same-record no-op rename does not
// self-collide.
func (s *Service) checkSystemDupes(ctx context.Context, name, slug, exceptID string) *apierr.Error {
	exists, err := s.store.SystemNameExists(ctx, name, exceptID)
	if err != nil {
		return mapStoreError(err)
	}
	if exists {
		return apierr.NameTaken()
	}
	exists, err = s.store.SystemSlugExists(ctx, slug, exceptID)
	if err != nil {
		return mapStoreError(err)
	}
	if exists {
		return apierr.SlugTaken()
	}
	return nil
}

// loadSystem loads by ID and enforces visibility=system. Any other visibility
// collapses to not_found so the admin surface cannot cross-enumerate.
func (s *Service) loadSystem(ctx context.Context, mcpID string) (*model.MCP, *apierr.Error) {
	m, err := s.store.GetByID(ctx, mcpID)
	if err != nil {
		return nil, mapStoreError(err)
	}
	if m.Visibility != model.VisibilitySystem {
		return nil, apierr.NotFound()
	}
	return m, nil
}

// buildSystemFromCreate is the admin twin of buildFromCreate: same
// validation and secret redaction, but skips the public visibility gate,
// stamps SpaceID="" so the row lands with space_id=NULL, and hard-codes
// Visibility=system regardless of the input.
func (s *Service) buildSystemFromCreate(caller Caller, req model.CreateRequest) (*model.MCP, *apierr.Error) {
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return nil, apierr.InvalidRequest("name is required",
			apierr.Detail{Field: "name", Reason: "required"})
	}
	if !model.ValidTransport(req.Transport) {
		return nil, apierr.InvalidTransport()
	}
	slug, apiErr := resolveSlug(req.Slug, name)
	if apiErr != nil {
		return nil, apiErr
	}
	if apiErr := validateContent(name, req); apiErr != nil {
		return nil, apiErr
	}
	env, headers, apiErr := redactConnectionSecrets(req.Env, req.Headers)
	if apiErr != nil {
		return nil, apiErr
	}
	now := s.now()
	m := &model.MCP{
		ID:            id.New(),
		Name:          name,
		Slug:          slug,
		Slogan:        req.Slogan,
		Category:      req.Category,
		Icon:          req.Icon,
		Tags:          normalizeTags(req.Tags),
		Tools:         req.Tools,
		UsageExamples: normalizeStringList(req.UsageExamples),
		FAQs:          normalizeFAQs(req.FAQs),
		Notes:         normalizeStringList(req.Notes),
		Visibility:    model.VisibilitySystem,
		OwnerUID:      caller.UID,
		SpaceID:       "", // NULL in DB — system rows are cross-Space.
		CreatorName:   caller.Name,
		Transport:     req.Transport,
		Connection: model.Connection{
			URL:        req.URL,
			Command:    req.Command,
			Args:       req.Args,
			Env:        env,
			Headers:    headers,
			AuthType:   normalizeAuthType(req.AuthType),
			ServerName: name,
		},
		CreatedAt: now,
		UpdatedAt: now,
	}
	return m, nil
}

// ─── End admin surface ─────────────────────────────────────────────────────

func (s *Service) list(ctx context.Context, caller Caller, p ListParams, mineOnly bool) (model.ListResponse, *apierr.Error) {
	filter := repository.ListFilter{
		CallerUID:  caller.UID,
		SpaceID:    caller.SpaceID,
		Keyword:    p.Keyword,
		Categories: p.Categories, Tags: p.Tags, Transports: p.Transports,
		Visibilities: p.Visibilities, Sources: p.Sources, VerificationStatuses: p.VerificationStatuses, Sort: p.Sort,
		Limit:    clampLimit(p.Limit),
		Offset:   clampOffset(p.Offset),
		MineOnly: mineOnly,
	}
	records, total, cats, err := s.store.List(ctx, filter)
	if err != nil {
		return model.ListResponse{}, mapStoreError(err)
	}
	items := make([]model.ListItem, 0, len(records))
	for i := range records {
		item := records[i].ToListItem()
		enrichListItem(&item, &records[i], p.Keyword, caller.UID)
		items = append(items, item)
	}
	if cats == nil {
		cats = []model.CategoryFilter{{Key: model.CategoryKeyAll, Count: total}}
	}
	return model.ListResponse{Items: items, Total: total, Categories: cats}, nil
}

func enrichListItem(item *model.ListItem, m *model.MCP, keyword, callerUID string) {
	if m.Visibility == model.VisibilitySystem {
		item.Source = "system"
	} else if m.OwnerUID == callerUID {
		item.Source = "mine"
	} else {
		item.Source = "space"
	}
	kw := strings.ToLower(strings.TrimSpace(keyword))
	if kw == "" {
		return
	}
	add := func(reason string, score int) {
		item.MatchReasons = append(item.MatchReasons, reason)
		item.Relevance += score
	}
	if strings.Contains(strings.ToLower(m.Name), kw) {
		add("name", 8)
	}
	if strings.Contains(strings.ToLower(m.Slogan), kw) {
		add("description", 2)
	}
	if strings.Contains(strings.ToLower(m.Category), kw) {
		add("category", 3)
	}
	for _, tag := range m.Tags {
		if strings.Contains(strings.ToLower(tag), kw) {
			add("tag:"+tag, 6)
			break
		}
	}
	for _, tool := range m.Tools {
		if strings.Contains(strings.ToLower(tool.Name), kw) || strings.Contains(strings.ToLower(tool.Description), kw) {
			add("tool:"+tool.Name, 7)
			break
		}
	}
	for _, example := range m.UsageExamples {
		if strings.Contains(strings.ToLower(example), kw) {
			add("usage_example", 1)
			break
		}
	}
	if strings.Contains(strings.ToLower(m.CreatorName), kw) {
		add("creator:"+m.CreatorName, 1)
	}
}

// loadVisible loads a record and applies the read visibility rule (doc §4.4):
//
//	system  OR  (space_id == caller_space AND (public OR owner == caller))
//
// Any record failing this rule is reported as not_found so cross-Space
// enumeration is closed.
func (s *Service) loadVisible(ctx context.Context, caller Caller, mcpID string) (*model.MCP, *apierr.Error) {
	m, err := s.store.GetByID(ctx, mcpID)
	if err != nil {
		return nil, mapStoreError(err)
	}
	if !isVisible(m, caller) {
		return nil, apierr.NotFound()
	}
	return m, nil
}

func isVisible(m *model.MCP, caller Caller) bool {
	if m.Visibility == model.VisibilitySystem {
		return true
	}
	if m.SpaceID != caller.SpaceID {
		return false
	}
	return m.Visibility == model.VisibilityPublic || m.OwnerUID == caller.UID
}

func detailForCaller(m *model.MCP, caller Caller) model.Detail {
	detail := m.ToDetail()
	if m.OwnerUID == caller.UID {
		return detail
	}
	detail.QuickStart.Env = blankMapValues(detail.QuickStart.Env)
	detail.QuickStart.Headers = blankMapValues(detail.QuickStart.Headers)
	return detail
}

func blankMapValues(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for key := range in {
		out[key] = ""
	}
	return out
}

// resolveSlug validates the incoming slug or auto-derives one from the
// display name. Rules (mcp-v1.md §3):
//   - non-empty client slug → must match ^[a-z0-9-]{1,64}$ verbatim (no
//     leniency; the client already runs the same slugify before submit)
//   - empty client slug → slugify(name); if that also yields ""
//     (e.g. all-CJK name) → slug_invalid so the client asks the user
//     for one instead of silently persisting an empty identifier
func resolveSlug(slugIn, name string) (string, *apierr.Error) {
	trimmed := strings.TrimSpace(slugIn)
	if trimmed != "" {
		if !slugRE.MatchString(trimmed) {
			return "", apierr.SlugInvalid("slug must match ^[a-z0-9-]{1,64}$",
				apierr.Detail{Field: "slug", Reason: "invalid_format"})
		}
		return trimmed, nil
	}
	derived := slugifyName(name)
	if derived == "" {
		return "", apierr.SlugInvalid("cannot derive slug from name; please provide one",
			apierr.Detail{Field: "slug", Reason: "required"})
	}
	return derived, nil
}

// buildFromCreate validates the flat request and produces a persist-ready
// domain record with identity stamped and secrets redacted.
func (s *Service) buildFromCreate(caller Caller, req model.CreateRequest) (*model.MCP, *apierr.Error) {
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return nil, apierr.InvalidRequest("name is required",
			apierr.Detail{Field: "name", Reason: "required"})
	}
	if !model.ValidTransport(req.Transport) {
		return nil, apierr.InvalidTransport()
	}
	visibility, apiErr := validateClientVisibility(req.Visibility)
	if apiErr != nil {
		return nil, apiErr
	}

	slug, apiErr := resolveSlug(req.Slug, name)
	if apiErr != nil {
		return nil, apiErr
	}

	if apiErr := validateContent(name, req); apiErr != nil {
		return nil, apiErr
	}

	env, headers, apiErr := redactConnectionSecrets(req.Env, req.Headers)
	if apiErr != nil {
		return nil, apiErr
	}

	now := s.now()
	m := &model.MCP{
		ID:            id.New(),
		Name:          name,
		Slug:          slug,
		Slogan:        req.Slogan,
		Category:      req.Category,
		Icon:          req.Icon,
		Tags:          normalizeTags(req.Tags),
		Tools:         req.Tools,
		UsageExamples: normalizeStringList(req.UsageExamples),
		FAQs:          normalizeFAQs(req.FAQs),
		Notes:         normalizeStringList(req.Notes),
		Visibility:    visibility,
		OwnerUID:      caller.UID,
		SpaceID:       caller.SpaceID,
		CreatorName:   caller.Name,
		Transport:     req.Transport,
		Connection: model.Connection{
			URL:        req.URL,
			Command:    req.Command,
			Args:       req.Args,
			Env:        env,
			Headers:    headers,
			AuthType:   normalizeAuthType(req.AuthType),
			ServerName: name,
		},
		CreatedAt: now,
		UpdatedAt: now,
	}
	return m, nil
}

// applyPatch mutates m in place from the partial request, re-running
// validation and redaction for any touched field. Immutable fields are absent
// from PatchRequest and therefore untouched.
func (s *Service) applyPatch(m *model.MCP, req model.PatchRequest) *apierr.Error {
	if req.Name != nil {
		name := strings.TrimSpace(*req.Name)
		if name == "" {
			return apierr.InvalidRequest("name must not be empty",
				apierr.Detail{Field: "name", Reason: "required"})
		}
		m.Name = name
		// serverName defaults to name (doc §3.3) and is not a separate input.
		m.Connection.ServerName = name
	}
	if req.Slug != nil {
		// Non-nil empty string is a client bug (they meant to omit): reject
		// so we never persist an empty identifier by accident.
		trimmed := strings.TrimSpace(*req.Slug)
		if trimmed == "" {
			return apierr.SlugInvalid("slug must not be empty",
				apierr.Detail{Field: "slug", Reason: "required"})
		}
		if !slugRE.MatchString(trimmed) {
			return apierr.SlugInvalid("slug must match ^[a-z0-9-]{1,64}$",
				apierr.Detail{Field: "slug", Reason: "invalid_format"})
		}
		m.Slug = trimmed
	}
	if req.Slogan != nil {
		m.Slogan = *req.Slogan
	}
	if req.Category != nil {
		m.Category = *req.Category
	}
	if req.Icon != nil {
		m.Icon = *req.Icon
	}
	if req.Tags != nil {
		m.Tags = normalizeTags(*req.Tags)
	}
	if req.Transport != nil {
		if !model.ValidTransport(*req.Transport) {
			return apierr.InvalidTransport()
		}
		m.Transport = *req.Transport
	}
	if req.URL != nil {
		m.Connection.URL = *req.URL
	}
	if req.Command != nil {
		m.Connection.Command = *req.Command
	}
	if req.Args != nil {
		m.Connection.Args = *req.Args
	}
	if req.Env != nil {
		redacted, leaks := redactSecrets(*req.Env, "env")
		if len(leaks) > 0 {
			return apierr.SecretLeaked(leaks...)
		}
		m.Connection.Env = redacted
	}
	if req.Headers != nil {
		redacted, leaks := redactSecrets(*req.Headers, "headers")
		if len(leaks) > 0 {
			return apierr.SecretLeaked(leaks...)
		}
		m.Connection.Headers = redacted
	}
	if req.AuthType != nil {
		m.Connection.AuthType = normalizeAuthType(*req.AuthType)
	}
	if req.Tools != nil {
		m.Tools = *req.Tools
	}
	if req.UsageExamples != nil {
		m.UsageExamples = normalizeStringList(*req.UsageExamples)
	}
	if req.FAQs != nil {
		m.FAQs = normalizeFAQs(*req.FAQs)
	}
	if req.Notes != nil {
		m.Notes = normalizeStringList(*req.Notes)
	}
	if req.Visibility != nil {
		visibility, apiErr := validateClientVisibility(*req.Visibility)
		if apiErr != nil {
			return apiErr
		}
		m.Visibility = visibility
	}
	// Length (bug #2) checks run over the fully merged record so every field —
	// freshly patched or carried over — stays within bounds.
	if apiErr := validateModelLengths(m); apiErr != nil {
		return apiErr
	}
	// Transport ↔ required-field (bug #3) is only enforced when the patch
	// touches the connection shape (transport / url / command). This keeps an
	// innocuous PATCH (e.g. a rename) from failing on a legacy record whose
	// stored connection predates the required-field rule, while still
	// validating any request that changes how the server is reached.
	if req.Transport != nil || req.URL != nil || req.Command != nil {
		if apiErr := validateTransportFields(m.Transport, m.Connection.URL, m.Connection.Command); apiErr != nil {
			return apiErr
		}
	}
	return nil
}

// validateModelLengths runs the length checks from validateLengths over a
// merged domain record (used by applyPatch).
func validateModelLengths(m *model.MCP) *apierr.Error {
	return validateLengths(m.Name, model.CreateRequest{
		Name:          m.Name,
		Slogan:        m.Slogan,
		URL:           m.Connection.URL,
		Command:       m.Connection.Command,
		Args:          m.Connection.Args,
		Headers:       m.Connection.Headers,
		Tools:         m.Tools,
		FAQs:          m.FAQs,
		Notes:         m.Notes,
		UsageExamples: m.UsageExamples,
		Transport:     m.Transport,
	})
}

// validateContent runs the authoritative server-side length checks (bug #2)
// and the transport ↔ required-field checks (bug #3) over an already
// trimmed name. name is passed separately because callers trim it before the
// required-name check; every other field is read straight off req. The first
// violation short-circuits with a 400 invalid_request carrying the offending
// field. args stay optional for every transport (product decision).
func validateContent(name string, req model.CreateRequest) *apierr.Error {
	if apiErr := validateLengths(name, req); apiErr != nil {
		return apiErr
	}
	return validateTransportFields(req.Transport, req.URL, req.Command)
}

// validateLengths enforces the per-field limits declared in model. Lengths are
// measured in Unicode code points (utf8.RuneCountInString) so a CJK slogan is
// counted by characters, matching the frontend maxLength contract rather than
// raw byte length.
func validateLengths(name string, req model.CreateRequest) *apierr.Error {
	if apiErr := tooLong("name", name, model.MaxNameLen); apiErr != nil {
		return apiErr
	}
	if apiErr := tooLong("slogan", req.Slogan, model.MaxSloganLen); apiErr != nil {
		return apiErr
	}
	if apiErr := tooLong("url", req.URL, model.MaxURLLen); apiErr != nil {
		return apiErr
	}
	if apiErr := tooLong("command", req.Command, model.MaxCommandLen); apiErr != nil {
		return apiErr
	}
	for i, a := range req.Args {
		if apiErr := tooLongAt("args", i, a, model.MaxArgLen); apiErr != nil {
			return apiErr
		}
	}
	if apiErr := validateHeaderLengths(req.Headers); apiErr != nil {
		return apiErr
	}
	for i, t := range req.Tools {
		if apiErr := tooLongAt("tools.name", i, t.Name, model.MaxToolNameLen); apiErr != nil {
			return apiErr
		}
		if apiErr := tooLongAt("tools.description", i, t.Description, model.MaxTextLen); apiErr != nil {
			return apiErr
		}
	}
	for i, f := range req.FAQs {
		if apiErr := tooLongAt("faqs.question", i, f.Question, model.MaxTextLen); apiErr != nil {
			return apiErr
		}
		if apiErr := tooLongAt("faqs.answer", i, f.Answer, model.MaxTextLen); apiErr != nil {
			return apiErr
		}
	}
	for i, n := range req.Notes {
		if apiErr := tooLongAt("notes", i, n, model.MaxTextLen); apiErr != nil {
			return apiErr
		}
	}
	for i, u := range req.UsageExamples {
		if apiErr := tooLongAt("usageExamples", i, u, model.MaxTextLen); apiErr != nil {
			return apiErr
		}
	}
	return nil
}

// validateHeaderLengths bounds each header key and value. Iteration order over
// a map is unspecified, but every entry is checked, so the reported field is
// deterministic for a given offending entry.
func validateHeaderLengths(headers map[string]string) *apierr.Error {
	for k, v := range headers {
		if utf8.RuneCountInString(k) > model.MaxHeaderKeyLen {
			return apierr.InvalidRequest(
				fmt.Sprintf("header key must be at most %d characters", model.MaxHeaderKeyLen),
				apierr.Detail{Field: "headers." + k, Reason: "too_long"})
		}
		if utf8.RuneCountInString(v) > model.MaxHeaderValueLen {
			return apierr.InvalidRequest(
				fmt.Sprintf("header value must be at most %d characters", model.MaxHeaderValueLen),
				apierr.Detail{Field: "headers." + k, Reason: "too_long"})
		}
	}
	return nil
}

// validateTransportFields enforces the接入方式 required-field rule (bug #3):
// stdio needs a command; streamable-http / sse need a url. args are optional
// for all transports. An unknown transport is caught earlier by ValidTransport.
func validateTransportFields(transport model.Transport, url, command string) *apierr.Error {
	switch transport {
	case model.TransportStdio:
		if strings.TrimSpace(command) == "" {
			return apierr.InvalidRequest("command is required for stdio transport",
				apierr.Detail{Field: "command", Reason: "required"})
		}
	case model.TransportStreamableHTTP, model.TransportSSE:
		if strings.TrimSpace(url) == "" {
			return apierr.InvalidRequest("url is required for this transport",
				apierr.Detail{Field: "url", Reason: "required"})
		}
	}
	return nil
}

func tooLong(field, value string, max int) *apierr.Error {
	if utf8.RuneCountInString(value) > max {
		return apierr.InvalidRequest(
			fmt.Sprintf("%s must be at most %d characters", field, max),
			apierr.Detail{Field: field, Reason: "too_long"})
	}
	return nil
}

func tooLongAt(field string, index int, value string, max int) *apierr.Error {
	if utf8.RuneCountInString(value) > max {
		return apierr.InvalidRequest(
			fmt.Sprintf("%s must be at most %d characters", field, max),
			apierr.Detail{Field: fmt.Sprintf("%s[%d]", field, index), Reason: "too_long"})
	}
	return nil
}

// redactConnectionSecrets redacts both maps and combines any leaks into a
// single secret_leaked error (doc §5.1).
func redactConnectionSecrets(env, headers map[string]string) (map[string]string, map[string]string, *apierr.Error) {
	redactedEnv, envLeaks := redactSecrets(env, "env")
	redactedHeaders, headerLeaks := redactSecrets(headers, "headers")
	leaks := append(envLeaks, headerLeaks...)
	if len(leaks) > 0 {
		return nil, nil, apierr.SecretLeaked(leaks...)
	}
	return redactedEnv, redactedHeaders, nil
}

// validateClientVisibility accepts only public/private from a client write;
// system (or anything else) is rejected (doc §4.1). An empty value defaults to
// private, matching the schema default and the private-by-omission posture.
func validateClientVisibility(v model.Visibility) (model.Visibility, *apierr.Error) {
	switch v {
	case "":
		return model.VisibilityPrivate, nil
	case model.VisibilityPublic, model.VisibilityPrivate:
		return v, nil
	default:
		return "", apierr.InvalidVisibility()
	}
}

// normalizeAuthType defaults empty/absent to "none" (doc §5.2).
func normalizeAuthType(v string) string {
	if strings.TrimSpace(v) == "" {
		return "none"
	}
	return v
}

func clampLimit(limit int) int {
	if limit <= 0 {
		return defaultLimit
	}
	if limit > maxLimit {
		return maxLimit
	}
	return limit
}

func clampOffset(offset int) int {
	if offset < 0 {
		return 0
	}
	return offset
}

// mapStoreError translates repository sentinels to wire errors; anything else
// is an internal 500 (its cause is logged by the handler, not surfaced).
func mapStoreError(err error) *apierr.Error {
	switch {
	case errors.Is(err, repository.ErrNotFound):
		return apierr.NotFound()
	case errors.Is(err, repository.ErrNameTaken):
		return apierr.NameTaken()
	case errors.Is(err, repository.ErrSlugTaken):
		return apierr.SlugTaken()
	default:
		return apierr.Internal()
	}
}
