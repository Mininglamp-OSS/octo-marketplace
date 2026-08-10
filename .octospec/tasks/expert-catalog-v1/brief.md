---
type: Task
title: "Task: expert-catalog-v1"
description: First slice of Expert Marketplace persistence and CRUD API for octo-web — standalone experts (agents) and expert squads.
tags: ["architecture", "api", "persistence"]
timestamp: 2026-08-06T00:00:00+08:00
slug: expert-catalog-v1
source: self
---

# Task: expert-catalog-v1

## Goal

Deliver a first, load-bearing Expert Marketplace on top of the scaffold,
mirroring `mcp-catalog-v1`: users can publish, browse, inspect, edit, and
delete two related-but-distinct catalog entities that the `octo-web`
`dmworkmcp` package (专家市场 tab) already renders against static mocks:

- **Expert** (专家 / agent) — a single expert defined by an `instruction`
  (system prompt), an `mcp_config` (raw `mcpServers` JSON), and a set of
  uploaded `skills`.
- **Expert Squad** (专家团 / squad) — a container of independently-defined
  member experts plus a dispatch strategy (`leader`, `strategies`,
  `dependencies`, `permission`, `members`).

No installation, no versioning, no audit, no ratings — that is later work. The
copy-to-agent install prompt stays a pure frontend concern (`buildExpertPrompt`
in octo-web); the backend ships no prompt endpoint.

## Design decisions (settled before this brief)

- **Two entity tables, not one.** Agents and squads share only generic
  marketplace metadata (name/summary/category/tags/owner/visibility/
  timestamps); their payloads do not overlap. The octo-web UI shows them in
  separate tabs (专家 / 专家团 / 我的) and never needs a single interleaved
  paginated feed, so there is no reason to force a `kind`-discriminator single
  table (which would leave half the columns NULL per row). The only cost is
  that the "我的" page and any cross-entity count run two queries — cheap,
  because the UI already renders the two in separate sections.
- **Members are standalone, not references.** A squad member is NOT a foreign
  key into `experts`. When a user builds a squad they define each member expert
  inline; those definitions are snapshotted into `expert_squads.members_json`.
  `template_id` inside a member is a plain string used only to generate the
  install prompt (client-side) — never an FK.
- **`ExpertSpec` is a shared sub-schema.** The trio `instruction` +
  `mcp_config` + `skills` defines "one expert". A standalone expert carries it
  at the row's top level; each squad member carries it inside its
  `members_json` entry (plus `member_key` / `template_id` / `role` /
  `is_leader`). Both sides implement one shape.
- **Categories use a dedicated `expert_categories` taxonomy** (migration
  `20260806-01-expert-categories.sql`), seeded with the octo-web prototype's 6
  categories, exposed via `GET /expert_categories` with a visible-record count
  per category. This **supersedes** the earlier "reuse the shared `categories`
  table" plan: the expert catalog needs per-category counts scoped to the
  caller's visibility and a stable, expert-owned category set the prototype's 6
  fixed chips map onto. Trade-off (accepted): expert category ids are a separate
  namespace from skill/MCP category ids and do not line up across catalogs.
  `category` is carried as the NAME on the wire and resolved to/from the stored
  id (doc §5).
- **Tags follow the Skill pattern, not the MCP pattern.** A per-Space
  `expert_tags` dictionary table (shared by agents and squads) plus a
  `tags_json` array of tag ids on each row. This is the `skill_tags` +
  `skills.tags` design (migrations `20260717-02` / `20260721-00`), not MCP's
  inline free-form `tags_json` of strings.

## Load-bearing behavior

- Persistent expert + squad records in the independent Marketplace MySQL
  schema (three new tables + reuse of `categories`):
  - `experts` — standalone experts. Adds `instruction TEXT`,
    `mcp_config JSON`, `skills_json JSON` to the shared marketplace columns.
  - `expert_squads` — squads. Adds `leader`, `strategies_json`,
    `dependencies_json`, `permission`, `members_json`.
  - `expert_tags` — per-Space tag dictionary shared by both entities
    (`(id, space_id, name, created_by, …)`, PK `(space_id, name)`).
