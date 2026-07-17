# MCP Catalog API — v1

> Base path: **`/market/api/v1`**
> Owner: `octo-marketplace`
> Consumers: `octo-web`, later `octo-cli`
> Related brief: `.octospec/tasks/mcp-catalog-v1/brief.md`

This document is the authoritative behavior contract for the MCP CRUD slice of
octo-marketplace. The exact generated wire schema lives in
`docs/openapi/swagger.yaml`; handler code, tests, and client integration must
stay aligned with both. Do not extend the surface here without first updating
this file and getting review sign-off.

---

## 0. Constants shared with the client

| Name | Value | Owner |
| --- | --- | --- |
| `SECRET_PLACEHOLDER_SENTINEL` | `"__OCTO_SECRET_PLACEHOLDER__"` | Frontend and backend both know this literal. The frontend renders a localized label ("请把这里换成你的 Token" / "Replace with your token") for the user but always submits the sentinel back on the wire; the backend treats sentinel and empty-string as equivalent (see §5). Fixed ASCII so no i18n mismatch on submit. |
| `CATEGORY_KEY_ALL` | `"all"` | Reserved category key that disables the category filter on `GET /mcps` and `GET /mcps/mine`. |

Type alignment between wire and TS
-----------------------------------

Wire responses are a **superset** of the current
`packages/dmworkmcp/src/types/mcp.ts` shapes. Extra fields shipped by the
server are silently ignored by the frontend today. The following extras are
intentional and the frontend is expected to add them to its TS types when it
begins to consume them:

- `McpListItem` on the wire carries `visibility` and `creator_name`; TS
  today does not. List-card UI should promote at least `visibility` to a
  card badge in the next frontend pass.
- `McpDetail` on the wire carries `created_at` and `updated_at`; TS today
  does not.

The doc uses "superset" and never claims 1:1 type match.

## 1. Auth

All endpoints (except the general health probes under `/`) require the caller
to present a valid Octo token.

| Header | Value | Notes |
| --- | --- | --- |
| `token` | Octo access token | Matches `octo-web`'s `WKApp.apiClient` convention. |
| `X-Space-Id` | Space UUID | Required on every MCP endpoint; anchors the visibility filter and the ownership scope. |
| `Accept-Language` | e.g. `zh-CN, en;q=0.8` | Optional; forwarded to the token resolver. |

Server-side flow on every business request:

1. Resolve `token` → `Identity{uid, name}` via `internal/auth`. Reject with
   HTTP 401 / `AUTH_REQUIRED` on failure.
2. Read `X-Space-Id`. Missing → HTTP 400 / `VALIDATION_ERROR`.
3. Verify `uid` is a member of that Space through the authoritative Octo
   membership probe. Failure → HTTP 403 / `FORBIDDEN`.
4. Never trust `owner_uid`, `space_id`, `creator_name` or any other identity
   field in the request body. These are stamped from step 1–3.

## 2. Error envelope

Every non-2xx response uses the standard OCTO OpenAPI error shape:

```json
{
  "error": {
    "code": "NOT_FOUND",
    "message": "MCP not found"
  }
}
```

- `code` is the stable machine-readable enum from the shared OCTO OpenAPI
  contract (`VALIDATION_ERROR`, `AUTH_REQUIRED`, `FORBIDDEN`, `NOT_FOUND`,
  `DUPLICATE`, `INTERNAL_ERROR`, ...). Clients switch on `code`, not on
  `message`.
- `message` is a human-readable summary for logs and toasts. It does not
  contain internal paths, credentials, SQL, or Go error strings.
- Additional fields (`details`, `hint`, …) may appear inside `error` for
  validation failures or operator guidance.

### Error code catalog

