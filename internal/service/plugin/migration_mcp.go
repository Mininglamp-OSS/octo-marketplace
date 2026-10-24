package plugin

// ValidateMigrationMCP reuses the installation contract before extracting an
// existing expert's MCP attachment. It validates only, never clears credentials,
// persists configuration, or starts a process. The migration separately refuses
// concrete secret-bearing env/header values until an operator resolves them.
func ValidateMigrationMCP(raw string) error {
	_, err := normalizeCapabilityMCP(raw)
	return err
}
