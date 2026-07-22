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
  "created_by_type": "human",
  "quick_start": {
    "transport": "streamable-http",
    "server_name": "GitHub MCP",
    "slug": "github-mcp",
    "url": "https://mcp.deepminer.com.cn/github/mcp",
    "auth_type": "none",
    "headers": { "X-Trace-Origin": "octo-web", "Authorization": "" },
    "headers_user_supplied": ["Authorization"]
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
- `created_by_type`: one of `human` / `bot` / `import`. Always present. `human`
  is stamped for user-token creates and every legacy row (pre-#894). `bot` is
  stamped when the create request was made with a Bot token — the middleware
  collapses the Bot into its owner Identity for authorization, so `owner_uid` /
  `creator_name` describe the owner user regardless. `import` is reserved for
  the Git-import path (#867) and not written today. Frontends use this value
  purely as a market badge — no permission behaviour derives from it.
- `created_by_bot_uid` / `created_by_bot_name`: present only when
  `created_by_type == "bot"`. `_uid` is the Bot's identity; `_name` is a
  snapshot at create time so the market badge stays intact after the Bot is
  renamed or deleted. Both fields are omitted from human-created rows.
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
- `quick_start.headers` / `quick_start.env`: values under keys listed in
  the companion `*_user_supplied` arrays are stored verbatim but blanked
  to non-owners at read time (§5.1 / §5.3); shared values also persist
  verbatim on any visibility and are similarly blanked to non-owners.
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
  "creator_name": "GitHub Bot",
  "created_by_type": "bot",
  "created_by_bot_uid": "bot_01HZR…",
  "created_by_bot_name": "GitHub Autoposter"
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
| `env` | `quick_start.env` | Record. Empty record collapses to omitted. Values under keys named in `env_user_supplied` are stored verbatim for the owner and blanked to non-owners at read time (§5.1 / §5.3). |
| `env_user_supplied` | `quick_start.env_user_supplied` | String array. Lists env keys whose value each consumer fills locally (§5). Empty array collapses to omitted. |
| `headers` | `quick_start.headers` | Record. Empty record collapses to omitted. Values under keys named in `headers_user_supplied` are stored verbatim for the owner and blanked to non-owners at read time (§5.1 / §5.3). |
| `headers_user_supplied` | `quick_start.headers_user_supplied` | String array. Same semantics as `env_user_supplied` for headers. |
| `auth_type` | `quick_start.auth_type` | Default `"none"`. Metadata only; does NOT gate any server behaviour under the toggle model (§5.2). |
| `slug` | `quick_start.slug` | Client sends flat; server echoes nested. Auto-derived from `name` when omitted. See field notes above. |
| *server-derived* | `quick_start.server_name` | Server sets to `name.trim()`. Not accepted from client. |

Top-level fields (`name`, `slug`, `slogan`, `category`, `icon`, `tags`,
`tools`, `usage_examples`, `faqs`, `notes`, `visibility`) round-trip 1:1
between write and read shapes.

Fields set by the server, never by the client:
`mcp_id`, `owner_uid` (server-only, never surfaced), `creator_name`, `tool_count`,
`created_by_type`, `created_by_bot_uid`, `created_by_bot_name`,
`created_at`, `updated_at`, `quick_start.server_name`. Request bodies are
strict: client-supplied server fields or any other unknown field are rejected
with `VALIDATION_ERROR`.

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
  "auth_type": "none",
  "headers": { "X-Trace": "web", "Authorization": "" },
  "headers_user_supplied": ["Authorization"],
  "command": null,
  "args": [],
  "env": {},
  "env_user_supplied": [],
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
| `keyword` | string | — | Case-insensitive substring match against `name`, `slogan`, `category`, `creator_name`, each entry of `tags`, `tools[].name`, `tools[].description`, and `usage_examples`. |
| `category` | string (repeatable) | `all` | Category key; `all` disables the filter. Repeat or comma-separate (`?category=dev,search`) to OR-combine. |
| `tag` | string (repeatable) | — | Tag filter; repeat or comma-separate to OR-combine. |
| `transport` | string (repeatable) | — | Transport filter (`stdio` / `sse` / `streamable-http`); repeat or comma-separate to OR-combine. |
| `visibility` | string (repeatable) | — | Visibility filter (`system` / `public` / `private`); repeat or comma-separate. Absent → no filter (still bounded by the visible-set rule above). |
| `source` | string (repeatable) | — | Source facet (`system` / `space` / `mine`). Predicates partition the set the same way the response's `source` label does — a caller-owned row is labeled `mine`, not `space`. |
| `created_by_type` | string (repeatable) | — | Provenance filter. Accepts `human` / `bot` / `import`. Repeat or comma-separate to OR-combine. Absent → no filter. |
| `sort` | string | — | Ranking selector. `relevance` (only meaningful together with a non-empty `keyword`) orders by a fixed weighted match score against every searchable field; any other value falls back to the default order. |
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
- Default order: newest first (`created_at DESC`, tie-broken by `id DESC`).
  Pass `sort=relevance` together with a non-empty `keyword` to switch to the
  weighted match score; any other `sort` value falls back to the default.

**Errors:** 401 / 403.

### 4.3 `GET /mcps/mine` — my MCPs

Returns every record owned by the caller in their current Space,
regardless of visibility (including their own `private`). Never leaks
anything owned by another user.

**Query parameters:** same as `GET /mcps` (`keyword`, `category`, `tag`,
`transport`, `visibility`, `source`, `created_by_type`, `sort`, `page`,
`page_size`).

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

## 5. Secret handling

Applied on every write (`POST`, `PATCH`) BEFORE persistence.

### 5.1 `config.env` / `config.headers` and their `*_user_supplied` companions

Each write body carries two independent arrays alongside the value maps:

- `env_user_supplied` — env keys whose value each consumer must fill locally.
- `headers_user_supplied` — same, for headers.

For each entry `(k, v)` in `env` or `headers`, the value is persisted
verbatim — irrespective of `visibility` or whether `k` is in the companion
`*_user_supplied` array. The `*_user_supplied` array is echoed unchanged
so the frontend can rebuild its per-row toggle state on read.

Non-owner reads are the single defense line: `detailForCaller` (§5.3)
blanks EVERY value in `config.env` / `config.headers` before returning
the record to anyone other than the owner. This means the author can
persist a shared secret under a public record — but only the owner ever
sees it via the API; consumers see an empty map value and are expected
to install through their own path.

Security posture: rule 1 (must_be_empty on user-supplied) and rule 2
(public_secret_disallowed on secret-shaped shared keys) have both been
removed in this revision. The `secret_leaked` error code and its
`must_be_empty` / `public_secret_disallowed` detail reasons are no
longer emitted by the server. Owner-scoped blanking in §5.3 is the
sole guard keeping author tokens out of consumer-facing responses;
any change to `detailForCaller` must preserve that invariant.

Legacy note: the empty string and `SECRET_PLACEHOLDER_SENTINEL` (§0)
remain valid submissions and are stored verbatim (frontends that predate
the relaxation continue to work). `entriesFromWire` on the client
normalizes the sentinel back to "" for display.

### 5.2 `auth_type`

Metadata only. Under the toggle model the "consumer fills a Bearer token"
signal is expressed by adding `Authorization` to `headers_user_supplied`,
not by setting `auth_type`. The field is still accepted on write and
echoed on read for backwards-compat display in card / detail badges;
it does NOT gate any server behaviour and does NOT cause the
`Authorization` header to be stripped or synthesised.

- `auth_type: "none"` is the default; when the field is absent or empty
  the server writes `"none"`.
- `auth_type: "bearer"` is accepted but has no side effect.

### 5.3 Response side

The `env` / `headers` maps and their `*_user_supplied` companions are
returned verbatim to the owner — including any value the owner persisted
under a `*_user_supplied` key (see §5.1 rule 1). Non-owner reads of a
public record blank map values (`config.env` / `config.headers`) as a
read-side defence — callers see keys and structure but not values, and
the shared-value snippet flow expects each consumer to install through
their own path.

This blanking is the ONLY line of defense keeping author-persisted
values under `*_user_supplied` keys from leaking to non-owners; the
write path no longer forces those values to empty (see §5.1 rule 1).
Any code touching `detailForCaller` / equivalent must preserve this
invariant.

## 6. Examples

### 6.1 Create → returns detail

```http
POST /market/api/v1/mcps HTTP/1.1
token: <opaque>
X-Space-Id: 3fa85f64-5717-4562-b3fc-2c963f66afa6
Content-Type: application/json

{"name":"Slack MCP","slug":"slack-mcp","category":"productivity",
 "transport":"streamable-http","url":"https://mcp.example.com/slack",
 "auth_type":"none",
 "headers":{"Authorization":""},"headers_user_supplied":["Authorization"],
 "visibility":"public","tools":[]}
```

```http
HTTP/1.1 201 Created
Content-Type: application/json

{"mcp_id":"01HK7Z3B9YV0K5H0KR6QF8N4M2","name":"Slack MCP","slogan":"",
 "category":"productivity","icon":"","tags":[],"tool_count":0,
 "visibility":"public","creator_name":"李世超",
 "quick_start":{"transport":"streamable-http","server_name":"Slack MCP",
               "slug":"slack-mcp",
               "url":"https://mcp.example.com/slack","auth_type":"none",
               "headers":{"Authorization":""},
               "headers_user_supplied":["Authorization"]},
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

### 6.3 User-supplied key accepts any value (owner-visible reference)

Post §5.1-relaxation, values under `*_user_supplied` keys are persisted
verbatim and echoed back to the owner. Non-owner reads are blanked by
§5.3. The sentinel is still accepted for backwards compat and normalized
to `""` on storage.

Accepted — sentinel input, stored as `""`:

```http
POST /market/api/v1/mcps
{"name":"x","transport":"stdio","command":"npx",
 "env":{"GITHUB_TOKEN":"__OCTO_SECRET_PLACEHOLDER__"},
 "env_user_supplied":["GITHUB_TOKEN"],
 "visibility":"private"}
```

```http
HTTP/1.1 201 Created
… env.GITHUB_TOKEN persisted as "" …
```

Accepted — real value input, stored verbatim; owner will see it on their
own detail read; a non-owner GET of this record (if it were public) sees
`env.GITHUB_TOKEN: ""` per §5.3.

```http
POST /market/api/v1/mcps
{"name":"x","transport":"stdio","command":"npx",
 "env":{"GITHUB_TOKEN":"ghp_realAuthorToken"},
 "env_user_supplied":["GITHUB_TOKEN"],
 "visibility":"private"}
```

```http
HTTP/1.1 201 Created
… env.GITHUB_TOKEN persisted as "ghp_realAuthorToken" …
```

### 6.4 Public visibility accepts a shared secret-shaped value

Accepted — public record persisting an `Authorization` header inline;
non-owner reads see `headers.Authorization: ""` per §5.3 blanking:

```http
POST /market/api/v1/mcps
{"name":"x","transport":"streamable-http","url":"https://x",
 "headers":{"Authorization":"Bearer sk-live-abc"},
 "visibility":"public","tools":[]}
```

```http
HTTP/1.1 201 Created
… stored verbatim; owner-scoped read returns the real value, others get "" …
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
| `Token` | Octo session token | Same session token the user's browser holds for the public surface (`Authorization: Bearer <token>` is also accepted). Marketplace verifies it against octo-server's `/v1/auth/verify?include=context` and requires the returned identity to carry `role == "superAdmin"`. |

**No `X-Space-Id` required.** Admin routes operate globally — the middleware
resolves the caller's SuperAdmin identity and stamps it into the request
context so downstream handlers reuse the same `callerFromContext` accessor as
the public surface. `creator_name` / `owner_uid` on newly created system MCPs
reflect the real SuperAdmin user, not a synthetic account.

**Dev mode**: when the service runs with `AUTH_ENABLED=false`, the token
resolve + role check are bypassed. `DEV_AUTH_UID` / `DEV_AUTH_NAME` (or a
fallback `admin`/`Admin`) are stamped so admin routes work end-to-end without
octo-server during local development.

### 9.2 Endpoints

**`POST /api/v1/admin/mcps`** — create a system MCP.

- Body: same shape as public `POST /mcps` (§4.1). Any `visibility` value
  in the body is silently overridden to `"system"`. `space_id` is
  always stored as NULL (system records are cross-Space).
- Response (201): full `McpDetail` (§3.1) with `visibility = "system"`,
  `creator_name` = the configured admin identity, and no space
  attribution on the wire.
- Errors: 400 `invalid_request` / `invalid_transport` /
  `slug_invalid`; 401 `auth.admin_unauthorized`;
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
  `invalid_visibility` / `slug_invalid`; 401
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
| 401 | `AUTH_REQUIRED` | Missing `Token` header or the token was rejected by octo-server. |
| 403 | `FORBIDDEN` | Token resolved but the caller's `role` is not `superAdmin`. |
| 503 | `UPSTREAM_UNAVAILABLE` | octo-server could not verify the token (network or upstream error). |

Endpoint tables above cite `auth.admin_unauthorized` from the previous
`X-Admin-Token` design; new deployments will see the standard `AUTH_REQUIRED`
/ `FORBIDDEN` codes for these cases.

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
- Admin access is gated by the caller's `superAdmin` role in octo-server;
  revoke a compromised SuperAdmin account in octo-server to cut off
  marketplace admin at the same time.
