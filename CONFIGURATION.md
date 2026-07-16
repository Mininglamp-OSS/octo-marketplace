# Configuration

Environment variables configure the API service.

## Core Settings

| Variable | Required | Default | Description |
| --- | --- | --- | --- |
| `MYSQL_DSN` | yes | — | Marketplace MySQL DSN |
| `API_PORT` | no | `8092` | HTTP listen port |
| `PUBLIC_BASE_URL` | no | `http://127.0.0.1:<API_PORT>` | External Marketplace URL used for local-storage redirects; may include a gateway prefix |
| `OCTO_API_URL` | when auth enabled | empty | `octo-server` API base URL |
| `AUTH_ENABLED` | no | `false` | Enable Octo token and Space verification |
| `AUTH_CACHE_TTL` | no | `30s` | Successful identity cache duration |
| `AUTH_CACHE_CAPACITY` | no | `10000` | Maximum cached identities |
| `DEV_AUTH_UID` | no | `dev-user` | Local identity when auth is disabled |
| `DEV_AUTH_NAME` | no | `Developer` | Local display name when auth is disabled |
| `DEV_SPACE_ID` | no | `dev-space` | Local Space when auth is disabled |
| `HTTP_READ_HEADER_TIMEOUT` | no | `5s` | Header read timeout |
| `HTTP_READ_TIMEOUT` | no | `15s` | Request read timeout |
| `HTTP_WRITE_TIMEOUT` | no | `30s` | Response write timeout |
| `HTTP_IDLE_TIMEOUT` | no | `60s` | Keep-alive idle timeout |
| `SKIP_MIGRATION` | no | `false` | Skip embedded SQL migrations when `true` |

## Storage Settings

| Variable | Required | Default | Description |
| --- | --- | --- | --- |
| `STORAGE_DRIVER` | no | `local` | Storage backend: `local` (filesystem) or `oss` (Alibaba Cloud OSS / S3-compatible) |
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

Example:

```bash
export MYSQL_DSN='marketplace:marketplace@tcp(127.0.0.1:3306)/octo_marketplace?charset=utf8mb4&parseTime=true'
go run ./cmd/marketplace-api
```

The credentials in `docker-compose.yaml` are development-only. Production must
provide rotated credentials through deployment-managed secrets.

## Authentication modes

Authentication is disabled by default so the service can run locally without
`octo-server`. In this mode, protected routes receive the configured development
identity and Space.

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
- `POST /api/v1/skill/admin/categories` — Create category (`name` required, optional `icon_key`, `sort_order`)
- `PUT /api/v1/skill/admin/categories/:id` — Update category
- `DELETE /api/v1/skill/admin/categories/:id` — Delete category (returns 409 if skills exist in category)

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
