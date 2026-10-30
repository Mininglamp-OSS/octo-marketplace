# Require explicit Expert/Team release versions

Goal: old CLI requests must not silently publish Expert/Expert Team releases without an explicit version.

Load-bearing: when PLUGIN_VERSION_MANAGEMENT_ENABLED is enabled, tenant plugin publish requires a valid explicit version for expert/expert_team; owner and Space checks precede resource-specific validation. Draft create/save retains existing compatibility. Legacy Expert/Squad create/patch writes cannot bypass the versioned publication lifecycle under the same flag; reads/install/delete stay unchanged. Private managed publication must not silently ignore a caller version different from the saved draft.

Out of scope: automatic version generation, CLI implementation, Skill/Connector publication policy, data migration and unrelated lifecycle changes.

Acceptance: missing/invalid versions fail before publication/review side effects with actionable structured errors; explicit valid versions work; feature-off behavior and draft writes remain compatible; legacy write guards are wired by router configuration and preserve authentication. Unit/handler tests and generated OpenAPI stay consistent.
