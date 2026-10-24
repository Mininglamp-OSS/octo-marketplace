# Pinned capability installation

## Goal
Resolve published roots, pinned released expert/skill versions and current connectors before any Fleet mutation, without changing the Fleet protocol.

## Background
Refs #119 and #120. Builds on version management and versioned references. Feature defaults off; local fixtures only.

## Load-bearing list
- Current caller scope and source audience remain authoritative; historical versions do not grant access.
- Missing pins/releases/artifacts fail closed, with no fallback to latest or inline expert MCP.
- Batched per-request reads, exact expert-to-skill bindings, stable skill aliases, bounded payloads.
- Delisted dependencies remain usable through existing released pins; roots must remain listed.
- No token, environment value or complete request persists in retry records.

## Out of scope
Production enablement, real tenant data, migration, Fleet protocol changes, executing skills or MCP processes.

## Acceptance
Exact version and permission tests; same-name skills keep distinct identities and bindings; connector updates affect new installs; malformed dependencies fail before Fleet; legacy/new entries share content. Real Fleet/Daemon evidence is still required for I4 and cannot be replaced by mocks.

## COMPREHENSION
The release table proves publication; an arbitrary snapshot or a current pointer
cannot. Installation reads intentionally differ from market discovery because
existing pins may survive delisting. Fleet is still the workspace authorization
and idempotency authority, so even a completed local attempt forwards identical
retries rather than bypassing Fleet. Legacy cleanup must track newly created
skill IDs separately from all skill IDs bound to each member.

## Local verification (2026-10-24)
- Focused plugin/expert/repository/config/handler package tests passed.
- `TEST_MYSQL_DSN=<isolated synthetic MySQL> go test ./...` passed, including
  real SQL batch access, released delisted pins, concurrent scoped attempt
  claims/completion conflicts and frozen-root retries after a later release.
- `go build ./...` passed with explicit checkout Git metadata.
- `make openapi-check` passed (10 existing warnings, no errors); regenerated
  output unchanged. `make openapi-diff` found no breaking changes.
- I4 real Fleet/Daemon execution remains pending; no remote code was changed,
  no feature enabled, no production/real tenant data accessed.