| HTTP | Code (wire) | When |
| --- | --- | --- |
| 400 | `VALIDATION_ERROR` | Body fails structural validation; invalid visibility / transport / slug / secret-leak and probe-request validation all collapse to this shared enum. `error.details[]` may list offending fields. |
| 401 | `AUTH_REQUIRED` | Missing / invalid Octo token. |
| 403 | `FORBIDDEN` | Caller is outside the requested Space, or is not allowed to mutate the MCP. |
| 404 | `NOT_FOUND` | Record does not exist, or exists in a different Space and cross-Space discovery is forbidden. |
| 409 | `DUPLICATE` | Name or slug collides with another live record. |
| 500 | `INTERNAL_ERROR` | Unclassified server error. Details are logged server-side only. |

## 3. Resource shape

### 3.1 `McpDetail`

The full record returned by `GET /mcps/{mcp_id}`, `POST /mcps`, `PATCH /mcps/{mcp_id}`.
Field names match the `octo-web` `dmworkmcp` package where the type overlaps;
`created_at` / `updated_at` are wire-only extras (see §0).

```json
{
  "mcp_id": "01HK7Z3B9YV0K5H0KR6QF8N4M2",
  "name": "GitHub MCP",
  "slogan": "读写仓库、Issue、PR",
  "category": "dev",
  "icon": "🐙",
  "tags": ["官方", "热门"],
  "tool_count": 8,
  "visibility": "public",
  "creator_name": "GitHub Bot",
  "quick_start": {
    "transport": "streamable-http",
    "server_name": "GitHub MCP",
    "slug": "github-mcp",
    "url": "https://mcp.deepminer.com.cn/github/mcp",
    "auth_type": "bearer",
    "headers": { "X-Trace-Origin": "octo-web" }
  },
  "tools": [
    { "name": "list_repositories", "description": "列出仓库" }
  ],
  "usage_examples": ["帮我在 octo-web 仓库里创建一个 Issue"],
  "faqs": [
    { "question": "需要哪些权限？", "answer": "至少需要 repo" }
  ],
  "notes": ["Token 请使用最小必要权限"],
  "created_at": "2026-07-14T10:15:00.000+08:00",
  "updated_at": "2026-07-14T10:15:00.000+08:00"
}
```

Field notes:

- `mcp_id`: server-generated, ULID-style opaque 26-char string. Clients treat
  it as opaque; never derive it from `name`.
- `icon`: emoji, short label, or a `data:` / `https://` image URL. No
  length limit at the API layer; the schema caps `MEDIUMTEXT`.
- `tags`: string array; entries de-duplicated and trimmed server-side.
- `tool_count`: derived from `tools.length`; always echoed for card display.
- `visibility`: one of `public` / `private` / `system`. `system` never
  appears in a client-write; it appears in reads for platform-provided
  records.
- `creator_name`: snapshot of the owner's `Identity.name` at create time.
  Not updated when the underlying user renames themselves.
- `quick_start.server_name`: defaults to `name`; not a separate user input.
  See §3.3 for the full mapping. Used inside prompt-tab template copy
  (human-readable).
- `quick_start.slug`: the ASCII identifier used as the JSON KEY in
  generated `mcpServers` snippets. Sent by the client at the top level of
  the create/patch body (§3.3); auto-derived by the server from `name`
  (lower-case, `[a-z0-9]` runs joined by `-`, hyphens trimmed, ≤ 64
  chars) when the client omits it. Reserved shape: `^[a-z0-9-]{1,64}$`.
  Unique per Space among live rows (mig 03) — a collision yields
  `err.marketplace.mcp.slug_taken`. Malformed or empty-after-slugify
  yields `err.marketplace.mcp.slug_invalid` (§2). Slug and `server_name`
  are distinct fields on purpose: `server_name` is the display label in
  prompts, `slug` is the JSON key — separating them lets a Chinese
  display name coexist with an ASCII config key.
- `quick_start.headers` / `quick_start.env`: values for keys matching
  `(?i)token|key|secret|password|pwd|auth` are always empty in responses
  (see §5 Secret redaction).
- `usage_examples` / `notes`: string arrays. Empty entries filtered out.
- `faqs`: array of `{question, answer}`; entries with an empty question are
  filtered out.
- `created_at` / `updated_at`: RFC 3339 with millisecond precision, in the
  server's local timezone.

