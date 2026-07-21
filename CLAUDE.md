# CLAUDE.md

This file provides guidance to Claude Code when working in this repository.

## Project Overview

Octo Marketplace is a standalone Go service planned to host OCTO's Skill and MCP
marketplace control plane. Its service shape follows the API-service portion of
`octo-smart-summary`, with independent deployment and MySQL connectivity.

The current repository is intentionally only a runnable scaffold. Do not assume
catalog, policy, artifact storage, database persistence, installation, or CLI
synchronization already exists.

- **Go Module**: `github.com/Mininglamp-OSS/octo-marketplace`
- **Go Version**: 1.25
- **Default Branch**: `main`
- **Database Driver**: `github.com/go-sql-driver/mysql`

## Common Commands

```bash
make build
make test
make fmt
make vet
make lint

make run-api
docker compose up --build
```

Health endpoints:

```text
GET http://127.0.0.1:8092/healthz
GET http://127.0.0.1:8092/readyz
```

## Architecture

```text
octo-web / octo-cli -> marketplace-api
                              |
                              +-> independent MySQL / future object storage
                              +-> future octo-server identity/access API
```

Directory responsibilities:

- `cmd/marketplace-api/`: process wiring only
- `internal/api/router/`: HTTP route registration
- `internal/auth/`: Octo identity resolver boundary
- `internal/config/`: environment configuration
- `internal/model/`: internal types
- `internal/db/`: MySQL connection and future persistence implementation
- `internal/service/`: future business rules
- `migrations/sql/`: future embedded SQL migrations

Handlers should parse and render HTTP only. Put business rules in services and
persistence behind explicit repository interfaces once those layers are added.

## System Ownership

| System | Authority |
| --- | --- |
| `octo-server` | Login identity, users, Space membership/roles, bot ownership |
| `octo-marketplace` | Marketplace assets, releases, policy, effective plans, install status |
| `octo-cli` | Local download, verification, installation, reconciliation, reporting |
| Agent/runtime service | Agent and runtime lifecycle |

Never execute Skill code or launch MCP servers inside Marketplace.

## Security Rules

- Health endpoints may remain unauthenticated; document any other public route.
- `AUTH_ENABLED` defaults to `true`; standalone development explicitly disables
  it. Disabled mode uses fixed `DEV_AUTH_*` values, never
  caller-supplied identity headers.
- Resolve identity from the Octo token. Never trust body/query identity fields.
- Validate Space membership/role and agent ownership using authoritative APIs.
- Tenant-owned queries must include scope predicates even after middleware.
- Cross-Space failures must not leak resource existence.
- Treat uploaded/imported archives, manifests, Markdown, URLs, and MCP config as
  hostile input.
- Prevent path traversal, symlink escape, decompression bombs, oversized
  manifests, and unsafe URL destinations.
- Store secret references or required environment variable names, never MCP
  secret values.
- Published artifacts are immutable and distributed only after digest/signature
  verification.

## Errors and API Contracts

- Follow `tools/octo-api/SKILL.md` for Skill and MCP endpoint work and run
  `make openapi-check` before committing API changes.
- Use `{ "data": ... }` for success and
  `{ "error": { "code", "message", "details", "hint" } }` for failures.
- Use only the fixed OCTO error-code enum in
  `tools/octo-api/references/api-spec.md`; do not add service-specific wire codes.
- Do not return raw internal errors to clients.
- Log internal causes and sanitize authentication responses against enumeration.
- Version public API contracts deliberately once web and CLI clients consume
  them.
- Do not introduce an extra listener without a concrete internal API need.

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

## Persistence

Only MySQL connection management exists. When adding persistence:

- Use an independent Marketplace database.
- Never query `octo-server` tables directly.
- Add ordered embedded migrations under `migrations/sql/`.
- Serialize migrations with a database lock, following `octo-smart-summary`.
- Add rollback and migration tests before relying on new schema.

## Testing

```bash
go test ./...
go build ./...
docker compose config
```

Security-sensitive changes require negative tests, especially cross-Space
access, unauthorized agent bindings, archive traversal, signature failures, and
secret redaction. Use integration tags for tests requiring external services.

## Coding Conventions

- English Conventional Commits only.
- Never add `Co-Authored-By` commit trailers.
- Run `gofmt` for changed Go files.
- Keep changes focused and avoid speculative frameworks or abstractions.
- Use Gin for HTTP routing and middleware, matching the other OCTO Go services.
- Never commit credentials or generated local environment files.

<!-- octospec:begin -->
## octo-spec workflow

The repository inherits `octo-spec@1.1.0` through `.octospec/manifest.yaml`.

- Read `.octospec/rules/_index.yaml` before load-bearing changes.
- Record architectural work in `.octospec/tasks/<slug>/brief.md`.
- Briefs include goal, load-bearing behavior, out-of-scope items, and acceptance.

This region is managed by octo-spec tooling; edit outside the markers.
<!-- octospec:end -->
