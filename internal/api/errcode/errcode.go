package errcode

// Standard error codes for the marketplace API.
const (
	BadRequest       = "err.marketplace.bad_request"
	Unauthorized     = "err.marketplace.unauthorized"
	NotFound         = "err.marketplace.not_found"
	PermissionDenied = "err.marketplace.permission_denied"
	FileTooLarge     = "err.marketplace.file_too_large"
	InvalidZip       = "err.marketplace.invalid_zip"
	SkillMDNotFound  = "err.marketplace.skill_md_not_found"
	CategoryInUse    = "err.marketplace.category_in_use"
	InternalError    = "err.marketplace.internal_error"
	Conflict         = "err.marketplace.conflict"
)