- Two HTTP surfaces under `/market/api/v1`, each mirroring the six MCP verbs:
  - `POST /experts` · `GET /experts` · `GET /experts/mine` ·
    `GET /experts/{id}` · `PATCH /experts/{id}` · `DELETE /experts/{id}`
  - `POST /squads` · `GET /squads` · `GET /squads/mine` ·
    `GET /squads/{id}` · `PATCH /squads/{id}` · `DELETE /squads/{id}`
  - `GET /expert_tags` — tag suggestions; `kind=agent|squad` selects which
    entity's visible rows to aggregate (mirrors `GET /mcp_tags`).
  - `GET /expert_categories` — the dedicated expert taxonomy with a
    visible-record count per category; `kind=agent|squad` selects which entity
    to count (see the taxonomy decision above).
- Visibility model identical to MCP (§4 of `docs/api/expert-v1.md`):
  `public` / `private` / `system`. New records are always `public`;
  `system` is admin-only and not settable through the public API.
- Identity resolution is always server-side via `internal/auth`; any
  `owner_uid`, `space_id`, or `creator_name` in a request body is ignored.
  Bot provenance (`created_by_type` / `created_by_bot_uid` /
  `created_by_bot_name`) is stamped from the token exactly as MCP does.
- Space membership is checked on every read and write that touches a
  Space-scoped record; failure returns a generic forbidden/not-found without
  disclosing existence across Spaces.
- Uniqueness on `(owner_uid, space_id, name)` for live rows in EACH entity
  table, enforced by a DB-level UNIQUE index over a STORED generated column
  `name_live = IF(deleted_at IS NULL, name, NULL)` — the exact non-deadlocking
  recipe proven in `mcp-catalog-v1` (see `docs/api/mcp-v1.md` §7). A duplicate
  live tuple maps to the `DUPLICATE` wire code.
- Structured error envelope with the fixed OCTO wire enum
  (`VALIDATION_ERROR` / `AUTH_REQUIRED` / `FORBIDDEN` / `NOT_FOUND` /
  `DUPLICATE` / `INTERNAL_ERROR`), plus documentation-level
  `err.marketplace.expert.*` reason labels in the API doc.
- `mcp_config` is treated as hostile input: validated as well-formed JSON and
  size-capped, stored verbatim, NEVER parsed to spawn a server and NEVER
  executed. Authors are guided (frontend) to use the
  `SECRET_PLACEHOLDER_SENTINEL` rather than embed real tokens; the backend
  does not attempt to redact inside arbitrary JSON in v1 (see Out of scope).
- Data model matches the octo-web payload shapes in
  `packages/dmworkmcp/src/mock/expertMock.ts` (`ExpertAgent`, `ExpertSquad`,
  `ExpertMember`). Wire responses are a **superset** of the current TS types
  (extra `visibility` / `created_at` / `updated_at`, per the MCP precedent).

## Out of scope

- Expert / squad installation, execution, or process supervision. Marketplace
  is a registry only; the install prompt is generated client-side.
- Versioning, publish/draft state, immutable release artifacts. (The octo-web
  prototype has already removed the version concept entirely.)
- ~~Real Skill file upload / parse pipeline for `skills_json`.~~ **Now in
  scope.** v1 ships whole-package skill upload (presigned
  `POST /expert_skill_uploads`), synchronous server-side `SKILL.md` extraction
  (`internal/service/parse`), and presigned download — see doc §3.1/§3.1.1,
  superseding the original plan to defer this and store only `{name, file_key?}`.
  An async parse queue remains out of scope; extraction runs inline on write.
