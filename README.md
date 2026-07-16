# OCTO Marketplace

OCTO Marketplace is an independent Go service scaffold for the future Skill and
MCP marketplace.

## Architecture

```text
octo-web -> marketplace-api
octo-cli -> marketplace-api
octo-server <-> marketplace-api
```

This repository follows the API-service portion of `octo-smart-summary`:
graceful shutdown, MySQL connectivity, containers, and reserved directories for
later Marketplace and Octo integration.

## Current scaffold

- Public health and readiness endpoints
- Configuration and Octo auth client foundations
- Unified user and `bf_*` User Bot authentication
- Skill CRUD, upload, parsing, and Local/OSS-backed downloads
- Reserved migration directory with an empty baseline
- API binary and container image
- `.octospec` rules and an initial architecture task brief

Marketplace catalog, publishing, versioning, and MCP business APIs remain deferred.

## Run

```bash
go run ./cmd/marketplace-api
```

See [`CONFIGURATION.md`](CONFIGURATION.md) for environment variables. The API
runs embedded SQL migrations at startup; set `SKIP_MIGRATION=true` only when
migrations are managed externally.

Authentication is disabled by default for standalone development. The sample
protected endpoint is available at `/api/v1/session`. Set `AUTH_ENABLED=true`
and `OCTO_API_URL` to enable Octo token and Space verification.

The API listens on port `8092`. Docker Compose exposes MySQL on local port
`3306`.

For a self-contained smoke demo:

```bash
docker compose up --build
curl http://127.0.0.1:8092/healthz
curl http://127.0.0.1:8092/readyz
```

## Planned web mount

The web client should call `/market/api/v1`; nginx or the Vite development
proxy removes `/market` before forwarding to this service.

## Design documents

- [Octo CLI 从 Marketplace 安装 Skill 方案](docs/octo-cli-skill-install.md)
- [GitHub CI 补充与改造方案](docs/github-ci-improvement.md)
