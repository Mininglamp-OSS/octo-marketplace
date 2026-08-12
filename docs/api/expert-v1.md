# Expert Marketplace API — v1

> Base path: **`/market/api/v1`**
> Owner: `octo-marketplace`
> Consumers: `octo-web` (`dmworkmcp` 专家市场 tab), later `octo-cli`
> Related brief: `.octospec/tasks/expert-catalog-v1/brief.md`
> Sibling contract: `docs/api/mcp-v1.md` (this doc mirrors its conventions)

This document is the authoritative behavior contract for the Expert Marketplace
CRUD slice of octo-marketplace. It covers two related-but-distinct entities —
**experts** (专家 / single agents) and **squads** (专家团 / expert teams). The
exact generated wire schema lives in `docs/openapi/swagger.yaml`; handler code,
tests, and client integration must stay aligned with both. Do not extend the
surface here without first updating this file and getting review sign-off.

---

## 0. Constants shared with the client

| Name | Value | Owner |
| --- | --- | --- |
| `CATEGORY_KEY_ALL` | `"all"` | Reserved category key that disables the category filter on the list endpoints. |
| `SECRET_PLACEHOLDER_SENTINEL` | `"__OCTO_SECRET_PLACEHOLDER__"` | Same literal as `mcp-v1.md` §0. Authors are encouraged to use it inside `mcp_config` rather than embedding real tokens. v1 stores `mcp_config` verbatim and does not redact (see §6). |

### Type alignment between wire and TS

Wire responses are a **superset** of the current
`packages/dmworkmcp/src/mock/expertMock.ts` shapes (`ExpertAgent`,
`ExpertSquad`, `ExpertMember`). Extra fields shipped by the server are silently
ignored by the frontend today. Intentional extras the frontend will adopt when
it consumes the API:

- List items and details carry `visibility`; TS today does not.
- Details carry `created_at` / `updated_at`; TS today does not.

The doc uses "superset" and never claims a 1:1 type match. The prototype's
`mine` flag and `checkResult` field are NOT wire fields: `mine` is derived
client-side from `owner_uid == caller`, and `checkResult` is a runtime
environment probe with no persisted value.

## 1. Auth

Identical to `mcp-v1.md` §1. All endpoints (except health probes under `/`)
require a valid Octo token.

| Header | Value | Notes |
| --- | --- | --- |
| `token` | Octo access token | Matches `octo-web`'s `WKApp.apiClient` convention. |
| `X-Space-Id` | Space UUID | Required on every endpoint; anchors visibility + ownership scope. |
| `Accept-Language` | e.g. `zh-CN, en;q=0.8` | Optional; forwarded to the token resolver. |

Server-side flow on every business request:

1. Resolve `token` → `Identity{uid, name}` via `internal/auth`. Failure →
   401 `AUTH_REQUIRED`.
2. Read `X-Space-Id`. Missing → 400 `VALIDATION_ERROR`.
3. Verify `uid` is a member of that Space via the authoritative Octo
   membership probe. Failure → 403 `FORBIDDEN`.
4. Never trust `owner_uid`, `space_id`, `creator_name`, or any identity field
   in the request body. These are stamped from steps 1–3, including the bot
   provenance triple (§3.1).

## 2. Error envelope

Every non-2xx response uses the standard OCTO OpenAPI error shape:

```json
{ "error": { "code": "NOT_FOUND", "message": "expert not found" } }
```

- `code` is the fixed OCTO wire enum (`tools/octo-api/references/api-spec.md`).
  Clients switch on `code`, not `message`.
- `message` is human-readable; never contains internal paths, credentials,
  SQL, or Go error strings.
- `details` / `hint` may appear inside `error` for validation failures.

### Error code catalog

| HTTP | Code (wire) | When |
| --- | --- | --- |
| 400 | `VALIDATION_ERROR` | Body fails structural validation: unknown field, bad visibility, malformed/oversized `mcp_config`, malformed `members`, empty required field. `error.details[]` may list offending fields. |
| 401 | `AUTH_REQUIRED` | Missing / invalid Octo token. |
| 403 | `FORBIDDEN` | Caller outside the requested Space, or not allowed to mutate the record. |
| 404 | `NOT_FOUND` | Record absent, or in a different Space (cross-Space discovery is closed). |
| 409 | `DUPLICATE` | Name collides with another live record owned by the caller in the Space. |
| 500 | `INTERNAL_ERROR` | Unclassified server error; details logged server-side only. |

