// Package handler renders the MCP catalog HTTP surface. It parses requests and
// writes responses only; all business rules live in internal/service and all
// SQL in internal/repository. The router is the sole place that maps URLs to
// these methods (AGENTS.md), so this file exposes handler methods, not routes.
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

	"github.com/Mininglamp-OSS/octo-marketplace/internal/apierr"
	marketmiddleware "github.com/Mininglamp-OSS/octo-marketplace/internal/middleware"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/service"
)

// maxBodyBytes caps request bodies. Icons may be data URLs (MEDIUMTEXT in the
// schema), so the limit is generous but bounded to reject decompression-bomb
// style oversized payloads at the ingestion boundary (AGENTS.md).
const maxBodyBytes = 8 << 20 // 8 MiB

// MCPService is the subset of the service the handlers depend on.
type MCPService interface {
	Create(ctx context.Context, caller service.Caller, req model.CreateRequest) (model.Detail, *apierr.Error)
	Get(ctx context.Context, caller service.Caller, id string) (model.Detail, *apierr.Error)
	Patch(ctx context.Context, caller service.Caller, id string, req model.PatchRequest) (model.Detail, *apierr.Error)
	Delete(ctx context.Context, caller service.Caller, id string) *apierr.Error
	List(ctx context.Context, caller service.Caller, p service.ListParams) (model.ListResponse, *apierr.Error)
	ListMine(ctx context.Context, caller service.Caller, p service.ListParams) (model.ListResponse, *apierr.Error)
	Probe(ctx context.Context, req service.ProbeRequest) (service.ProbeResponse, *apierr.Error)
	UploadIcon(ctx context.Context, caller service.Caller, id string, data []byte, contentType string) (service.IconResult, *apierr.Error)
}

// MCP wires the service to HTTP.
type MCP struct {
	svc MCPService
}

// NewMCP returns an MCP handler.
func NewMCP(svc MCPService) *MCP {
	return &MCP{svc: svc}
}

// Create handles POST /mcps.
func (h *MCP) Create(w http.ResponseWriter, r *http.Request) {
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
	detail, apiErr := h.svc.Create(r.Context(), caller, req)
	if apiErr != nil {
		writeError(w, apiErr)
		return
	}
	writeJSON(w, http.StatusCreated, detail)
}

