package plugin

import "github.com/Mininglamp-OSS/octo-marketplace/internal/model"

// ValidateMigrationMCP reuses the installation contract before extracting an
// existing expert's MCP attachment. It validates only, never clears credentials,
// persists configuration, or starts a process. The migration separately refuses
// concrete secret-bearing env/header values until an operator resolves them.
func ValidateMigrationMCP(raw string) error {
	// A duplicate key can hide an earlier credential from last-wins parsers,
	// while the untouched raw attachment would still persist that credential.
	if model.ValidateUniqueJSONFields([]byte(raw)) != nil {
		return ErrInvalidRequest
	}
	_, err := normalizeCapabilityMCP(raw)
	return err
}