- Secret redaction inside `mcp_config`. v1 validates JSON well-formedness and
  size only; a later slice can add env-value blanking mirroring MCP §5 once a
  real install flow needs it.
- Admin surface for `system` experts/squads (a follow-up brief, mirroring
  MCP §9).
- Search relevance tuning, ratings, reviews, telemetry, install counts.
- CLI synchronization endpoints.
- Cross-entity unified feed / single-table redesign.

## Dependencies

- **Shared `categories` table + endpoint.** Experts reuse the existing
  `categories` table and its list endpoint verbatim; no new category storage.
  The octo-web prototype's 6 hardcoded categories (营销策划 / 内容创作 /
  广告投放 / 数据洞察 / 办公提效 / 研发工具) must be remapped to the canonical
  seeded taxonomy (办公效率 / 内容创作 / 开发编程 / 数据分析 / 设计多媒体 /
  AI Agent / 知识管理 / 商业运营 / …). The exact mapping is agreed as part of
  the client-wiring PR, not this backend task; the backend simply stores a
  `category_id` FK.
- **Space membership authority.** Same dependency as `mcp-catalog-v1`: relies
  on the `internal/auth` Space-membership probe already introduced there.
- **Tag id storage pattern.** Reuses the `skill_tags` design (dictionary +
  `tags_json` array of ids). The repository layer for tag resolution mirrors
  `internal/repository/skill/tags.go`.
- **Client contract match.** `octo-web` `dmworkmcp` will add an
  `expertService` seam (mirroring `mcpService.ts` `USE_MOCK`) that consumes
  these endpoints. Field names in `docs/api/expert-v1.md` must match the
  `ExpertAgent` / `ExpertSquad` shapes so the frontend flips `USE_MOCK=false`
  without renames.
- **Base path.** External/canonical prefix `/market/api/v1` (matching MCP and
  Skill); the `/market` segment is stripped by the dev proxy / prod gateway, so
  the gin router mounts these routes under `/api/v1` (see `internal/api/router`).

## Acceptance

- `docs/api/expert-v1.md` fully specifies both entity surfaces (12 endpoints)
  + `GET /expert_tags`, the shared `ExpertSpec` sub-schema, the error
  envelope, every error code, and one example request/response per endpoint.
- New migrations create `experts`, `expert_squads`, and `expert_tags` with
  their indexes and the `name_live` uniqueness generated column; migration
  round-trip (Up/Down) passes on a clean MySQL 8 instance via the existing
  `internal/db` migration tests.
- Handlers, service, and repository layers implement the API doc exactly.
  `internal/api/router` remains the only place that maps URLs.
- Every mutating handler pulls identity from the token, ignores client-supplied
  identity, and denies cross-owner mutation with `FORBIDDEN`.
- Every read handler enforces Space scope and returns `NOT_FOUND` (never a
  leaky 403) when a record exists in a different Space.
- `GET /experts/mine` and `GET /squads/mine` return only `owner_uid == caller`
  rows in the caller's Space; deleted rows excluded.
- Cross-Space negative tests: a member of Space A cannot read, update, or
  delete a `public` expert/squad from Space B, nor discover it exists.
- `mcp_config` write validation: malformed JSON and oversized payloads are
  rejected with `VALIDATION_ERROR`; well-formed JSON round-trips verbatim.
- Squad `members_json` round-trips the full `ExpertSpec` per member
  (instruction / mcp_config / skills) plus `member_key` / `template_id` /
  `role` / `is_leader`, preserving order.
- `go test ./...`, `go build ./...`, `docker compose config` pass;
  `make fmt` / `make vet` / `make lint` clean; `make openapi-check` passes and
  generated OpenAPI matches the handlers.

## Non-goals for this brief

- Seeding `system` experts/squads (follow-up admin brief).
- Real skill upload/parse integration and `mcp_config` secret redaction
  (gated by a future install flow).
- Frontend changes — tracked in `octo-web` under a separate branch
  (`expertService` seam + category remap).