### 3.2 `McpListItem`

Projection used by `GET /mcps` and `GET /mcps/mine`. Wire response is a
superset of the frontend TS type (§0):

```json
{
  "mcp_id": "01HK7Z3B9YV0K5H0KR6QF8N4M2",
  "name": "GitHub MCP",
  "slogan": "...",
  "category": "dev",
  "icon": "🐙",
  "tags": ["官方", "热门"],
  "tool_count": 8,
  "visibility": "public",
  "creator_name": "GitHub Bot"
}
```

### 3.3 Field mapping: flat create body → nested detail response

Create/update requests are FLAT (mirrors the frontend `CreateMcpParams`
shape). Read responses NEST the connection under `quick_start{}`. The
mapping is fixed here so both sides implement one translation, not two:

| Flat field on write | Nested field on read | Notes |
| --- | --- | --- |
| `transport` | `quick_start.transport` | Verbatim. |
| `url` | `quick_start.url` | Empty string collapses to omitted in response. |
| `command` | `quick_start.command` | stdio only. |
| `args` | `quick_start.args` | Array. Empty array collapses to omitted. |
| `env` | `quick_start.env` | Record. Empty record collapses to omitted. |
| `headers` | `quick_start.headers` | Record. Empty record collapses to omitted. Secret-key values are stripped (§5). |
| `auth_type` | `quick_start.auth_type` | Default `"none"`. |
| `slug` | `quick_start.slug` | Client sends flat; server echoes nested. Auto-derived from `name` when omitted. See field notes above. |
| *server-derived* | `quick_start.server_name` | Server sets to `name.trim()`. Not accepted from client. |

Top-level fields (`name`, `slug`, `slogan`, `category`, `icon`, `tags`,
`tools`, `usage_examples`, `faqs`, `notes`, `visibility`) round-trip 1:1
between write and read shapes.

Fields set by the server, never by the client:
`mcp_id`, `owner_uid` (server-only, never surfaced), `creator_name`, `tool_count`,
`created_at`, `updated_at`, `quick_start.server_name`. Request bodies are
strict: client-supplied server fields or any other unknown field are rejected
with `VALIDATION_ERROR`.

Auth-related fields never on the wire:
`config.headers.Authorization` is stripped on write and never returned.
The frontend re-generates the `Authorization: Bearer <sentinel>` line
locally when it renders the JSON quick-start snippet, purely from the
`auth_type` marker.

## 4. Endpoints

### 4.1 `POST /mcps` — create

Publish a new MCP owned by the caller.

**Request body:**

```json
{
  "name": "My GitHub MCP",
  "slogan": "写 Issue 用的",
  "category": "dev",
  "icon": "🐙",
  "tags": ["个人"],
  "transport": "streamable-http",
  "url": "https://mcp.example.com/github",
  "auth_type": "bearer",
  "headers": { "X-Trace": "web" },
  "command": null,
  "args": [],
  "env": {},
  "tools": [
    { "name": "create_issue", "description": "创建 Issue" }
  ],
  "usage_examples": ["帮我建 Issue"],
  "faqs": [],
  "notes": [],
  "visibility": "public"
}
```

- `name` is required; every other field has a documented default.
- `transport` decides which of `url` / `command`+`args`+`env` is meaningful.
- `visibility` accepts only `public` or `private`. Any other value —
  including `system` — yields `err.marketplace.mcp.invalid_visibility`.
- Client-supplied `mcp_id`, `owner_uid`, `space_id`, `creator_name`,
  `created_at`, `updated_at`, `tool_count` are rejected as unknown fields (§3.3).

**Response (201):** the full `McpDetail` for the newly created record —
same shape as `GET /mcps/{mcp_id}`. Frontend picks up `mcp_id` from the response.

**Errors:**
- 400 `err.marketplace.mcp.invalid_request` /
      `err.marketplace.mcp.invalid_visibility` /
      `err.marketplace.mcp.invalid_transport` /
      `err.marketplace.mcp.secret_leaked` /
      `err.marketplace.mcp.slug_invalid`
