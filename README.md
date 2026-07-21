# OCTO Marketplace

[![CI](https://github.com/Mininglamp-OSS/octo-marketplace/actions/workflows/ci.yml/badge.svg)](https://github.com/Mininglamp-OSS/octo-marketplace/actions/workflows/ci.yml)

OCTO Marketplace is the catalog and publishing service for Skills and MCP
servers in OCTO. It provides Space-aware discovery, owner-managed publishing,
archive parsing, version history, object-storage downloads, MCP connection
metadata, and remote MCP capability probing.

The service is consumed by
[`octo-web`](https://github.com/Mininglamp-OSS/octo-web) and automation clients
such as `octo-cli`. User and User Bot identities are verified through
[`octo-server`](https://github.com/Mininglamp-OSS/octo-server).

## Capabilities

- Skill catalog with public, Space, and private visibility
- Skill archive upload, validation, parsing, publishing, re-upload, download,
  and immutable version history
- MCP catalog with browsing, personal entries, system entries, categories,
  create/update/delete operations, and icon uploads
- Remote MCP probing for `streamable-http` and SSE transports, with DNS,
  redirect, private-network, and response-size protections
- User token and `bf_*` User Bot authentication with Space isolation
- Local filesystem storage for development and S3-compatible object storage
  for deployments
- Embedded, ordered MySQL migrations applied at service startup
- OpenAPI generation, drift detection, and linting in the development workflow

## Quickstart

### Docker Compose

The repository includes a development-only Compose stack containing the API
and MySQL:

```bash
git clone https://github.com/Mininglamp-OSS/octo-marketplace.git
cd octo-marketplace
docker compose up --build
```

Verify the service from another terminal:

```bash
curl http://127.0.0.1:8092/healthz
curl http://127.0.0.1:8092/readyz
```

The bundled credentials and `AUTH_ENABLED=false` setting are for local
development only.

### Local Go process

Start MySQL, create an `octo_marketplace` database, then configure the service:

```bash
export MYSQL_DSN='marketplace:marketplace@tcp(127.0.0.1:3306)/octo_marketplace?charset=utf8mb4&parseTime=true'
export AUTH_ENABLED=false
export DEV_AUTH_UID=dev-user
export DEV_AUTH_NAME=Developer
export DEV_SPACE_ID=dev-space

make run-api
```

The API listens on `:8092` by default. Migrations run automatically. Set
`SKIP_MIGRATION=true` only when schema changes are managed externally.

## Web integration

The service exposes `/api/v1/*`. OCTO Web uses the same-origin public prefix
`/market/api/v1/*`; the Vite development proxy or deployment gateway removes
`/market` before forwarding the request:

```text
octo-web /market/api/v1/skills
                  |
                  v
gateway   /api/v1/skills
                  |
                  v
octo-marketplace :8092
```

Authenticated browser requests send the OCTO token in `Token` or
`Authorization: Bearer`, plus `X-Space-Id`. When the browser calls Marketplace
through a different origin, add the exact web origin to
`CORS_ALLOWED_ORIGINS`; same-origin proxy deployments do not need a CORS
exception.

## Architecture

```text
                        identity verification
octo-web / octo-cli ------------------------------> octo-server
        |
        | REST: /market/api/v1/*
        v
octo-marketplace :8092
        |                    |
        | catalog metadata   | archives and icons
        v                    v
      MySQL            local storage / S3-compatible storage
```

Repository layout:

| Path | Purpose |
| --- | --- |
| `cmd/marketplace-api/` | API entry point and graceful HTTP server lifecycle |
| `internal/api/handler/` | Skill, MCP, category, upload, probe, and admin HTTP handlers |
| `internal/service/` | Catalog rules, visibility, parsing, probing, and secret handling |
| `internal/repository/` | MySQL persistence and transaction boundaries |
| `internal/storage/` | Local and S3-compatible object-storage implementations |
| `internal/middleware/` | User, User Bot, Space, and admin-token authentication |
| `migrations/sql/` | Embedded, ordered database migrations |
| `docs/openapi/` | Generated OpenAPI 3.1 contract |
| `docs/api/` | Additional protocol and contract documentation |

## Authentication and isolation

Authentication is enabled by default and fails closed. In an OCTO deployment,
configure:

```bash
AUTH_ENABLED=true
OCTO_API_URL=http://octo-server:5001
```

The service verifies user tokens with `octo-server`, requires an authoritative
Space, and scopes catalog access by that identity and Space. `bf_*` User Bot
tokens use the Bot's verified owner and Space.

Local development must disable authentication explicitly. Never deploy with
`AUTH_ENABLED=false`.

Administrative routes under `/api/v1/admin/*` use `X-Admin-Token`, configured
with `MARKETPLACE_ADMIN_TOKEN`. They do not rely on a browser user's role.

See [CONFIGURATION.md](CONFIGURATION.md) for all settings, secure defaults, and
storage examples.

## API

Canonical endpoint groups:

| Area | Endpoints |
| --- | --- |
| Health | `GET /healthz`, `GET /readyz` |
| Skills | `/api/v1/skills`, `/api/v1/skills/mine`, `/api/v1/skills/{skill_id}` |
| Skill files | `/api/v1/skill_uploads`, `/api/v1/skill_parse_tasks`, `/api/v1/skills/{skill_id}/download` |
| Skill categories | `/api/v1/skill_categories` |
| MCP catalog | `/api/v1/mcps`, `/api/v1/mcps/mine`, `/api/v1/mcps/{mcp_id}` |
| MCP support | `/api/v1/mcp_categories`, `/api/v1/mcps/_probe`, `/api/v1/mcp_icon_uploads` |
| Administration | `/api/v1/admin/mcps`, `/api/v1/admin/skill_categories` |

The authoritative machine-readable contract is
[`docs/openapi/swagger.yaml`](docs/openapi/swagger.yaml). MCP-specific behavior
and examples are documented in [`docs/api/mcp-v1.md`](docs/api/mcp-v1.md).
Legacy singular Skill routes remain available during migration, but new clients
should use the canonical plural routes above.

Marketplace business API responses use a standard envelope (health and
readiness probes intentionally return a minimal status object):

```json
{
  "data": {}
}
```

Errors use:

```json
{
  "error": {
    "code": "err.marketplace.example",
    "message": "Request failed",
    "details": {}
  }
}
```

## Storage

`STORAGE_DRIVER=local` is intended for local development. Production should
use `STORAGE_DRIVER=oss` with an S3-compatible endpoint and deployment-managed
credentials. Skill archives and MCP icons use separate configuration groups so
their buckets and public delivery policies can be managed independently.

Downloads are authorized by Marketplace before the client receives a redirect
or signed/public object URL. The catalog response does not expose internal
object keys or storage URLs.

See [CONFIGURATION.md](CONFIGURATION.md) for local, OSS, COS, signing-host, and
CDN examples.

## Development

```bash
make build
make test
make vet
make lint
make openapi-check
```

`make openapi-check` verifies handler annotation coverage, regenerates the
OpenAPI document, rejects contract drift, and runs Spectral linting.

Before submitting a change:

```bash
gofmt -w $(find . -name '*.go' -not -path './vendor/*')
go test ./...
go vet ./...
make openapi-check
```

## Additional documentation

- [Configuration reference](CONFIGURATION.md)
- [MCP API contract](docs/api/mcp-v1.md)
- [Generated OpenAPI contract](docs/openapi/swagger.yaml)
