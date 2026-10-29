package plugin

import "github.com/Mininglamp-OSS/octo-marketplace/internal/model"

// ValidateMigrationMCP reuses the installation contract before extracting an
// existing expert's MCP attachment. It validates only, never clears credentials,
// persists configuration, or starts a process. Migration preserves existing
// endpoint, transport and authentication settings without test replacements.
func ValidateMigrationMCP(raw string) error {
	// A duplicate key can hide an earlier credential from last-wins parsers,
	// while the untouched raw attachment would still persist that credential.
	if model.ValidateUniqueJSONFields([]byte(raw)) != nil {
		return ErrInvalidRequest
	}
	_, _, err := normalizeCapabilityConnectorMCP(raw)
	return err
}
