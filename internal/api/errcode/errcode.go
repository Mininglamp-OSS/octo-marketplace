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
	InternalError    = "INTERNAL_ERROR"
	Conflict         = "CONFLICT"

	// Metrics error codes.
	MetricsUnsupportedEvent    = "METRICS_UNSUPPORTED_EVENT"
	MetricsUnsupportedResource = "METRICS_UNSUPPORTED_RESOURCE"
	MetricsResourceNotVisible  = "METRICS_RESOURCE_NOT_VISIBLE"
)
