---
type: Task
title: "Task: mcp-catalog-v1"
description: First slice of MCP catalog persistence and CRUD API surface for octo-web and octo-cli.
tags: ["architecture", "api", "persistence"]
timestamp: 2026-07-14T00:00:00+08:00
slug: mcp-catalog-v1
source: self
---

# Task: mcp-catalog-v1

## Goal

Deliver a first, load-bearing MCP catalog on top of the scaffold: users can
publish, browse, inspect, edit, and delete MCP server descriptors that
downstream clients (`octo-web` market page, later `octo-cli`) can rely on. No
installation, no versioning, no audit — that is later work.

## Load-bearing behavior

- Persistent MCP records in an independent Marketplace MySQL schema.
- One HTTP surface under `/market/api/v1/mcps` covering:
  - `POST   /mcps`             — create (owner = caller from Octo token)
  - `GET    /mcps`             — list visible-to-caller inside current Space
  - `GET    /mcps/mine`        — list owned-by-caller across the caller's Space
  - `GET    /mcps/{id}`        — detail
  - `PATCH  /mcps/{id}`        — partial update (owner only)
  - `DELETE /mcps/{id}`        — soft delete (owner only)
- Visibility model with three scopes:
  - `public`  — visible to every member of the owning Space; only the owner
    can mutate.
  - `private` — visible only to the owner in their own Space.
  - `system`  — visible from every Space; **not settable via public API in
    v1**. Reserved for later platform-provisioned or admin-managed
    records.
- Identity resolution is always server-side via the existing Octo token
  resolver (`internal/auth`). Any `owner_uid`, `space_id`, or `creator_name`
  in a request body is ignored.
- Space membership check happens on every read and write that touches a
  Space-scoped record; failure returns a generic forbidden error without
  disclosing whether the record exists.
- Structured error envelope with stable `err.marketplace.mcp.*` and
  `err.marketplace.auth.*` codes (see `docs/api/mcp-v1.md`).
- Shared `SECRET_PLACEHOLDER_SENTINEL` constant
  (`"__OCTO_SECRET_PLACEHOLDER__"`) is the ONLY string, besides empty, that
  the backend treats as an intentional "no secret" for token-like env or
  header values. The frontend renders a localized label to the user but
  submits the ASCII sentinel back, so an English-locale user does not
  trip `secret_leaked` because their placeholder copy differs from the
  Chinese one.
- Uniqueness on `(owner_uid, space_id, name)` for live records is enforced
  by a real DB-level UNIQUE constraint (migration
  `20260714-02-mcp-uniqueness.sql`): a STORED generated column
  `name_live = IF(deleted_at IS NULL, name, NULL)` plus a UNIQUE index over
  `(owner_uid, space_id, name_live)`. A duplicate live tuple fails with
  MySQL duplicate-key (1062), mapped to `err.marketplace.mcp.name_taken`;
  soft-deleted rows carry `name_live = NULL` (many NULLs allowed) so the
  name is reusable after delete. An earlier `SELECT … FOR UPDATE`
  service-layer recipe was PROVEN to deadlock under concurrency
  (InnoDB gap-lock circular wait) by
  `internal/repository/mcp_test.go:TestConcurrentCreateSameName` and MUST
  NOT be used — see `docs/api/mcp-v1.md` §7 and the migration files.
- Secret redaction on write:
  - `config.env` and `config.headers` values are stripped for keys matching
    the token/secret pattern; only the keys survive as metadata.
  - `Authorization: Bearer …` values inside `config.headers` are always
    replaced with the empty string; `config.authType = "bearer"` is the only
    persisted signal.
  - Reject requests that submit a non-empty, non-placeholder secret value —
    surface a `secret_leaked` code so the client can guide the user to
    remove the token before submitting.
- Data model designed for the current front-end payload shape
  (`packages/dmworkmcp/src/types/mcp.ts` in `octo-web`): name / icon / tags
  / slogan / transport / url|command / args / env / headers / authType /
  tools / usageExamples / faqs / notes / visibility / creatorName.
- **Wire response is a superset of the frontend TS type** (see
  `docs/api/mcp-v1.md` §0). Backend ships `visibility` + `creatorName` on
  `McpListItem` and `createdAt` + `updatedAt` on `McpDetail` even though
  today's TS does not declare them — the frontend adds them in a
  follow-up when list-card badges land.

## Out of scope

- MCP installation, execution, or process supervision. Marketplace is a
  registry only.
- Versioning, publish / draft state, or immutable release artifacts.
- Search relevance, ratings, reviews, telemetry.
- Admin surfaces for provisioning `system` MCPs (a separate follow-up brief
  will design that boundary).
- CLI synchronization endpoints.
- OpenAPI generation or SDK publishing. The API doc lives as
  `docs/api/mcp-v1.md`; formal machine specs are deferred.
- Fine-grained rate limiting; standard reverse-proxy limits suffice.

## Dependencies

- **Space membership authority.** The current `internal/auth` resolver
  returns only `{uid, name}`. This task depends on adding a Space membership
  probe (either a new method on the resolver or a separate small client)
  that answers "is uid U a member of Space S?" against `octo-server`. If
  the probe is not yet available, a temporary "trust the header, log a
  warning" path is acceptable in dev builds only, gated behind a
  configuration flag documented in the brief and removed before shipping.
- Client contract match: `octo-web` `dmworkmcp` package will switch its
  service layer's `USE_MOCK = false` to consume these endpoints. Field
  names in the API doc must match its `McpDetail` / `CreateMcpParams`
  shapes verbatim.
- Base path convention. The README already reserves `/market/api/v1`; the
  first octo-web draft used `/mcp/api/v1`. This task adopts
  `/market/api/v1/mcps` as the canonical prefix. The frontend switch is
  tracked as part of the client wiring PR, not this backend task.

## Acceptance

- `docs/api/mcp-v1.md` fully specifies six endpoints, the error envelope,
  every error code, and one example request/response per endpoint.
- `migrations/sql/20260714-01-mcp-catalog.sql` creates the `mcp_servers`
  table, its indexes, and any supporting objects; migration test round-trip
  passes on a clean MySQL 8 instance.
- Handlers, service, and repository layers implement the API doc exactly.
  `internal/api/router` remains the only place that maps URLs.
- Every mutating handler pulls the identity from the token, ignores any
  client-supplied identity, and denies cross-owner mutation with
  `err.marketplace.mcp.forbidden`.
- Every read handler enforces Space scope and returns
  `err.marketplace.mcp.not_found` (never a leaky 403) when the record
  exists in a different Space than the caller's.
- Secret redaction has direct positive AND negative tests:
  keys touched, plaintext rejection, well-known placeholder tolerated,
  `Authorization` header value never persisted.
- `GET /mcps/mine` returns only records where `owner_uid == caller`, across
  the caller's current Space; deleted records are excluded.
- Cross-Space negative tests: a member of Space A cannot read, update, or
  delete a `public` record from Space B; cannot discover it exists.
- `go test ./...`, `go build ./...`, and `docker compose config` pass.
- `make fmt` and `make vet` clean.

## Non-goals for this brief

- Defining how `system` MCPs are seeded. Add a follow-up brief.
- Deciding secret vault or KMS integration. The v1 stance is
  "never persist secret values, ever" — the vault question is a later
  concern gated by real installation flows.
- Frontend changes. Tracked in `octo-web` under a separate branch.
