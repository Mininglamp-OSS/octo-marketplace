# Block legacy Skill publication under managed versions

Goal: when PLUGIN_VERSION_MANAGEMENT_ENABLED is enabled, old CLI Skill writes must not bypass the managed Plugin publication lifecycle, and Skill/Expert/Expert Team formal publication must explicitly name a valid version.

Load-bearing: wire the flag into the legacy Skill service; reject Bot synchronous publish before parse/task work, and reject legacy Skill create/update (including /skill aliases) before database/storage writes. Preserve authentication and Bot identity gates. Return 400 VALIDATION_ERROR with details.reason=versioned_api_required and an explicit octo-cli update/Plugin API hint, matching the existing Expert/Team contract. Extend the same explicit-version requirement and private draft-label matching to managed Skill publication; draft saves/imports continue accepting omitted versions.

Out of scope: CLI changes or automatic updates, migrations, admin operations, read/delete/upload/parse behavior, Connector publication policy and deployment settings. Feature-off behavior stays compatible.

Acceptance: managed publication rejects missing/blank/malformed versions for all three versioned types before side effects; explicit versions continue to work and private mismatches fail; draft-save defaults stay compatible. Enabled legacy routes reject before any side effects; unauthenticated/non-Bot callers retain their authentication errors; flag-off legacy writes still execute their original validation; generated OpenAPI and compatibility docs agree with implementation. Focused tests, go test ./..., openapi-check and openapi-diff pass.
