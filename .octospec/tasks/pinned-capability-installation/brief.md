# Pinned capability installation

## Goal
Resolve published roots, pinned released expert/skill versions and current connectors before any Fleet mutation, without changing the Fleet protocol.

## Background
Refs #119 and #120. Builds on version management and versioned references. Feature defaults off; local fixtures only.

## Load-bearing list
- Current caller scope and source audience remain authoritative; historical versions do not grant access.
- Missing pins/releases/artifacts fail closed, with no fallback to latest or inline expert MCP.
- Both entrypoints reject inline root/member capability snapshots before remote
  mutation; missing/hidden roots keep the existing 404 contract.
- Batched per-request reads, exact expert-to-skill bindings, original skill names and bytes, bounded payloads.
- Retain historical pins/releases, but both roots and dependencies must currently
  be published and authorized before first dispatch or same-key retry. No
  delisting exemption, request-time conversion or automatic re-listing.
- Persist a secret-free frozen definition and HMAC fingerprints for retry; never
  store tokens or custom runtime environment values.
- Activation requires completed M1/M3 evidence and the read-only released-graph
  check; expired-key reconciliation preserves durable key tombstones.

## Out of scope
Production enablement, real tenant data, migration, Fleet protocol changes, executing skills or MCP processes.

## Acceptance
Exact version and permission tests; same-name skills keep distinct identities and bindings; connector updates affect new installs; malformed dependencies fail before Fleet; legacy/new entries share content. Real Fleet/Daemon evidence is still required for I4 and cannot be replaced by mocks.

## COMPREHENSION
The release table proves publication; an arbitrary snapshot or a current pointer
cannot. The scoped SQL reader may return retained delisted rows/evidence, but
that is not distribution permission: the service checks current listing/status,
caller access and each source audience before dispatch. A delisted node blocks
an otherwise frozen plan without erasing its history or earlier uncertainty.
Fleet remains the workspace authorization and idempotency authority, so eligible
completed attempts still forward identical requests. Both URLs share one
capability executor; there is no legacy create/bind/compensation executor.

## Historical verification (2026-10-24; not the current contract)

This dated record describes the earlier implementation. Its delisted-pin
acceptance is excluded by the current release runbook;
it must not be used to restore an installation exemption. See the committed
regression tests and current pull request validation results.
- Focused plugin/expert/repository/config/handler package tests passed.
- `TEST_MYSQL_DSN=<isolated synthetic MySQL> go test ./...` passed, including
  real SQL batch access, released delisted pins, concurrent scoped attempt
  claims/completion conflicts and frozen-root retries after a later release.
- `go build ./...` passed with explicit checkout Git metadata.
- `make openapi-check` passed (10 existing warnings, no errors); regenerated
  output unchanged. `make openapi-diff` found no breaking changes.
- I4 real Fleet/Daemon execution remains pending; no remote code was changed,
  no feature enabled, no production/real tenant data accessed.