Documentation-level reason labels (`err.marketplace.expert.*`) used in the
endpoint tables below map onto these wire codes; they are for humans reading the
doc, not additional wire enum values.

## 3. Resource shapes

Field names are `snake_case` on the wire and match the `octo-web`
`dmworkmcp` expert shapes where they overlap.

### 3.1 `ExpertSpec` — the shared "one expert" sub-schema

The trio that defines a single expert's behavior. It appears **twice**: at the
top level of an `ExpertAgentDetail` (§3.2), and inside every squad member
(§3.4). Implement it once on each side.

```json
{
  "instruction": "你是资深后端架构师……",
  "mcp_config": "{\n  \"mcpServers\": {\n    \"git\": { \"command\": \"npx\", \"args\": [\"-y\", \"@modelcontextprotocol/server-git\"] }\n  }\n}",
  "skills": [{"name": "架构评审清单"}, {"name": "容量估算模板"}]
}
```

- `instruction`: system prompt / role definition. Required (non-empty) — a
  blank value is rejected with `400 VALIDATION_ERROR`.
- `mcp_config`: the raw `mcpServers` config as a **JSON string** (exactly what
  the user typed in the config editor — see §6). Optional. Validated as
  well-formed JSON and size-capped on write; stored and returned verbatim.
- `skills`: on **read**, an array of skill objects
  `{name, has_content, can_download, file_name, file_size, files}` (the write
  shape differs — see below). A skill is a whole Agent-Skill package (a
  `.zip/.skill` containing `SKILL.md`) stored per-expert in object storage:
  - `has_content` (bool): a stored `SKILL.md` exists → fetch its text via the
    `skill_md` endpoint.
  - `can_download` (bool): the raw package is stored → get a presigned download
    URL via the `skill_download` endpoint.
  - `file_name` / `file_size`: the uploaded package's original name and byte size.
  - `files`: manifest of paths inside the package (for a bundled-file list).
  Empty array collapses to omitted.

  On **write**, each skill is one of two forms:
  - **Package upload** (installable, preferred):
    `{name, upload_object_key, file_name, file_size}`. First presign an upload
    (`POST /expert_skill_uploads`), `PUT` the raw `.zip/.skill` to the returned
    URL, then send its `upload_object_key` here. The server extracts `SKILL.md`,
    derives the authoritative `name` from its frontmatter, stores the package,
    and records the manifest. `upload_object_key` must be a key the upload-init
    endpoint minted (prefix `expert-uploads/`).
  - **Inline content** (legacy / name-only): `{name, content}` — the `SKILL.md`
    text directly, or just `{name}` for a name-only skill.

#### 3.1.1 Skill package upload / download

- `POST /expert_skill_uploads` — body `{file_name, file_size}` (`.zip`/`.skill`,
  ≤ 20 MiB). Returns `{upload_object_key, presigned_url, method, headers, expires_in}`.
  `PUT` the raw bytes to `presigned_url` with `headers`, then reference
  `upload_object_key` in a create/update skill (§4.1/§4.5).
- `GET /experts/{expert_id}/skill_md?i={index}` — returns `{content}`, the
  stored `SKILL.md` text for the skill at `index`. `404` when the skill has no
  stored content (`has_content=false`) or the index is out of range.
- `GET /squads/{squad_id}/skill_md?member={member_key}&i={index}` — the
  squad-member twin.
- `GET /experts/{expert_id}/skill_download?i={index}` — returns
  `{download_url}`, a short-lived presigned GET URL for the skill package at
  `index`. `404` when the skill has no stored package.
- `GET /squads/{squad_id}/skill_download?member={member_key}&i={index}` — the
  squad-member twin.

