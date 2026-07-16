// Admin surface for platform-provided (visibility=system) MCP records.
// Reached via /admin/api/v1/*, guarded by the AdminAuthenticator middleware
// (internal/middleware/admin.go). Public /market/api/v1/* handlers live in
// mcp.go; the two share the request/response types (CreateRequest, Detail,
// ListResponse) and the wire error envelope.

package handler

import (
	"context"
	"net/http"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/apierr"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/service"
)

// AdminMCPService is the subset of the service the admin handlers depend on.
// Kept as an interface so tests can inject fakes without spinning up a repo.
type AdminMCPService interface {
	CreateSystem(ctx context.Context, caller service.Caller, req model.CreateRequest) (model.Detail, *apierr.Error)
	ListSystem(ctx context.Context, p service.ListParams) (model.ListResponse, *apierr.Error)
	GetSystem(ctx context.Context, id string) (model.Detail, *apierr.Error)
	UpdateSystem(ctx context.Context, id string, req model.PatchRequest) (model.Detail, *apierr.Error)
	DeleteSystem(ctx context.Context, id string) *apierr.Error
	Probe(ctx context.Context, req service.ProbeRequest) (service.ProbeResponse, *apierr.Error)
}

// AdminMCP wires the admin subset of the MCP service to HTTP.
type AdminMCP struct {
	svc AdminMCPService
}

// NewAdminMCP returns an admin handler.
func NewAdminMCP(svc AdminMCPService) *AdminMCP {
	return &AdminMCP{svc: svc}
}

// Create handles POST /admin/api/v1/mcps.
//
// The body is a normal CreateRequest (same shape as the public endpoint) but
// any `visibility` field is ignored — the service always stamps
// visibility=system on this path. The caller (an admin identity from the
// middleware) becomes owner_uid and creator_name; space_id is NULL.
func (h *AdminMCP) Create(w http.ResponseWriter, r *http.Request) {
	caller, ok := callerFromContext(r)
	if !ok {
		writeError(w, apierr.Unauthorized())
		return
	}
	var req model.CreateRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, err)
		return
	}
	detail, apiErr := h.svc.CreateSystem(r.Context(), caller, req)
	if apiErr != nil {
		writeError(w, apiErr)
		return
	}
	writeJSON(w, http.StatusCreated, detail)
}

// List handles GET /admin/api/v1/mcps. Lists every visibility=system record
// regardless of Space, paginated the same way the public list is.
func (h *AdminMCP) List(w http.ResponseWriter, r *http.Request) {
	resp, apiErr := h.svc.ListSystem(r.Context(), listParams(r))
	if apiErr != nil {
		writeError(w, apiErr)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// Get handles GET /admin/api/v1/mcps/{id}. Returns the full detail of a
// visibility=system record; anything else is 404 (loadSystem enforcement).
func (h *AdminMCP) Get(w http.ResponseWriter, r *http.Request) {
	detail, apiErr := h.svc.GetSystem(r.Context(), r.PathValue("id"))
	if apiErr != nil {
		writeError(w, apiErr)
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

// Patch handles PATCH /admin/api/v1/mcps/{id}. Same partial-update shape as
// the public PATCH (doc §4.5) but skips ownership — any admin can edit any
// system MCP. Visibility is pinned to system by the service (see
// service.UpdateSystem); a body that tries to demote to public/private is
// rejected 400.
func (h *AdminMCP) Patch(w http.ResponseWriter, r *http.Request) {
	var req model.PatchRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, err)
		return
	}
	detail, apiErr := h.svc.UpdateSystem(r.Context(), r.PathValue("id"), req)
	if apiErr != nil {
		writeError(w, apiErr)
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

// Delete handles DELETE /admin/api/v1/mcps/{id}. Soft delete; response is
// 204 to match the public DELETE semantics (doc §4.6).
func (h *AdminMCP) Delete(w http.ResponseWriter, r *http.Request) {
	if apiErr := h.svc.DeleteSystem(r.Context(), r.PathValue("id")); apiErr != nil {
		writeError(w, apiErr)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Probe handles POST /admin/api/v1/mcps/probe — the admin-scoped mirror of
// the public /mcps/probe endpoint (see mcp.go). Reuses the same service
// method; the only difference is the auth path (AdminAuthenticator vs the
// user Authenticator). No caller-identity requirement here: the admin token
// already gates access, and probe never persists anything caller-scoped.
func (h *AdminMCP) Probe(w http.ResponseWriter, r *http.Request) {
	var req service.ProbeRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, err)
		return
	}
	resp, apiErr := h.svc.Probe(r.Context(), req)
	if apiErr != nil {
		writeError(w, apiErr)
		return
	}
	if resp.Tools == nil {
		resp.Tools = []model.Tool{}
	}
	writeJSON(w, http.StatusOK, resp)
}
