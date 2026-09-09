# Configuration

Environment variables configure the API service.

## Core Settings

| Variable | Required | Default | Description |
| --- | --- | --- | --- |
| `MYSQL_DSN` | yes | — | Marketplace MySQL DSN |
| `API_PORT` | no | `8092` | HTTP listen port |
| `PUBLIC_BASE_URL` | no | `http://127.0.0.1:<API_PORT>` | External Marketplace URL used for local-storage redirects; may include a gateway prefix |
| `OCTO_API_URL` | when auth enabled | empty | `octo-server` API base URL |
| `AUTH_ENABLED` | no | `true` | Enable Octo token and Space verification; disable explicitly only for local development |
| `AUTH_CACHE_TTL` | no | `30s` | Successful identity cache duration |
| `AUTH_CACHE_CAPACITY` | no | `10000` | Maximum cached identities |
| `DEV_AUTH_UID` | no | `dev-user` | Local identity when auth is disabled |
| `DEV_AUTH_NAME` | no | `Developer` | Local display name when auth is disabled |
| `DEV_SPACE_ID` | no | `dev-space` | Local Space when auth is disabled |
| `DEV_AUTH_SPACE_ROLE` | no | `2` | Local Space role when auth is disabled (`0` member / `1` admin / `2` owner). Admin/owner is required to exercise the review queue locally. |
| `HTTP_READ_HEADER_TIMEOUT` | no | `5s` | Header read timeout |
| `HTTP_READ_TIMEOUT` | no | `15s` | Request read timeout |
| `HTTP_WRITE_TIMEOUT` | no | `150s` | Response write timeout; must be greater than `BOT_PUBLISH_TIMEOUT` because bot publish waits synchronously for parsing |
| `HTTP_IDLE_TIMEOUT` | no | `60s` | Keep-alive idle timeout |
| `PROBE_ALLOW_PRIVATE` | no | `false` | Allow MCP probes to private/local network targets; enable only in trusted self-hosted deployments |
| `BOT_PUBLISH_TIMEOUT` | no | `2m` | End-to-end synchronous parse budget for `POST /api/v1/bot/skills/publish` |
| `SKIP_MIGRATION` | no | `false` | Skip embedded SQL migrations when `true` |

## Plugin Review & IM Notification

These variables drive the Space-level plugin review workflow: notification of
new submissions to Space admins, and HMAC-signed card-action callbacks when an
admin clicks approve/deny inside an IM card.

| Variable | Required | Default | Description |
| --- | --- | --- | --- |
| `OCTO_NOTIFY_TIMEOUT` | no | `3s` | HTTP client timeout for calls to octo-server's internal notify and member-role endpoints. Must be short enough that a slow IM service does not stall the tenant submit path. Note: the dispatch path wraps the entire notify call in a hard 2-second best-effort budget (see `internal/notify/best_effort.go`); values above 2s will never bind the HTTP client, because the enclosing context is cancelled first. The 3s default and any larger configured value are effectively 2s at the wire. |
| `OCTO_MARKETPLACE_INTERNAL_TOKEN` | when notify URL set | empty | Shared secret sent as `X-Internal-Token` to octo-server's internal `/v1/internal/spaces/:space_id/members/:uid/role` and `/v1/internal/notify` endpoints. Setting `OCTO_API_URL` without this token produces a boot WARNING: review endpoints return 200 over HTTP but no approval cards are dispatched (the first signal is humans asking why their plugin was never reviewed). A mismatch produces 401s on every card dispatch and every card-action decision. |
| `OCTO_MARKETPLACE_CARD_ACTION_SECRET` | requires `OCTO_MARKETPLACE_INTERNAL_TOKEN` | empty | Shared HMAC secret octo-server uses to sign card-action callbacks (`POST /v1/card-actions/decide`). When empty, the callback route is permanently CLOSED (every request returns 401) rather than accepting unsigned input — that is the safe rollout path. Setting this WITHOUT `OCTO_MARKETPLACE_INTERNAL_TOKEN` FAILS BOOT: signatures would verify but operator-role authorization cannot happen, so every admin click 503s and retries into the DLQ. Setting `OCTO_MARKETPLACE_INTERNAL_TOKEN` without this secret produces a boot WARNING: cards are dispatched but every admin click is rejected (401). Match this against octo-server's `MARKETPLACE_CARD_ACTION_SECRET` / equivalent. |
| `OCTO_CARD_ACTION_MAX_SKEW` | no | `5m` | Maximum clock skew allowed on the `X-Octo-Timestamp` header (Go duration, e.g. `300s`, `2m`). Outside this window a signed request is rejected as stale to bound replay time. Hard upper bound: **15 minutes**. The default is 5m, which is already generous for inter-pod NTP drift; anything beyond 15m would leave a captured valid callback replayable against a still-valid signature for longer than any realistic delivery lag. Negative, zero, or malformed values fall back to the 5m default; values above 15m FAIL BOOT. |

