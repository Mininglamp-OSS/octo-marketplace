package errcode

// Standard error codes for the marketplace API.
const (
	BadRequest          = "VALIDATION_ERROR"
	Unauthorized        = "AUTH_REQUIRED"
	NotFound            = "NOT_FOUND"
	PermissionDenied    = "FORBIDDEN"
	FileTooLarge        = "PAYLOAD_TOO_LARGE"
	InvalidZip          = "VALIDATION_ERROR"
	SkillMDNotFound     = "VALIDATION_ERROR"
	CategoryInUse       = "CONFLICT"
	RateLimited         = "RATE_LIMITED"
	InternalError       = "INTERNAL_ERROR"
	UpstreamUnavailable = "UPSTREAM_UNAVAILABLE"
	Conflict            = "CONFLICT"
	// Duplicate is the wire code for a name/slug collision with an existing live
	// record (409). Matches apierr.CodeNameTaken and the mcp handler; kept
	// distinct from the generic Conflict so a client switching on the advertised
	// "DUPLICATE" (docs/api/expert-v1.md §2) matches a duplicate-name response.
	Duplicate = "DUPLICATE"

	// Metrics error codes.
	MetricsUnsupportedEvent    = BadRequest
	MetricsUnsupportedResource = BadRequest
	MetricsResourceNotVisible  = NotFound
)
