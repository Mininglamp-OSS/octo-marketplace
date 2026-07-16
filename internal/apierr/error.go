// Package apierr defines the marketplace HTTP error envelope and the stable
// err.marketplace.* codes from docs/api/mcp-v1.md §2. Handlers translate a
// *Error into the wire JSON; the service and repository layers construct them.
//
// The envelope is:
//
//	{ "err": { "code": "err.marketplace.mcp.not_found", "message": "...",
//	           "details": [ { "field": "env.GITHUB_TOKEN", "reason": "non_empty" } ] } }
//
// message never carries internal paths, credentials, SQL, or Go error strings.
package apierr

import "net/http"

// Stable wire codes. Always the full err.marketplace.* form (doc §2).
const (
	CodeInvalidRequest    = "err.marketplace.mcp.invalid_request"
	CodeInvalidVisibility = "err.marketplace.mcp.invalid_visibility"
	CodeInvalidTransport  = "err.marketplace.mcp.invalid_transport"
	CodeSecretLeaked      = "err.marketplace.mcp.secret_leaked"
	CodeNotFound          = "err.marketplace.mcp.not_found"
	CodeForbidden         = "err.marketplace.mcp.forbidden"
	CodeNameTaken         = "err.marketplace.mcp.name_taken"
	CodeSlugTaken         = "err.marketplace.mcp.slug_taken"
	CodeSlugInvalid       = "err.marketplace.mcp.slug_invalid"
	CodeProbeUnsupported  = "err.marketplace.mcp.probe_unsupported"

	CodeUnauthorized   = "err.marketplace.auth.unauthorized"
	CodeForbiddenSpace = "err.marketplace.auth.forbidden_space"

	CodeInternal = "err.marketplace.internal"
)

// Detail describes a single offending field for validation errors (doc §2:
// details always live inside err, never at the top level).
type Detail struct {
	Field  string `json:"field"`
	Reason string `json:"reason"`
}

// Error is a structured, client-facing marketplace error. It implements the
// error interface so it can flow up through the service and repository layers.
type Error struct {
	Status  int      `json:"-"`
	Code    string   `json:"code"`
	Message string   `json:"message"`
	Details []Detail `json:"details,omitempty"`
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

// New builds an Error with the given HTTP status, wire code, and message.
func New(status int, code, message string) *Error {
	return &Error{Status: status, Code: code, Message: message}
}

// WithDetails attaches validation details and returns the same Error for
// fluent construction.
func (e *Error) WithDetails(details ...Detail) *Error {
	e.Details = append(e.Details, details...)
	return e
}

// Constructors for the common cases so call sites stay terse and consistent.

func InvalidRequest(message string, details ...Detail) *Error {
	if message == "" {
		message = "Request body failed validation"
	}
	return (&Error{Status: http.StatusBadRequest, Code: CodeInvalidRequest, Message: message}).WithDetails(details...)
}

func InvalidVisibility() *Error {
	return New(http.StatusBadRequest, CodeInvalidVisibility, "Visibility must be public or private")
}

func InvalidTransport() *Error {
	return New(http.StatusBadRequest, CodeInvalidTransport, "Transport must be one of stdio, streamable-http, sse")
}

func SecretLeaked(details ...Detail) *Error {
	return (&Error{Status: http.StatusBadRequest, Code: CodeSecretLeaked, Message: "Secret value must not be submitted"}).WithDetails(details...)
}

func Unauthorized() *Error {
	return New(http.StatusUnauthorized, CodeUnauthorized, "Missing or invalid Octo token")
}

func ForbiddenSpace() *Error {
	return New(http.StatusForbidden, CodeForbiddenSpace, "Missing X-Space-Id or Space membership denied")
}

func Forbidden() *Error {
	return New(http.StatusForbidden, CodeForbidden, "Only the owner may modify this MCP")
}

func NotFound() *Error {
	return New(http.StatusNotFound, CodeNotFound, "MCP not found")
}

func NameTaken() *Error {
	return New(http.StatusConflict, CodeNameTaken, "An MCP with this name already exists")
}

// SlugTaken signals that (space_id, slug) collides with another live row.
// Per doc §3, slug is unique per Space among live records.
func SlugTaken() *Error {
	return New(http.StatusConflict, CodeSlugTaken, "An MCP with this slug already exists in this Space")
}

// SlugInvalid signals that a supplied slug (or one auto-derived from the
// name) fails ^[a-z0-9-]{1,64}$ or reduces to the empty string. Fill Details
// with the offending field so the client can pinpoint it.
func SlugInvalid(message string, details ...Detail) *Error {
	if message == "" {
		message = "Slug must match ^[a-z0-9-]{1,64}$"
	}
	return (&Error{Status: http.StatusBadRequest, Code: CodeSlugInvalid, Message: message}).WithDetails(details...)
}

// ProbeUnsupported signals that the probe endpoint refuses to handle the
// requested transport at the network boundary — currently only stdio (see
// docs/api/mcp-v1.md §4.7). The marketplace host must not spawn arbitrary
// user commands, so stdio probing is the desktop client's job.
func ProbeUnsupported(message string) *Error {
	if message == "" {
		message = "This probe transport is not supported by the marketplace server"
	}
	return New(http.StatusBadRequest, CodeProbeUnsupported, message)
}

func Internal() *Error {
	return New(http.StatusInternalServerError, CodeInternal, "Internal server error")
}