For local development, `scripts/dev-octo-stub.py` is a minimal octo-server that
implements the member-role and notify endpoints; it honors
`OCTO_MARKETPLACE_INTERNAL_TOKEN`, and its synthetic role table is configured by
`DEV_STUB_ROLES="uid1:2,uid2:0"` (role `0` member / `1` admin / `2` owner).

## Logging Settings

Logs are written to stdout by default so Kubernetes can collect them without a
mounted volume. Every request receives an `X-Request-Id` value, either from the
incoming header or generated by the service; access logs, recovery logs, and
internal API error logs include the same request ID for correlation.

| Variable | Required | Default | Description |
| --- | --- | --- | --- |
| `LOG_LEVEL` | no | `info` | Minimum log level: `debug`, `info`, `warn`, or `error` |
| `LOG_FORMAT` | no | `console` | Log encoder: `console` for human-readable key/value text or `json` for structured JSON |
| `LOG_ADD_CALLER` | no | `false` | Include Go caller locations in logs |
| `LOG_FILE_ENABLED` | no | `false` | Also write a rotated `marketplace.log` file when true; stdout remains enabled |
| `LOG_DIR` | when file logging enabled | `/var/log/octo-marketplace` | Directory for rotated `marketplace.log` |
| `LOG_MAX_SIZE_MB` | no | `20` | Rotation size threshold for `marketplace.log`, in MiB |
| `LOG_MAX_BACKUPS` | no | `3` | Maximum rotated backups for `marketplace.log` |
| `LOG_MAX_AGE_DAYS` | no | `7` | Retention age for rotated `marketplace.log`, in days |

File logging is optional. Container deployments that enable it should mount
`LOG_DIR`; deployments that do not mount a volume should keep
`LOG_FILE_ENABLED=false`.

## Storage Settings

| Variable | Required | Default | Description |
| --- | --- | --- | --- |
| `STORAGE_DRIVER` | no | `local` | Storage backend: `local` (filesystem, **single-Pod only**) or `oss` (Alibaba Cloud OSS / S3-compatible). ⚠️ `local` stores files on the Pod's filesystem and is **not shared across replicas** — use it only for local development or single-Pod deployments. Production multi-Pod deployments **must** use `oss` with a shared object store (OSS/S3/COS). |
| `LOCAL_STORAGE_DIR` | no | `/tmp/marketplace-uploads` | Local filesystem directory (when STORAGE_DRIVER=local) |
| `MAX_UPLOAD_MB` | no | `20` | Maximum upload file size in megabytes |
| `OSS_ENDPOINT` | when oss | — | Canonical S3-compatible API endpoint used for signing and server-side operations |
| `OSS_REGION` | no | `us-east-1` | SigV4 region |
| `OSS_BUCKET` | when oss | — | OSS bucket name |
| `OSS_KEY_PREFIX` | no | empty | Environment/application prefix prepended to every object key |
| `OSS_PATH_STYLE` | no | `true` | Use path-style addressing; set `false` for Tencent COS virtual-host/custom-domain mode |
| `OSS_PUBLIC_ENDPOINT` | no | empty | Public-read CDN/custom-domain used for unsigned downloads and signed uploads |
| `OSS_SIGNING_HOST` | no | empty | Expected canonical host covered by signed uploads; mismatch fails closed |
| `OSS_DOWNLOAD_SIGNED` | no | `false` | Sign download URLs; when false, return the public CDN object URL |
| `OSS_ACCESS_KEY` | when oss | — | OSS access key ID |
| `OSS_SECRET_KEY` | when oss | — | OSS secret access key |

For Tencent COS behind a public-read custom CDN domain, use virtual-host
addressing and separate the browser host from the signing host. Uploads remain
signed; after Marketplace authorization, downloads redirect to the public CDN
object URL without signature query parameters.

```bash
OSS_ENDPOINT=https://cos.ap-beijing.myqcloud.com
OSS_REGION=ap-beijing
OSS_BUCKET=example-bucket
OSS_KEY_PREFIX=im-test/marketplace
OSS_PATH_STYLE=false
OSS_PUBLIC_ENDPOINT=https://cdn.example.com
OSS_SIGNING_HOST=example-bucket.cos.ap-beijing.myqcloud.com
OSS_DOWNLOAD_SIGNED=false
```

## Parse Worker Settings

Configuration for the asynchronous skill zip parsing worker and the Poll Lazy
Recovery mechanism that self-heals stuck tasks in multi-Pod deployments.