- 401 `err.marketplace.auth.unauthorized`
- 403 `err.marketplace.auth.forbidden_space`
- 409 `err.marketplace.mcp.name_taken` /
      `err.marketplace.mcp.slug_taken`

### 4.2 `GET /mcps` — list (Space-scoped)

Returns every record visible to the caller inside their current Space:

- all `system` records, plus
- all `public` records in `X-Space-Id`, plus
- the caller's own `private` records in `X-Space-Id`.

**Query parameters:**

| Name | Type | Default | Meaning |
| --- | --- | --- | --- |
| `keyword` | string | — | Case-insensitive substring match on `name` and `slogan`. |
| `category` | string | `all` | Category key; `all` disables the filter. |
| `page` | int | `1` | One-based page number. |
| `page_size` | int | `20` | Page size, max `100`. |

**Response (200):**

```json
{
  "data": [ /* McpListItem[] */ ],
  "pagination": {
    "total": 42,
    "page": 1,
    "page_size": 20
  }
}
```

- `total` is the count after `keyword` + `category` filters, before
  pagination.
- Category filter options are supplied by the dedicated category API; MCP list
  responses do not embed category facets.
- Order: newest first (`created_at DESC`). Not configurable in v1.

**Errors:** 401 / 403.

### 4.3 `GET /mcps/mine` — my MCPs

Returns every record owned by the caller in their current Space,
regardless of visibility (including their own `private`). Never leaks
anything owned by another user.

**Query parameters:** same `keyword`, `category`, `page`, `page_size` as
`GET /mcps`.

**Response (200):** same envelope as `GET /mcps`, with `data` and
`pagination.total` restricted to `owner_uid == caller`.

**Errors:** 401 / 403.

### 4.4 `GET /mcps/{mcp_id}` — detail

Returns a single `McpDetail` if visible to the caller:

- record's `visibility` is `system`, OR
- record's `space_id == X-Space-Id` AND (`visibility == public` OR
  `owner_uid == caller`).

Otherwise the response is `404 err.marketplace.mcp.not_found` — never
`403` — so cross-Space enumeration is closed.

**Errors:** 401 / 403 (auth) / 404.

### 4.5 `PATCH /mcps/{mcp_id}` — update

Partial update. Only the record's owner may call this endpoint; anyone
else receives `err.marketplace.mcp.forbidden`.

**Mutable fields:** `name`, `slug`, `slogan`, `category`, `icon`, `tags`,
`transport`, `url`, `command`, `args`, `env`, `headers`, `auth_type`,
`tools`, `usage_examples`, `faqs`, `notes`, `visibility` (`public` /
`private` only). `slug` follows the same shape rules as create (§3.1
field notes); a non-nil empty string is rejected as `slug_invalid`.

**Immutable fields:** `mcp_id`, `owner_uid`, `space_id`, `creator_name`,
`created_at`. Attempts to send them are rejected as unknown fields.

**Response (200):** the updated `McpDetail`.

**Errors:** 400 (`err.marketplace.mcp.invalid_request` /
`err.marketplace.mcp.slug_invalid` / …) / 401 / 403
(`err.marketplace.auth.forbidden_space` or
`err.marketplace.mcp.forbidden`) / 404 / 409
(`err.marketplace.mcp.name_taken` if a rename collides;
`err.marketplace.mcp.slug_taken` if a slug change collides).

### 4.6 `DELETE /mcps/{mcp_id}` — soft delete

Only the owner may call this endpoint. The row is soft-deleted
(`deleted_at` set to now); `GET /mcps` and `GET /mcps/{mcp_id}` treat it as
gone. A second create with the same name is allowed after delete.

**Response (204):** empty body.

**Errors:** 401 / 403 / 404.

### 4.7 `POST /mcps/probe` — try-connect + fetch tool list

Runs an MCP `initialize` + `tools/list` handshake against a remote MCP server
and returns the tool catalog. The create wizard calls this to auto-populate the
tools grid so the user does not have to type each tool name by hand.

