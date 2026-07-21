package errcode

// Standard error codes for the marketplace API.
const (
	BadRequest       = "VALIDATION_ERROR"
	Unauthorized     = "AUTH_REQUIRED"
	NotFound         = "NOT_FOUND"
	PermissionDenied = "FORBIDDEN"
	FileTooLarge     = "PAYLOAD_TOO_LARGE"
	InvalidZip       = "VALIDATION_ERROR"
	SkillMDNotFound  = "VALIDATION_ERROR"
	CategoryInUse    = "CONFLICT"
	RateLimited      = "RATE_LIMITED"
	InternalError    = "INTERNAL_ERROR"
	Conflict         = "CONFLICT"

	// Metrics error codes.
	MetricsUnsupportedEvent    = BadRequest
	MetricsUnsupportedResource = BadRequest
	MetricsResourceNotVisible  = NotFound
)