| Variable | Required | Default | Description |
| --- | --- | --- | --- |
| `SKILL_PARSE_TIMEOUT` | no | `1m` | Maximum execution time for a single parse worker run |
| `SKILL_PARSE_STALE_TIMEOUT` | no | `5m` | Duration after which a `parsing` task with no `updated_at` progress is considered stuck and eligible for recovery. **Must be greater than `SKILL_PARSE_TIMEOUT`** — startup fails if this constraint is violated |
| `SKILL_PARSE_MAX_ATTEMPTS` | no | `2` | Maximum number of recovery retries before marking the task as permanently failed (`PARSE_RETRY_EXHAUSTED`) |
| `SKILL_PARSE_WORKER_POOL_SIZE` | no | `10` | Maximum concurrent parse goroutines per Pod |

**Constraint:** `SKILL_PARSE_STALE_TIMEOUT > SKILL_PARSE_TIMEOUT` — the service
panics at startup if this invariant is not met, to prevent premature task
reclamation while a legitimate parse is still running.

Example:

```bash
export MYSQL_DSN='marketplace:marketplace@tcp(127.0.0.1:3306)/octo_marketplace?charset=utf8mb4&parseTime=true'
go run ./cmd/marketplace-api
```

The credentials in `docker-compose.yaml` are development-only. Production must
provide rotated credentials through deployment-managed secrets.

## Authentication modes

Authentication is enabled by default and fails closed unless `OCTO_API_URL` is
configured. Local development must explicitly disable it; in this mode,
protected routes receive the configured development identity and Space.

```bash
AUTH_ENABLED=false
DEV_AUTH_UID=dev-user
DEV_SPACE_ID=dev-space
```

Enable authentication when running with OCTO:

```bash
AUTH_ENABLED=true
OCTO_API_URL=http://octo-server:5001
```

Enabled mode validates tokens through
`POST /v1/auth/verify?include=context`, requires `X-Space-Id`, and verifies that
the authenticated user belongs to the requested Space. Production deployments
must set `AUTH_ENABLED=true`.

Requests authenticated with a `bf_*` User Bot token are validated through
`POST /v1/auth/verify-bot`. The verified owner is used as the business identity,
and the Bot's authoritative Space replaces the `X-Space-Id` request header.

## API Endpoints

### Public (no auth required)
- `GET /healthz` — Liveness check
- `GET /readyz` — Readiness check (verifies DB connection)

### Authenticated (`/api/v1/*`)

All routes require authentication (token via `Token:` or `Authorization: Bearer` header, plus `X-Space-Id` header when AUTH_ENABLED=true).

#### Session
- `GET /api/v1/session` — Current user identity

#### Categories
- `GET /api/v1/skill/categories` — List all categories with visible skill counts

#### Admin Categories (operations dashboard)
Retired. Category management moved to the unified plugin admin surface —
`GET/POST /api/v1/admin/plugin_categories` and
`PATCH/DELETE /api/v1/admin/plugin_categories/:category_id` (gated by the admin
role). The legacy `/api/v1/skill/admin/categories` routes are gone.

#### Skills
- `GET /api/v1/skill` — List skills (visibility-filtered, supports `?q=`, `?category_id=`, `?cursor=`, `?limit=`)
- `GET /api/v1/skill/mine` — List my skills
- `GET /api/v1/skill/:id` — Get skill detail (visibility-checked)
- `POST /api/v1/skill` — Create skill (from completed parse task)
- `PUT /api/v1/skill/:id` — Update skill (owner only, returns 404 for non-owner)
- `DELETE /api/v1/skill/:id` — Delete skill (owner only, returns 404 for non-owner)

#### Upload & Parse
- `POST /api/v1/skill/upload/init` — Initialize upload (returns presigned URL + upload_id)
- `POST /api/v1/skill/upload/:uploadId/parse` — Trigger zip parsing
- `GET /api/v1/skill/parse/:taskId` — Poll parse task status
- `POST /api/v1/skill/:id/reupload/init` — Initialize reupload for existing skill (owner only)
- `GET /api/v1/skill/:id/download` — Download skill file (302 redirect to presigned URL)

## Error Response Format

All error responses use a unified JSON format:

```json
{
  "code": "err.marketplace.xxx",
  "message": "Human-readable error message"
}
```

Standard error codes:
- `err.marketplace.bad_request` — Invalid parameters (HTTP 400)
- `err.marketplace.unauthorized` — Missing or invalid authentication (HTTP 401)
- `err.marketplace.not_found` — Resource not found or permission denied (HTTP 404)
- `err.marketplace.permission_denied` — Insufficient permissions (HTTP 403)
- `err.marketplace.file_too_large` — Upload exceeds MAX_UPLOAD_MB (HTTP 413)
- `err.marketplace.invalid_zip` — Invalid ZIP archive (HTTP 400)
- `err.marketplace.skill_md_not_found` — skill.md not found in ZIP (HTTP 400)
- `err.marketplace.category_in_use` — Category has skills, cannot delete (HTTP 409)
- `err.marketplace.conflict` — Resource conflict (HTTP 409)
- `err.marketplace.internal_error` — Internal server error (HTTP 500)