**Auth:** same headers as every other endpoint (§1). The identity is required
so the endpoint cannot be used as an open HTTP proxy. Space membership is
verified as usual.

**Request body:**

```json
{
  "transport": "streamable-http",
  "url": "https://mcp.example.com/github",
  "headers": { "Authorization": "Bearer ghp_realTokenPastedByUserJustForProbe" },
  "command": null,
  "args": [],
  "env": {}
}
```

- `transport` — one of `streamable-http` / `sse`. `stdio` is rejected with
  `err.marketplace.mcp.probe_unsupported`: the marketplace host must not spawn
  arbitrary user commands. stdio probing is the desktop client's job.
- `url` — required for remote transports. Must be `http` or `https`; other
  schemes (`file://`, `ftp://`, …) are rejected with
  `err.marketplace.mcp.invalid_request`.
- `headers` — optional custom headers to forward on every probe request
  (including `Authorization` if the server needs a token to answer
  `tools/list`). The `SECRET_PLACEHOLDER_SENTINEL` (§0) is dropped before
  forwarding — an empty/redacted secret means "no auth", not "literal
  sentinel string".
- `command` / `args` / `env` — ignored for remote transports. Present in the
  schema so the frontend can submit a single shape regardless of transport.

**Response (200 — success):**

```json
{
  "ok": true,
  "tools": [
    { "name": "list_repositories", "description": "列出仓库" },
    { "name": "create_issue",      "description": "创建 Issue" }
  ],
  "server_info": { "name": "GitHub MCP", "version": "1.2.0" }
}
```

**Response (200 — operational failure):**

```json
{
  "ok": false,
  "tools": [],
  "error": { "code": "timeout", "message": "probe timed out" }
}
```

Operational failures return HTTP 200 with `ok=false` and an in-body `error`.
This lets the frontend surface a friendly localized message without inventing
a synthetic HTTP error code. Only auth / malformed-body failures use the
standard §2 envelope (`err.marketplace.*`) with a non-2xx status.

**In-body error codes** (`error.code`):

| Code | Meaning |
| --- | --- |
| `timeout` | The probe exceeded the 15 s hard cap (server unreachable / too slow). |
| `init_failed` | `initialize` failed — server unreachable, wrong URL, non-2xx response, or JSON-RPC error. `error.message` carries a truncated cause. |
| `no_tools_capability` | `initialize` succeeded but the server did not advertise a `tools` capability. |
| `command_not_found` | Reserved for the desktop client's stdio path (LSC-70); the marketplace server never emits this code. |

**Behavior notes:**

- Timeout: the endpoint caps the full handshake at 15 seconds. Individual
  responses are bounded to 4 MiB to prevent memory abuse.
- Handshake: `initialize` → `notifications/initialized` → `tools/list`. The
  notification is best-effort; some servers return 202/204/nothing, and any
  wire error on the notification is ignored.
- Content types: the endpoint handles both `application/json` responses and
  `text/event-stream` (SSE-framed) responses on the same POST.
- Session id: if the server returns an `Mcp-Session-Id` header after
  `initialize`, subsequent requests in the same probe reuse it.
- Nothing about the probe is persisted. The endpoint does not write to the
  catalog and does not log the returned tools.

**Errors** (standard §2 envelope, non-2xx):

- 400 `err.marketplace.mcp.invalid_request` — missing / malformed URL.
- 400 `err.marketplace.mcp.invalid_transport` — unknown transport.
- 400 `err.marketplace.mcp.probe_unsupported` — `stdio` transport.
- 401 `err.marketplace.auth.unauthorized`.
- 403 `err.marketplace.auth.forbidden_space`.

## 5. Secret redaction

Applied on every write (`POST`, `PATCH`) BEFORE persistence.

### 5.1 `config.env` and `config.headers`

For each entry `(k, v)`:

1. If `k` matches
   `(?i)^(authorization|token|.*token|.*key|.*secret|password|pwd|api[-_]?key)$`:
   - If `v` is empty OR equal to the shared
     `SECRET_PLACEHOLDER_SENTINEL` (§0) — accept, store empty string.
   - Otherwise — reject the entire request with
     `err.marketplace.mcp.secret_leaked` and `err.details[]` naming the
     key.
2. Non-matching keys are stored as-is.

Rationale for the sentinel over a natural-language placeholder: the
frontend runs under multiple locales (zh-CN, en-US). A localized
placeholder like `"请把这里换成你的 Token"` vs
`"Please replace with your token"` would fail case-1 comparison under
the wrong locale, forcing the user through a `secret_leaked` error
before every real submit. The ASCII sentinel is locale-independent and
grep-friendly.

### 5.2 `auth_type`

- `auth_type: "bearer"` is a marker only. Server never persists the
  token.
- `auth_type: "none"` is the default; when the field is absent or empty
  the server writes `"none"`.

### 5.3 Response side

The redaction is one-way in this contract; a value that was never
persisted does not come back. Responses always show empty strings for
the sensitive keys above.

## 6. Examples

### 6.1 Create → returns detail

```http
POST /market/api/v1/mcps HTTP/1.1
token: <opaque>
X-Space-Id: 3fa85f64-5717-4562-b3fc-2c963f66afa6
Content-Type: application/json

{"name":"Slack MCP","slug":"slack-mcp","category":"productivity",
 "transport":"streamable-http","url":"https://mcp.example.com/slack",
 "auth_type":"bearer","visibility":"public","tools":[]}
```

```http
HTTP/1.1 201 Created
Content-Type: application/json

{"mcp_id":"01HK7Z3B9YV0K5H0KR6QF8N4M2","name":"Slack MCP","slogan":"",
 "category":"productivity","icon":"","tags":[],"tool_count":0,
 "visibility":"public","creator_name":"李世超",
 "quick_start":{"transport":"streamable-http","server_name":"Slack MCP",
               "slug":"slack-mcp",
               "url":"https://mcp.example.com/slack","auth_type":"bearer",
               "headers":{}},
 "tools":[],"usage_examples":[],"faqs":[],"notes":[],
 "created_at":"2026-07-14T18:30:12.123+08:00",
 "updated_at":"2026-07-14T18:30:12.123+08:00"}
```

### 6.2 List with keyword

```http
GET /market/api/v1/mcps?keyword=git&page=1&page_size=20 HTTP/1.1
token: <opaque>
X-Space-Id: 3fa85f64-…
```

```http
HTTP/1.1 200 OK
Content-Type: application/json

{"data":[{"mcp_id":"01HK7Z3B9YV0K5H0KR6QF8N4M2","name":"GitHub MCP",
           "slogan":"…","category":"dev","icon":"🐙",
           "tags":["官方","热门"],"tool_count":8,
           "visibility":"public","creator_name":"GitHub Bot"}],
 "pagination":{"total":1,"page":1,"page_size":20}}
```

### 6.3 Sentinel accepted / plain token rejected

Accepted — client submitted the sentinel:

```http
POST /market/api/v1/mcps
{"name":"x","transport":"stdio","command":"npx",
 "env":{"GITHUB_TOKEN":"__OCTO_SECRET_PLACEHOLDER__"},
 "visibility":"private"}
```

```http
HTTP/1.1 201 Created
… env.GITHUB_TOKEN persisted as "" …
```

Rejected — client submitted a real token by accident:

```http
POST /market/api/v1/mcps
{"name":"x","transport":"stdio","command":"npx",
 "env":{"GITHUB_TOKEN":"ghp_realTokenPastedByAccident"},
 "visibility":"private"}
```

```http
HTTP/1.1 400 Bad Request
{"err":{"code":"err.marketplace.mcp.secret_leaked",
        "message":"Secret value must not be submitted",
        "details":[{"field":"env.GITHUB_TOKEN","reason":"non_empty"}]}}
```

## 7. Performance & limits (v1 posture)

Sized for the prototype scale. Revisit when a scale metric trips these
thresholds.

