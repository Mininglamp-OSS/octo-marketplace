# AGENTS.md

This file provides guidance to coding agents when working in this repository.

## Project Overview

Octo Marketplace is an independent Go service for the future OCTO Skill and MCP
marketplace. It is modeled after the API-service portion of
`octo-smart-summary`: independent deployment, MySQL connectivity, and explicit
integration boundaries with
`octo-server`, `octo-web`, and `octo-cli`.

The repository is currently a runnable scaffold. Marketplace catalog, policy,
artifact, installation, database, and CLI synchronization behavior have not
been implemented yet.

- Go module: `github.com/Mininglamp-OSS/octo-marketplace`
- Go version: 1.25
- Default branch: `main`
- Database driver: `github.com/go-sql-driver/mysql`
- HTTP framework: Gin

## Common Commands

```bash
# Build both binaries
make build

# Run tests
make test

# Format and vet
make fmt
make vet
make lint

# Run locally
make run-api
# Run the self-contained demo
docker compose up --build
```

Smoke endpoints:

```bash
curl http://127.0.0.1:8092/healthz
curl http://127.0.0.1:8092/readyz
```

## Architecture

### Process Layout

```text
octo-web / octo-cli
        |
        v
marketplace-api (:8092)
        |
        +---- future persistence / artifact storage
        +---- future octo-server auth and Space access resolution

```

### Repository Layout

| Path | Purpose |
| --- | --- |
| `cmd/marketplace-api/` | Public and internal API process entrypoint |
| `internal/api/router/` | HTTP route wiring and basic health endpoints |
| `internal/auth/` | Reserved Octo token resolution client |
| `internal/config/` | Environment configuration and validation |
| `internal/model/` | Shared internal transport/domain types |
| `internal/db/` | MySQL connection and future persistence boundary |
| `internal/service/` | Reserved business service layer; currently empty |
| `migrations/sql/` | Reserved embedded migration location |

Keep API wiring, business rules, and persistence separated.
Do not place business logic directly in `cmd/` or HTTP handlers.

## Service Boundaries

- `octo-server` remains authoritative for users, authentication, Space
  membership, Space roles, and bot/agent ownership exposed by Octo.
- `octo-marketplace` will own marketplace metadata, versions, scope policy,
  effective asset plans, and installation status.
- `octo-cli` will download, verify, install, reconcile, and report assets on an
  agent machine.
- Agent/runtime lifecycle remains outside this service.
- Never execute Skill scripts or start MCP processes in this service.

## Authentication and Space Isolation

Health endpoints are intentionally unauthenticated. Future business endpoints
must authenticate through Octo and enforce scope access server-side.

Authentication is controlled by `AUTH_ENABLED` and defaults to `true` so a
missing production setting fails closed. Standalone development explicitly
disables it. Disabled mode injects `DEV_AUTH_UID`, `DEV_AUTH_NAME`,
and `DEV_SPACE_ID`. Production deployments must enable authentication. Enabled
mode calls `/v1/auth/verify?include=context` and fails closed unless the response
contains authoritative Space context.

- Never trust client-supplied `uid`, `space_id`, `scope_id`, or `agent_id`.
- Resolve user identity from the Octo token.
- Verify Space membership and role through an authoritative Octo contract.
- Verify agent ownership/access before reading or changing agent bindings.
- Cross-Space reads and writes must fail closed without revealing whether a
  resource exists in another Space.
- Document any intentionally unauthenticated route in code.

## Marketplace Safety Boundaries

When marketplace behavior is introduced:

- Published versions are immutable; publish a new version instead of mutating
  an existing artifact.
- Verify artifact digest and signature before distribution.
- Treat imported manifests, archives, URLs, Markdown, and configuration as
  untrusted input.
- Reject archive traversal, symlink escape, absolute paths, oversized payloads,
  and unsafe executable content at the ingestion boundary.
- MCP manifests may describe required secret names, but must never persist or
  log secret values.
- The server resolves global, Space, user, and agent policy. CLI clients receive
  an effective plan and must not reproduce authorization logic.

## API Contracts and Error Handling

Skill and MCP Marketplace endpoints follow the vendored OCTO OpenAPI standard
under `tools/octo-api/`. Run `make openapi-check` after changing an endpoint.
Handlers must not expose raw Go errors, SQL errors, internal URLs, credentials,
or filesystem paths.

- Successful responses use `{ "data": ... }`; list responses add the standard
  cursor or offset `pagination` object.
- Failed responses use `{ "error": { "code", "message", "details", "hint" } }`.
- Error codes are restricted to the fixed enum documented in
  `tools/octo-api/references/api-spec.md`.
- Log internal causes server-side and return a generic 5xx response.
- Authentication failures use a generic response to prevent enumeration.
- Keep wire contracts backward compatible after clients begin consuming them.

## OpenAPI Workflow

Any task that adds, changes, or reviews an HTTP endpoint must load and follow
`$octo-api` before implementation or review.

- Run `make openapi-check` for every endpoint change.
- Run `make openapi-diff` when modifying an existing endpoint.
- Regenerate and commit the OpenAPI artifacts together with the implementation.
- Keep handler routes, request/response schemas, operation IDs, and generated
  OpenAPI in sync.
- Do not consider endpoint work complete while either validation command fails
  or generated OpenAPI differs from the handlers.

## Database and Migrations

Only MySQL connection management exists in the scaffold. When persistence is added:

- Marketplace uses its own database; do not read or write `octo-server` tables.
- Place ordered migrations in `migrations/sql/` and embed them in the binary.
- Serialize migration execution with a database lock, following
  `octo-smart-summary/internal/db/migrate.go`.
- Scope every tenant-owned query explicitly; do not rely only on middleware.
- Published artifacts and audit records require immutable or append-only data
  semantics where appropriate.

## Testing

- Run the narrowest relevant package test first, then `go test ./...`.
- Add tests proportional to risk for authentication, Space isolation, policy
  resolution, archive handling, signature verification, and migrations.
- Cross-Space negative cases are mandatory for scoped data access.
- Tests should not require external services unless marked as integration tests.
- Keep health endpoints and graceful shutdown covered by lightweight tests.

## Coding Conventions

- Use English Conventional Commits: `feat:`, `fix:`, `test:`, `refactor:`,
  `docs:`, and `chore:`.
- Do not add `Co-Authored-By` lines to commit messages.
- Keep changes scoped; avoid unrelated refactors.
- Run `gofmt` on edited Go files.
- Prefer explicit interfaces at external boundaries, not speculative internal
  abstractions.
- Do not add a framework or dependency until the implementation needs it.
- Do not add extra listeners without a concrete need.
- Secrets come from environment or a secret manager and are never committed.

<!-- octospec:begin -->
## octo-spec engineering standard

This repository carries shared engineering metadata in `.octospec/`.

- Read `.octospec/rules/_index.yaml` and matching rules before changing
  load-bearing behavior.
- Capture architectural tasks in `.octospec/tasks/<slug>/brief.md` with goal,
  load-bearing list, out-of-scope items, and acceptance criteria.
- Trivial documentation and scaffold maintenance do not require a new task.

This region is managed by octo-spec tooling; edit outside the markers.
<!-- octospec:end -->