Object-key scheme (each stored skill gets a unique per-record folder, so a
PATCH never overwrites another skill's objects): the SKILL.md at
`experts/{id}/skills/{uuid}/SKILL.md` (member:
`squads/{id}/members/{mk}/skills/{uuid}/SKILL.md`) and the package at the
sibling `.../skill.zip`. Uploaded-but-not-committed packages live under
`expert-uploads/{upload_id}/…` and are deleted on commit; objects orphaned by a
PATCH are left for deferred GC (none in v1).

### 3.2 `ExpertAgentDetail`

Full record for `GET /experts/{expert_id}`, `POST /experts`,
`PATCH /experts/{expert_id}`.

```json
{
  "expert_id": "01HK7Z3B9YV0K5H0KR6QF8N4M2",
  "short_name": "架构",
  "name": "后端架构师",
  "summary": "评审服务边界、数据模型和可靠性方案。",
  "category": "研发工具",
  "tags": ["架构评审", "可靠性"],
  "publisher": "Octo Community",
  "visibility": "public",
  "creator_name": "王决",
  "created_by_type": "human",
  "instruction": "你是资深后端架构师……",
  "mcp_config": "{ \"mcpServers\": { \"git\": { … } } }",
  "skills": [{"name": "架构评审清单"}, {"name": "容量估算模板"}],
  "created_at": "2026-08-06T10:15:00.000+08:00",
  "updated_at": "2026-08-06T10:15:00.000+08:00"
}
```

Field notes:

- `expert_id`: server-generated, ULID-style opaque 26-char string. Opaque to
  clients; never derived from `name`.
- `short_name`: 1–4 char tile label. Server-derived from `name` (first 2
  characters) when the client omits it.
- `category`: the category **NAME** from the dedicated `expert_categories`
  table (§5) — e.g. `营销策划`. Stored as a `category_id`; the service resolves
  name → id on write and id → name on read. `""` or an unknown name is rejected
  on write with `VALIDATION_ERROR`.
- `tags`: string array of names; de-duplicated + trimmed server-side. Stored
  as tag ids against the `expert_tags` dictionary (§3.7) and resolved back to
  names on read.
- `publisher`: display org string. Defaults to the caller's workspace label.
- `visibility`: `public` / `private` / `system`. `system` never appears in a
  client write; it appears in reads for platform-provided records.
- `creator_name`: snapshot of the owner's `Identity.name` at create time.
- `created_by_type`: `human` / `bot` / `import`; always present. Semantics
  identical to `mcp-v1.md` §3.1. `created_by_bot_uid` / `created_by_bot_name`
  present only when `created_by_type == "bot"`.
- `instruction` / `mcp_config` / `skills`: the `ExpertSpec` (§3.1).
- `created_at` / `updated_at`: RFC 3339, millisecond precision, server-local
  timezone.

Server-only fields (never accepted from the client): `expert_id`, `owner_uid`
(never surfaced), `short_name` (derivable), `creator_name`, `created_by_type`,
`created_by_bot_uid`, `created_by_bot_name`, `created_at`, `updated_at`.
Request bodies are strict — unknown or server-owned fields yield
`VALIDATION_ERROR`.

### 3.3 `ExpertAgentListItem`

Projection for `GET /experts` and `GET /experts/mine`. Drops the heavy
`ExpertSpec` payload (`instruction` / `mcp_config` / `skills`) — those load on
detail. `skill_count` is the length of the dropped `skills` array (the squad
twin §3.6 carries `member_count` the same way).

```json
{
  "expert_id": "01HK7Z3B9YV0K5H0KR6QF8N4M2",
  "short_name": "架构",
  "name": "后端架构师",
  "summary": "评审服务边界、数据模型和可靠性方案。",
  "category": "研发工具",
  "tags": ["架构评审", "可靠性"],
  "publisher": "Octo Community",
  "visibility": "public",
  "creator_name": "王决",
  "created_by_type": "human",
  "skill_count": 2
}
```

### 3.4 `SquadMember`

One member inside a squad. An `ExpertSpec` (§3.1) plus team-role metadata.
Members are self-contained snapshots — NOT foreign keys into `experts` (see
brief). `template_id` is a plain string used only by the client-side install
prompt.

```json
{
  "member_key": "tech_lead",
  "template_id": "expert-tech-lead",
  "name": "技术负责人",
  "role": "拆解方案并调度成员",
  "is_leader": true,
  "instruction": "你是……",
  "mcp_config": "{ \"mcpServers\": {} }",
  "skills": [{"name": "架构评审清单"}]
}
```

- `member_key`: stable key used by the client install prompt to bind
  `memberKey → agentId`. Server fills a default `member_NN` when omitted.
- `template_id`: template identifier for the install prompt; free string,
  server fills `expert-{squad_id}-NN` when omitted. Never an FK.
- `name` / `role`: required. `is_leader`: exactly one member SHOULD be leader;
  the server normalizes `is_leader` to a single member — the first one flagged,
  or the first member when none is flagged.
- `instruction` / `mcp_config` / `skills`: the member's `ExpertSpec`.

> Precedence of `leader` vs `is_leader`: the boolean `is_leader` is the
> authoritative flag (normalized to exactly one member above); the top-level
> `leader` **string** is only the display name. When a write supplies both, the
> `leader` string is stored verbatim for display and may name a member other
> than the flagged one — clients that need the leader identity should read
> `is_leader`, not match the `leader` string.

### 3.5 `ExpertSquadDetail`

Full record for `GET /squads/{squad_id}`, `POST /squads`,
`PATCH /squads/{squad_id}`. Shares the generic marketplace fields with
`ExpertAgentDetail` and swaps the `ExpertSpec` for squad payload.

```json
{
  "squad_id": "01HK7Z3B9YV0K5H0KR6QF8N4M2",
  "short_name": "研发",
  "name": "软件研发交付团",
  "summary": "从需求澄清到开发测试的一条软件研发协作链路。",
  "category": "研发工具",
  "tags": ["需求分析", "前后端开发", "自动化测试"],
  "publisher": "Mininglamp-OSS",
  "visibility": "public",
  "creator_name": "林澈",
  "created_by_type": "bot",
  "created_by_bot_uid": "bot_01HZR…",
  "created_by_bot_name": "研发交付助手",
  "leader": "技术负责人",
  "strategies": [
    "技术负责人先澄清需求、范围和验收标准，再形成技术拆分。",
    "方案确认后并行调用前端与后端；两者共享同一份接口契约。"
  ],
  "dependencies": {
    "blocking": ["codex-runtime", "git-mcp"],
    "recommended": ["GPT-5.2 或同等能力模型"]
  },
  "permission": "读取工作区文件、创建专家配置、写入专家团关系",
  "members": [ /* SquadMember[] (§3.4), order preserved */ ],
  "created_at": "2026-08-06T10:15:00.000+08:00",
  "updated_at": "2026-08-06T10:15:00.000+08:00"
}
```

Field notes:

- `leader`: display name of the leader member. Server derives it from the
  member flagged `is_leader` when omitted.
- `strategies`: ordered string array; the dispatch rules. Empty → the client
  falls back to `DEFAULT_STRATEGIES` for display; the server stores whatever
  is sent.
- `dependencies`: `{blocking: string[], recommended: string[]}`.
- `permission`: free-text permission summary.
- `members`: `SquadMember[]`, order preserved. At least 1 member required on
  create; `VALIDATION_ERROR` otherwise.

### 3.6 `ExpertSquadListItem`

Projection for `GET /squads` and `GET /squads/mine`. Drops `strategies` /
`dependencies` / `permission` / `members`; adds a `member_count`.

```json
{
  "squad_id": "01HK7Z3B9YV0K5H0KR6QF8N4M2",
  "short_name": "研发",
  "name": "软件研发交付团",
  "summary": "…",
  "category": "研发工具",
  "tags": ["需求分析", "前后端开发"],
  "publisher": "Mininglamp-OSS",
  "visibility": "public",
  "creator_name": "林澈",
  "created_by_type": "bot",
  "member_count": 5
}
```

### 3.7 Tags storage

Tags are a per-Space dictionary (`expert_tags`, shared by both entities) plus a
`tags_json` array of tag **ids** on each row — the `skill_tags` +
`skills.tags` design, NOT MCP's inline free-form strings. On the **wire**, tags
are always string **names** (matching the frontend). On write the server upserts
each name into `expert_tags` for the caller's Space and stores the resulting
ids; on read it resolves ids back to names. `space_id = ''` denotes a global
tag. Clients never see tag ids.

## 4. Endpoints

Two symmetric families. `{entity}` below is `experts` or `squads`; behavior is
identical except for the payload shape (§3) and the resource id field name
(`expert_id` / `squad_id`).

### 4.1 `POST /experts` — create expert

Publish a new standalone expert owned by the caller.

**Request body** (flat; `ExpertAgentDetail` minus server-only fields):

```json
{
  "name": "后端架构师",
  "summary": "评审服务边界、数据模型和可靠性方案。",
  "category": "研发工具",
  "tags": ["架构评审", "可靠性"],
  "instruction": "你是资深后端架构师……",
  "mcp_config": "{ \"mcpServers\": { \"git\": { … } } }",
  "skills": [{"name": "架构评审清单"}, {"name": "容量估算模板"}]
}
```

- `name` and `summary` are required; every other field has a documented
  default. `category` must be a valid category NAME (§5).
- New records are always persisted `public`. A `visibility` field may be sent
  for compatibility but is ignored; `system` is rejected with
  `err.marketplace.expert.invalid_visibility`.
- `mcp_config` is validated per §6.

**Response (201):** the full `ExpertAgentDetail`.

**Errors:** 400 `err.marketplace.expert.invalid_request` /
`invalid_visibility` / `invalid_mcp_config` · 401
`err.marketplace.auth.unauthorized` · 403
`err.marketplace.auth.forbidden_space` · 409
`err.marketplace.expert.name_taken`.

### 4.2 `GET /experts` — list (Space-scoped)

Returns every expert visible to the caller in their current Space: all `system`
records, plus all `public` records in `X-Space-Id`, plus the caller's own
`private` records.

**Query parameters** (identical set for all four list endpoints):

| Name | Type | Default | Meaning |
| --- | --- | --- | --- |
| `keyword` | string | — | Case-insensitive substring match against `name`, `summary`, `category`, `creator_name`. Tags excluded (owned by the `tag` filter). |
| `category` | string (repeatable) | `all` | `category_id`; `all` disables the filter. Repeat or comma-separate to OR-combine. |
| `tag` | string (repeatable) | — | Tag-name filter; repeat / comma-separate to AND-combine. |
| `visibility` | string (repeatable) | — | `system` / `public` / `private`; OR-combine. |
| `created_by_type` | string (repeatable) | — | `human` / `bot` / `import`; OR-combine. |
| `sort` | string | — | `updated` → `updated_at DESC, id DESC`; `relevance` (with non-empty `keyword`) → weighted match; else default (creation-time DESC). |
| `page` | int | `1` | One-based page number. |
| `page_size` | int | `20` | Max `100`. |

> Note: `category` here is the category **id** (as returned by
> `GET /expert_categories` in `expert_category_id`), whereas in create/update/read
> **bodies** `category` is the category **NAME** (§5). Same field name, two
> representations — the list filter takes the id, the resource shape takes the name.
> `keyword` matching against `category` resolves the name through the taxonomy.

**Response (200):**

```json
{ "data": [ /* ExpertAgentListItem[] */ ],
  "pagination": { "total": 6, "page": 1, "page_size": 20 } }
```

`total` is the count after filters, before pagination. Default order: newest
first (`created_at DESC`, tie-broken by `id DESC`).

**Errors:** 401 / 403.

### 4.3 `GET /experts/mine`

Every expert owned by the caller in their Space, regardless of visibility
(including own `private`). Never leaks another user's rows. Same query params
and envelope as §4.2, restricted to `owner_uid == caller`.

**Errors:** 401 / 403.

### 4.4 `GET /experts/{expert_id}` — detail

Returns `ExpertAgentDetail` if visible: `visibility == system`, OR
(`space_id == X-Space-Id` AND (`public` OR `owner_uid == caller`)). Otherwise
`404 err.marketplace.expert.not_found` — never 403 — closing cross-Space
enumeration.

**Errors:** 401 / 403 (auth) / 404.

### 4.5 `PATCH /experts/{expert_id}` — update

Partial update; owner only (else `err.marketplace.expert.forbidden`).

- **Mutable:** `name`, `summary`, `category`, `tags`, `instruction`,
  `mcp_config`, `skills`. A `visibility` field is accepted but ignored,
  preserving current visibility.
- **Immutable:** `expert_id`, `owner_uid`, `space_id`, `creator_name`,
  `created_by_*`, `created_at` — rejected as unknown fields.

**Response (200):** updated `ExpertAgentDetail`.

**Errors:** 400 (`invalid_request` / `invalid_mcp_config`) · 401 · 403
(`forbidden_space` / `forbidden`) · 404 · 409 (`name_taken` on rename
collision).

### 4.6 `DELETE /experts/{expert_id}` — soft delete

Owner only. Row soft-deleted (`deleted_at = now()`); list + detail treat it as
gone; the name frees up for reuse. **Response (200):** `{"data":{}}`. **Errors:**
401 / 403 / 404.

### 4.6.1 `POST /experts/{expert_id}/install` — provision to a Loop workspace/runtime

Provision the expert as a Loop agent in the caller's chosen workspace/runtime.
The server, acting **as the calling user** (the octo `Token` is forwarded to
octo-fleet, which enforces workspace membership, runtime access, and space
scoping — this service does not re-check them):

1. resolves the **visible** expert (§4.4 visibility rule),
2. creates a Loop agent seeded with the expert's `instruction` + `mcp_config`,
3. creates one workspace skill per packaged skill and attaches its supporting
   files, then binds the skills to the agent.

On any partial failure it **rolls back** the agent and every skill it created
(best-effort, on a context detached from the request so cancellation still
cleans up). The whole install is bounded by an aggregate timeout, and the total
supporting-file fan-out is capped across all skills (a package exceeding it →
`400`).

**Request:**

```json
{ "workspace_id": "ws-123", "runtime_id": "rt-456" }
```

**Response (200):**

```json
{ "data": { "agent_id": "agent-789" } }
```

**Errors:** 400 (`invalid_request` — missing workspace/runtime, or an install
that exceeds the resource caps) · 401 · 403 · 404 (expert not visible) · 409
(fleet rejects a duplicate agent name in the workspace) · 500 · 503
(`UPSTREAM_UNAVAILABLE` when octo-fleet is unconfigured or unavailable).

### 4.7–4.12 `/squads` family

Identical verbs and semantics to §4.1–4.6, with the squad payload (§3.5/§3.6)
and `squad_id`:

- `POST /squads` — create. Body is the flat squad shape: generic fields +
  `leader` / `strategies` / `dependencies` / `permission` / `members`.
  `members` requires ≥ 1 entry; each member is validated as an `ExpertSpec` +
  role metadata (§3.4), including per-member `mcp_config` validation (§6).
  Malformed members → `err.marketplace.expert.invalid_members`.
  **Response (201):** `ExpertSquadDetail`.
- `GET /squads` / `GET /squads/mine` — same query params + envelope as §4.2/§4.3,
  returning `ExpertSquadListItem[]` (with `member_count`).
- `GET /squads/{squad_id}` — `ExpertSquadDetail`; same visibility rule as §4.4.
- `PATCH /squads/{squad_id}` — owner-only partial update. Mutable: generic
  fields + `leader` / `strategies` / `dependencies` / `permission` /
  `members`. Sending `members` **replaces** the whole array (full-replace, not
  merge) — the client always submits the complete member list, mirroring the
  octo-web squad editor. Immutable fields as §4.5.
- `DELETE /squads/{squad_id}` — soft delete; 200 `{"data":{}}`.
- `POST /squads/{squad_id}/install` — provision the squad into a Loop
  workspace/runtime. Body `{ "workspace_id", "runtime_id" }`. The server first
  installs **each member** as a Loop agent (create agent seeded with the
  member's instruction + `mcp_config`, create + bind its packaged skills), then
  **forms the squad** via octo-fleet: create it led by the leader member (first
  member flagged `is_leader`, else the one whose name matches the squad `leader`
  label, else the first member — fleet auto-adds the leader as a member) and
  attach the remaining members with their roles. Aggregates the fleet calls on
  behalf of the caller (forwarded token, so fleet enforces workspace membership
  — squad create requires owner/admin — runtime access, and space scoping) and
  **rolls back** the squad plus every provisioned member agent on any partial
  failure. **Response (200):** `{ "squad_id", "leader_agent_id" }`. Mirrors the
  single-agent expert flow `POST /experts/{expert_id}/install`. **Errors:** 400
  (`invalid_request`, incl. a squad with no members, or an install that exceeds
  the aggregate resource caps) · 401 · 403 · 404 · 409 (fleet rejects a duplicate
  agent/squad name in the workspace) · 500 · 503 (`UPSTREAM_UNAVAILABLE` when
  octo-fleet is unconfigured or unavailable).

  **Request:** `{ "workspace_id": "ws-123", "runtime_id": "rt-456" }` —
  **Response (200):** `{ "data": { "squad_id": "sq-1", "leader_agent_id": "agent-1" } }`.

**Errors:** same catalog as the experts family, with
`err.marketplace.expert.invalid_members` added on create/update.

### 4.13 `GET /expert_tags` — tag suggestions

Tags aggregated from records visible to the caller in the current Space, sorted
by descending row count (alphabetical tie-break). Mirrors `GET /mcp_tags`.

**Query parameters:**

| Name | Type | Default | Meaning |
| --- | --- | --- | --- |
| `kind` | string | `agent` | `agent` aggregates over `experts`; `squad` over `expert_squads`. The octo-web tag popover is per-tab, so it requests the current tab's kind. |
| `q` | string | — | Case-insensitive substring match on tag name; empty → all visible tags. |
| `limit` | int | `50` | Clamped to `[1, 100]`. |
| `mode` | string | — | `mine` restricts to caller-owned rows; absent → full visible set. |

**Response:**

```json
{ "data": [ { "name": "架构评审", "count": 3 }, { "name": "可靠性", "count": 2 } ] }
```

Visibility scope matches §4.2. Empty tags and soft-deleted rows excluded. No
pagination.

**Errors:** 401 / 403.

## 5. Categories

Experts and squads use a **dedicated `expert_categories` table** (migration
`20260806-01`) — separate from the shared skill `categories` table. On the wire,
`category` on every expert/squad shape is the category **NAME** (e.g. `营销策划`)
in BOTH directions; storage is a `category_id` (VARCHAR(36)) on
`experts` / `expert_squads`. The service resolves name → id on write (unknown or
empty name → `VALIDATION_ERROR`) and id → name on read.

The 6 seeded categories (id / name / lucide icon_key / sort_order):

| id | name | icon_key | sort_order |
| --- | --- | --- | --- |
| `marketing-planning` | 营销策划 | `Megaphone` | 1 |
| `content-creation` | 内容创作 | `PenLine` | 2 |
| `ad-delivery` | 广告投放 | `Target` | 3 |
| `data-insight` | 数据洞察 | `ChartColumn` | 4 |
| `office-efficiency` | 办公提效 | `BriefcaseBusiness` | 5 |
| `dev-tools` | 研发工具 | `Code2` | 6 |

### 5.1 `GET /expert_categories` — category chips

Returns every live category with the number of records **visible to the caller**
of the requested kind in the caller's Space, so the octo-web 专家市场 tab can
render category chips with live counts.

**Query parameters:**

| Name | Type | Default | Meaning |
| --- | --- | --- | --- |
| `kind` | string | `agent` | `agent` counts `experts`; `squad` counts `expert_squads`. |

**Response (200):**

```json
{ "data": [ { "expert_category_id": "marketing-planning", "name": "营销策划", "count": 3 } ] }
```

- `count` is the number of records of the kind visible to the caller — the same
  visible-set rule as the list endpoints (§4.2): `system OR (space + (public OR
  owner))`, soft-deleted excluded.
- ALL categories are returned (a category with no visible records reports
  `count: 0`), ordered by `sort_order`.

**Errors:** 401 / 403.


## 6. `mcp_config` handling

Applied on every write (`POST`, `PATCH`) for the expert top-level `mcp_config`
and for each squad member's `mcp_config`:

- **Validated as well-formed JSON.** A non-empty value that does not parse is
  rejected with `err.marketplace.expert.invalid_mcp_config`
  (`VALIDATION_ERROR`). Empty string / omitted is allowed (no MCP).
- **Size-capped** (v1: 64 KiB per config). Oversized → `VALIDATION_ERROR`.
- **Stored verbatim** as text (MEDIUMTEXT), preserving the author's exact
  formatting; returned byte-for-byte on read. The server does NOT reformat,
  reorder keys, parse into server descriptors, spawn any process, or fetch any
  URL. Treated purely as an opaque, untrusted config document.
- **No secret redaction in v1** (brief Out-of-scope). Authors are guided by the
  frontend to use `SECRET_PLACEHOLDER_SENTINEL` (§0) instead of real tokens. A
  later slice may add env-value blanking mirroring `mcp-v1.md` §5 once a real
  install flow exists.

## 7. Performance & limits (v1 posture)

Sized for prototype scale; revisit on scale metrics. Mirrors `mcp-v1.md` §7.

- **`GET` keyword search is a full scan** over the caller's visible set
  (`name`/`summary` `LIKE %kw%`, non-sargable). Fine while a Space has
  < ~10 k records per entity; beyond that add FULLTEXT or a sidecar index.
- **Uniqueness** on `(owner_uid, space_id, name)` for live rows in each entity
  table is enforced by a DB-level UNIQUE index over a STORED generated column
  `name_live = IF(deleted_at IS NULL, name, NULL)`. A duplicate live tuple
  fails with MySQL duplicate-key (1062), mapped to `DUPLICATE`; soft-deleted
  rows carry `name_live = NULL` so the name frees up after delete. The
  `SELECT … FOR UPDATE` pre-check recipe is FORBIDDEN (proven to deadlock under
  concurrency — see `mcp-v1.md` §7); the repository does a plain insert/update
  and maps the duplicate-key error.
- **"我的" spans two entity tables** → two queries; the frontend renders them
  in separate sections so no merge/interleave is needed.
- **Timestamps** are RFC 3339 ms in server-local timezone.

## 8. Change management

- New fields land in this doc first, then implementation.
- Renaming / removing a field is breaking: publish a `v2` doc and keep `v1`
  handlers alive until clients migrate.
- Adding an optional field with a safe default is backward-compatible.
- `ExpertSpec` (§3.1) is a shared shape; a change to it affects both the expert
  top level and squad members — version both together.

## 9. Admin surface

A separate SuperAdmin surface manages platform-provided (`visibility = system`)
records — mirroring `mcp-v1.md` §9. The public surface still REJECTS
`visibility = system` on write.

Implemented endpoints (all under `/api/v1/admin`, gated by
`AdminAuthenticator`; wire shapes reuse §3, and generated OpenAPI under
`docs/openapi/` is the field-level reference — tag `admin_expert`):

- `POST/GET /admin/experts`, `GET/PATCH/DELETE /admin/experts/{expert_id}` —
  create stamps `visibility = system` (a client-sent `visibility` is rejected
  as an unknown field); list bypasses Space scoping and pages by
  `page`/`page_size`; create/patch bodies are §4.1/§4.5 shapes.
- `GET /admin/experts/{expert_id}/skill_md?i=N` — stored SKILL.md text of the
  skill at index `i` (§3.1 `SkillContentResp`).
- The same six verbs for squads under `/admin/squads`, plus
  `GET /admin/squads/{squad_id}/skill_md?member=<member_key>&i=N`.
- `GET/POST /admin/expert_categories`,
  `PATCH/DELETE /admin/expert_categories/{category_id}` — taxonomy management;
  delete is rejected 409 with the reference count while records still use the
  category. NOTE: update writes all three columns (`name`, `icon_key`,
  `sort_order`) — clients must echo the current `icon_key` back or it is
  cleared.
- `GET /admin/expert_tags?kind=agent|squad` — tag aggregation over system rows.
- `POST /admin/expert_skill_uploads` — the §5 presigned-upload handshake,
  reused verbatim.