- **`GET /mcps` keyword search is a full scan** over the caller's visible
  set. `name`/`slogan` `LIKE %kw%` is non-sargable; no full-text index
  in v1. Acceptable while a single Space has < ~10 k MCPs. Beyond that,
  add a FULLTEXT index or a sidecar search index.
- **`categories[].count` follows `keyword`**, so every list request also
  runs a `GROUP BY category` over the filtered set. Combined with the
  scan above, list latency is dominated by the visible-set size, not the
  page size. Cache-friendly enough for now (idempotent GET, short
  windows).
- **`GET /mcps/mine` + `category`** hits `idx_owner_created` for the
  base filter, but narrows `category` via filesort/refilter. Acceptable
  while a single user has < ~200 personal MCPs. Later add
  `(owner_uid, category, created_at)` if the mine-page starts feeling
  slow.
- **Uniqueness** on `(owner_uid, space_id, name)` for live rows is enforced
  by a DB-level UNIQUE index over a STORED generated column
  `name_live = IF(deleted_at IS NULL, name, NULL)` — see migration
  `20260714-02-mcp-uniqueness.sql`. A duplicate live tuple fails with a
  MySQL duplicate-key error (1062), mapped to
  `err.marketplace.mcp.name_taken`; soft-deleted rows carry
  `name_live = NULL` so the name frees up after delete. An earlier
  `SELECT … FOR UPDATE` recipe was proven to DEADLOCK under concurrent
  creates (InnoDB gap-lock circular wait) and MUST NOT be used; the
  repository does a plain insert/update and maps the duplicate-key error.
- **Timestamps** are RFC 3339 ms in server-local timezone; if we ever
  add cross-region deploys, revisit UTC canonicalization.

## 8. Change management

- New fields must land in this doc first, then implementation.
- Renaming or removing a field is a breaking change; publish a `v2` doc
  and keep `v1` handlers alive until all clients migrate.
- Adding an optional field with a safe default is backward-compatible;
  clients ignore unknown fields.
- `SECRET_PLACEHOLDER_SENTINEL` (§0) is versioned with this doc.
  Changing its literal value is a breaking change for any deployed
  frontend/backend pair.

## 9. Admin surface

A separate, non-public path used by `octo-admin` (the platform admin
console) to create and list platform-provided (`visibility = system`) MCP
records. The public `/api/v1/mcps` endpoints continue to REJECT
`visibility = system` on write with
`err.marketplace.mcp.invalid_visibility` (§2) — the admin surface is the
ONLY path that can mint or manage system-visibility records.

Base path: **`/api/v1/admin/mcps`** (a subpath of the same `/api/v1`
namespace as the public surface, so the gateway `/market/*` prefix
rewrite handles both uniformly).

### 9.1 Auth

| Header | Value | Notes |
| --- | --- | --- |
| `X-Admin-Token` | Shared secret | Must match `MARKETPLACE_ADMIN_TOKEN` on the marketplace service (`crypto/subtle.ConstantTimeCompare`). Empty on the server side ⇒ admin routes are disabled by design. |

**No `token` / `X-Space-Id` required.** The middleware stamps a
server-configured admin identity (`ADMIN_OWNER_UID` / `ADMIN_OWNER_NAME`,
required in prod) into the request context so downstream handlers reuse
the same `callerFromContext` accessor as the public surface.

**Dev mode**: when the service runs with `AUTH_ENABLED=false`, the
`X-Admin-Token` check is bypassed. Combined with `ADMIN_OWNER_UID`
falling back to `DEV_AUTH_UID`, admin routes are usable end-to-end
without extra secret plumbing during local development.

### 9.2 Endpoints

**`POST /api/v1/admin/mcps`** — create a system MCP.

- Body: same shape as public `POST /mcps` (§4.1). Any `visibility` value
  in the body is silently overridden to `"system"`. `space_id` is
  always stored as NULL (system records are cross-Space).
- Response (201): full `McpDetail` (§3.1) with `visibility = "system"`,
  `creator_name` = the configured admin identity, and no space
  attribution on the wire.