// List handles GET /mcps.
func (h *MCP) List(w http.ResponseWriter, r *http.Request) {
	caller, ok := callerFromContext(r)
	if !ok {
		writeError(w, apierr.Unauthorized())
		return
	}
	resp, apiErr := h.svc.List(r.Context(), caller, listParams(r))
	if apiErr != nil {
		writeError(w, apiErr)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// ListMine handles GET /mcps/mine.
func (h *MCP) ListMine(w http.ResponseWriter, r *http.Request) {
	caller, ok := callerFromContext(r)
	if !ok {
		writeError(w, apierr.Unauthorized())
		return
	}
	resp, apiErr := h.svc.ListMine(r.Context(), caller, listParams(r))
	if apiErr != nil {
		writeError(w, apiErr)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// Get handles GET /mcps/{id}.
func (h *MCP) Get(w http.ResponseWriter, r *http.Request) {
	caller, ok := callerFromContext(r)
	if !ok {
		writeError(w, apierr.Unauthorized())
		return
	}
	detail, apiErr := h.svc.Get(r.Context(), caller, r.PathValue("id"))
	if apiErr != nil {
		writeError(w, apiErr)
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

// Patch handles PATCH /mcps/{id}.
func (h *MCP) Patch(w http.ResponseWriter, r *http.Request) {
	caller, ok := callerFromContext(r)
	if !ok {
		writeError(w, apierr.Unauthorized())
		return
	}
	var req model.PatchRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, err)
		return
	}
	detail, apiErr := h.svc.Patch(r.Context(), caller, r.PathValue("id"), req)
	if apiErr != nil {
		writeError(w, apiErr)
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

// Delete handles DELETE /mcps/{id}.
func (h *MCP) Delete(w http.ResponseWriter, r *http.Request) {
	caller, ok := callerFromContext(r)
	if !ok {
		writeError(w, apierr.Unauthorized())
		return
	}
	if apiErr := h.svc.Delete(r.Context(), caller, r.PathValue("id")); apiErr != nil {
		writeError(w, apiErr)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// maxIconBytes caps the multipart icon upload before the file is read into
// memory. The service enforces the same limit on the decoded bytes; this is
// the ingestion-boundary guard (AGENTS.md) so a huge multipart body is
// rejected before allocation.
const maxIconBytes = 2 << 20 // 2 MiB

// UploadIcon handles POST /mcps/{id}/icon — a multipart form with a single
// `file` part. It stores the image in object storage and returns { url }
// (the frontend-agreed contract, LSC-80 #1). Owner-only; enforced by the
// service.
func (h *MCP) UploadIcon(w http.ResponseWriter, r *http.Request) {
	caller, ok := callerFromContext(r)
	if !ok {
		writeError(w, apierr.Unauthorized())
		return
	}
	// Bound the whole request body first so an oversized multipart payload is
	// rejected before we parse it.
	r.Body = http.MaxBytesReader(w, r.Body, maxIconBytes+bytesReaderSlack)
	if err := r.ParseMultipartForm(maxIconBytes); err != nil {
		writeError(w, apierr.InvalidRequest("invalid multipart form or file too large"))
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, apierr.InvalidRequest("missing file part",
			apierr.Detail{Field: "file", Reason: "required"}))
		return
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, maxIconBytes+1))
	if err != nil {
		writeError(w, apierr.InvalidRequest("could not read uploaded file"))
		return
	}

	contentType := ""
	if header != nil {
		contentType = header.Header.Get("Content-Type")
	}

	result, apiErr := h.svc.UploadIcon(r.Context(), caller, r.PathValue("id"), data, contentType)
	if apiErr != nil {
		writeError(w, apiErr)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"url": result.URL, "version": result.Version})
}

// bytesReaderSlack leaves room for the multipart envelope (boundaries,
// headers) on top of the raw file cap so a legitimate max-size file is not
// rejected by the body limit.
const bytesReaderSlack = 1 << 20 // 1 MiB

// Probe handles POST /mcps/probe — runs an MCP initialize + tools/list
// the tool list (docs/api/mcp-v1.md §4.7). Auth is required to keep the
// endpoint from acting as an open HTTP proxy; the caller's identity is not
// otherwise used, so an in-body probe failure still returns HTTP 200 with
// {ok:false, error:{code,message}}.
func (h *MCP) Probe(w http.ResponseWriter, r *http.Request) {
	if _, ok := callerFromContext(r); !ok {
		writeError(w, apierr.Unauthorized())
		return
	}
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

// callerFromContext lifts the identity + Space stamped by the authenticator
// (doc §1). Absence means the auth middleware did not run — treated as
// unauthorized rather than trusting the request.
func callerFromContext(r *http.Request) (service.Caller, bool) {
	identity, ok := marketmiddleware.IdentityFromContext(r.Context())
	if !ok || identity.UID == "" {
		return service.Caller{}, false
	}
	return service.Caller{
		UID:     identity.UID,
		Name:    identity.Name,
		SpaceID: marketmiddleware.SpaceIDFromContext(r.Context()),
	}, true
}

func listParams(r *http.Request) service.ListParams {
	q := r.URL.Query()
	return service.ListParams{
		Keyword:  strings.TrimSpace(q.Get("keyword")),
		Category: strings.TrimSpace(q.Get("category")),
		Limit:    atoiDefault(q.Get("limit"), 0),
		Offset:   atoiDefault(q.Get("offset"), 0),
	}
}

func atoiDefault(s string, fallback int) int {
	if s == "" {
		return fallback
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return fallback
	}
	return n
}

// decodeJSON reads a bounded, strict JSON body. A malformed body is a client
// error (invalid_request), not a 500.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) *apierr.Error {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	dec := json.NewDecoder(r.Body)
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

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		log.Printf("[handler] encode response: %v", err)
	}
}

// writeError renders the doc §2 envelope: {"err":{code,message,details?}}.
func writeError(w http.ResponseWriter, e *apierr.Error) {
	if e.Status >= http.StatusInternalServerError {
		log.Printf("[handler] %s", e.Error())
	}
	writeJSON(w, e.Status, map[string]any{"err": e})
}
