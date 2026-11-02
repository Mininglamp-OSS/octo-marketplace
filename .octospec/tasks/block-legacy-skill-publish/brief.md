# Block legacy Skill publication under managed versions

Goal: when PLUGIN_VERSION_MANAGEMENT_ENABLED is enabled, old CLI Skill writes must not bypass the managed Plugin publication lifecycle.

Load-bearing: wire the flag into the legacy Skill service; reject Bot synchronous publish before parse/task work, and reject legacy Skill create/update (including /skill aliases) before database/storage writes. Preserve authentication and Bot identity gates. Return 400 VALIDATION_ERROR with details.reason=versioned_api_required and an explicit octo-cli update/Plugin API hint, matching the existing Expert/Team contract.

Out of scope: CLI changes or automatic updates, migrations, admin operations, read/delete/upload/parse behavior, new Plugin publication policy and deployment settings. Feature-off behavior stays compatible.

Acceptance: enabled routes reject before any side effects; unauthenticated/non-Bot callers retain their authentication errors; flag-off legacy writes still execute their original validation; generated OpenAPI and compatibility docs agree with implementation. Focused tests, go test ./..., openapi-check and openapi-diff pass.