- Errors: 400 `invalid_request` / `invalid_transport` /
  `secret_leaked` / `slug_invalid`; 401 `auth.admin_unauthorized`;
  409 `name_taken` / `slug_taken`
  (the `(owner_uid, space_id=NULL, name)` uniqueness constraint applies
  per §7).

**`GET /api/v1/admin/mcps`** — list system MCPs across all Spaces.

- Query: same `keyword` / `category` / `limit` / `offset` as public
  `GET /mcps` (§4.2). `category=all` disables the category filter.
- Response (200): `{items, total, categories}` envelope (§4.2), with
  the visibility rule collapsed to `visibility = 'system'`. `space_id`
  is never returned (system rows carry NULL).
- Errors: 401 `auth.admin_unauthorized`.

**`GET /api/v1/admin/mcps/{mcp_id}`** — fetch a system MCP detail.

- Response (200): full `McpDetail` (§3.1). A record whose visibility is
  not `system` collapses to 404 — the admin surface deliberately cannot
  peek at Space-scoped records by ID.
- Errors: 401 `auth.admin_unauthorized`; 404 `not_found`.

**`PATCH /api/v1/admin/mcps/{mcp_id}`** — update a system MCP.

- Body: same partial-update shape as public `PATCH /mcps/{mcp_id}` (§4.5).
  Any `visibility` in the body must be `"system"` (or omitted); other
  values are rejected 400 `invalid_visibility` — a PATCH cannot demote
  a system row to public/private (which would silently strip it from
  the admin list and grant it a fictitious Space).
- Ownership is not enforced on this path: any authenticated admin can
  edit any system MCP (contrast with public `PATCH` which is
  owner-only per §4.5).
- Response (200): the refreshed `McpDetail` (§3.1). Secret redaction
  rules from §5 apply to any touched `env` / `headers` entries.
- Errors: 400 `invalid_request` / `invalid_transport` /
  `invalid_visibility` / `secret_leaked` / `slug_invalid`; 401
  `auth.admin_unauthorized`; 404 `not_found`; 409 `name_taken` /
  `slug_taken`.

**`DELETE /api/v1/admin/mcps/{mcp_id}`** — soft-delete a system MCP.

- Same soft-delete semantics as public `DELETE /mcps/{mcp_id}` (§4.6):
  `deleted_at` is stamped; the record disappears from admin/public
  list responses and detail lookups.
- Ownership is not enforced (see PATCH note above).
- Response (204): empty body.
- Errors: 401 `auth.admin_unauthorized`; 404 `not_found`.

### 9.3 Error codes added by this surface

| HTTP | Code | When |
| --- | --- | --- |
| 401 | `err.marketplace.auth.admin_unauthorized` | Missing / mismatched `X-Admin-Token`, or the server was deployed without one (admin closed). |

All other error codes are shared with the public surface (§2). The admin
error deliberately lives in the `err.marketplace.auth.*` family, not a
standalone `err.marketplace.admin.*` namespace, so clients switch on one
family for every authentication failure.

### 9.4 Out of scope for v1 admin

- Bulk import / seed / migrate.
- Audit log; admin creates and updates land in the same
  soft-delete-friendly table as public rows but there is no immutable
  audit trail yet.
- Undelete / restore of a soft-deleted system MCP; today the only way
  back is a DB fixup.

### 9.5 Deployment guidance

- Marketplace MUST NOT be reachable from the public internet; front it
  with nginx / an internal load balancer that only accepts traffic from
  trusted origins (admin console + `/market/*` gateway rewrite).
- `MARKETPLACE_ADMIN_TOKEN` should be rotated on any suspicion of leak.
  Because `octo-admin` today ships this token to the browser via
  `import.meta.env.VITE_MARKETPLACE_ADMIN_TOKEN` (Vite build-time
  inline), treat it as a low-value shared secret: the network fence is
  the primary defense, the token is defense-in-depth.
- A future iteration will move admin writes behind octo-admin's own
  backend so the token never touches the browser.
