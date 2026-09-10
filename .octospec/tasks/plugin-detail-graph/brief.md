---
type: Task
title: "Plugin: detail_graph aggregated endpoint"
description: Add `GET /plugins/detail_graph` returning a Plugin root together with the flat, deduplicated transitive closure of its relation graph, collapsing the frontend's 1+N+N×M `/plugins/detail` fan-out into one call.
tags: ["plugin", "api", "performance"]
timestamp: 2026-09-02T11:30:00+08:00
slug: plugin-detail-graph
source: self
---

# Plugin: detail_graph aggregated endpoint

## Goal

Rendering an Expert Team detail page on octo-web today costs `1 + N + N×M` calls
to `GET /plugins/detail` (root → N members → M skills/connectors per member).
Each response carries the heavy `plugin_json` blob and full-normalizes every
JSON field, forcing the frontend to stitch the tree client-side. This task
adds a new endpoint `GET /plugins/detail_graph?plugin_id=…` that returns the
root (full projection, byte-identical to today's `/plugins/detail`) plus every
edge in the transitive closure and a deduplicated list of related plugins in
light projection (manifest only, no `plugin_json`). The existing
`/plugins/detail` endpoint is preserved byte-for-byte; no migration, no model
change, no admin surface change in this task.

## Load-bearing behavior

- New endpoint is **additive**: `GET /plugins/detail_graph`,
  `@ID plugin.graph.get`, mounted on the public plugin router (tenant-scoped,
  Bearer auth). Route naming follows the existing verb-suffix, query-param-id
  convention on the public surface.
- Response is a **flat** (not nested) envelope:
  ```
  { data: {
      plugin:         pluginResponse,         // full, same as /plugins/detail
      relations:      relationResponse[],     // closure of all edges, data intact
      related_plugins: listItemResponse[]     // light, deduped, no plugin_json
  } }
  ```
  No `graph`/`node_count`/`is_partial`/`truncated` meta object is emitted.
- Root and related-node projections include the same owner-only review state as
  `/plugins/detail`: pending first, otherwise the latest non-deleted review.
  Review metadata is selected only when both the row's owner and Space match
  the authoritative caller. Other authors' and other Spaces' review IDs/statuses
  remain absent, including on globally visible nodes. The root and batch-node
  SELECTs carry these correlated projections, so enrichment adds no round-trips.
- Traversal depth is fixed by the relation matrix and derived from the root
  plugin's type — no `depth` query param:
  - `skill` / `connector` (leaves): zero edge queries beyond the root fetch.
  - `expert` (container of skills/connectors): one level of edges.
  - `expert_team` (container of experts): up to two levels (members + their
    skills/connectors).
- The endpoint performs **at most 4 SQL round-trips** regardless of fan-out
  (1 for a leaf root, 2 when no child is visible): root fetch → level-1 edges →
  (optional) level-2 edges → one batch payload fetch for all related nodes using
  `pluginSummaryColumns` (which deliberately omits `plugin_json`, reusing an
  existing column list).
- **Two caps, both enforced mid-scan**: node cap 630 and edge cap 2000. Both are
  anchored to the writer that actually mints squad graphs — container import,
  which admits `containerMaxMembers` (30) members each declaring up to
  `containerMaxSkills` (20) skills, and dedupes embedded skills only by
  `(file, name)`. A maximum-size legal import is therefore 30 + 30×20 = 630
  child nodes and one edge per child, and that squad's detail page must render
  rather than 413 forever; the install budget (`maxInstallRelationTargets` = 500)
  is the wrong anchor and sits below what import accepts. The edge cap is
  separate because the node cap alone does not bound cost — a graph whose members
  share targets keeps the unique-node count low while edges grow as
  members × targets-per-member, and `maxRelations` permits 200 edges per plugin
  (≈40k in a two-hop closure); 2000 allows ~3× the container ceiling for
  shared-target squads and fails closed above it. Both caps are checked while
  draining each result set, and each edge query carries
  `LIMIT maxGraphEdges + 1`, so an over-cap graph is never fully materialized in
  the process and an abandoned result set is never drained off the wire. The
  `LIMIT` does not remove the server-side sort — the join is still fully
  evaluated, and the level-2 `ORDER BY` cannot be served from
  `idx_plugin_relations_source_type_order` — it only bounds it. Exceeding either
  cap returns HTTP 413 `PAYLOAD_TOO_LARGE` with `details.max_nodes` and
  `details.max_edges`; the endpoint fails closed rather than silently
  truncating, so the UI never renders a partial squad. The repository mirrors
  the two container constants locally; `TestGraphCapsClearContainerImportCeiling`
  fails if they drift, and the edge-query expectations pin `LIMIT` to
  `maxGraphEdges + 1` so the clause and the cap cannot desynchronise.
- **Hidden related plugins are silently omitted** — edge and node both —
  matching the existing `/plugins/detail` behavior. No truncation flag, no
  error. This is deliberately different from the install path's fail-closed
  `ErrDependencyHidden`; install needs to refuse a partial provision, but a
  read-only detail page should degrade gracefully when cross-space targets
  exist.
- **Every descendant is filtered by the same `visibilitySQL` that
  `/plugins/detail` applies**, on both edge queries and the node payload query —
  embedded (`is_embedded=1`) children included. Embedded children get no
  relaxation: every writer that mints them (container import, container
  reupload, `RebuildGraph`, backfill) stamps each child with its container top's
  `(visibility, space_id, owner_uid)`, so `visibilitySQL` on a child is already
  equivalent to `visibilitySQL` on the container authorized as the root. A
  relaxation would return no additional rows while permanently dropping the
  per-row guard, leaving any future divergence between container and children to
  disclose them cross-Space with no code change here.
- Icons are resolved per-request with memoization by raw icon key (no extra
  allocations for shared icons). No `member_count` is derived onto any node: the
  relation matrix never admits an `expert_team` as a relation target, so a
  related node is never a team, and the root's response projection carries no
  `member_count` field. A client counts `expert_team_expert` edges in the
  returned relation slice, which is also the only count consistent with the
  caller's visibility.
- View/install/download counters are read-only projections on the existing
  read path; detail reads never write metrics, so fanning out to related
  plugins cannot bump their view counts.
- An extra `space_id` predicate on children is deliberately NOT imposed:
  `visibilitySQL` is not a pure space predicate — its
  `visibility IN ('public','system')` branch admits system-visibility children
  regardless of `space_id` — so a `space_id` match would incorrectly hide the
  NULL-space embedded members of a system-visible container from a caller who
  can legitimately see that container.
- The new repo primitive is not generalized to an arbitrary-ID batch read;
  `GetGraphClosure(ctx, scope, rootID string)` takes a single root ID, so the
  traversal is always anchored on one authorized root. The admin route for the same shape is intentionally deferred (repo
  works under `scope.Admin = true` as-is; handler veneer can be added when the
  admin console needs it). The install path is deliberately not refactored in
  this task — it is fail-closed and needs full `plugin_json` per node; sharing
  the primitive would require a visibility-mode and package-blob toggle that
  are separate concerns.
- API envelope and error codes use the fixed OCTO enum; swaggo annotations
  follow the project house style; `make openapi-check` must pass.

## Out of scope

- Admin surface (`/admin/plugins/:id/detail_graph`) — left for follow-up.
- Install-path refactor.
- Any new migration, model field, plugin type, or relation type.
- Changing `/plugins/detail` shape or behavior.
- Re-adding any backend-side secret value scanning.
- **A byte budget on the response.** Both caps count rows. Summing the caps at
  `maxJSONBytes` (1 MiB) per manifest/relation yields a loose bound of 630 + 2000
  = 2630 MiB (about 2.6 GiB), before the root package and JSON overhead. Decoding
  and re-encoding each blob can increase peak memory further. This is much
  larger than List's roughly 100 MiB bound; its existence does not make the
  graph's memory cost acceptable. A shared byte-budget follow-up must cover
  both read surfaces before broad production adoption. This PR does not claim
  a byte limit and therefore does not return a fictitious `details.max_bytes`.
- **Reconciling the four relation-count constants.** `maxGraphNodes` (630),
  `maxInstallRelationTargets` (500), `maxRelations` (200 per plugin), and the
  backfill's `validateGraph(..., 16, 500)` do not agree, so a maximum-size
  container is importable and readable but neither installable nor reproducible
  by backfill. This read cap is anchored to container import, the writer that
  mints squad graphs; the generic upsert API can still mint a 31-member team
  (651 children) that exceeds it and fails closed with a diagnosable 413 naming
  both caps. Widening the anchor to cover the upsert path — or bounding that
  path so the writers agree on what a legal squad is — is follow-up work that
  should move all four numbers together.

## Acceptance

- New endpoint `GET /api/v1/plugins/detail_graph?plugin_id=…` returns 200 with
  the flat graph envelope.
- Root plugin byte-matches `/plugins/detail`'s plugin projection (including
  `plugin_json`). Related plugins do not carry `plugin_json` (verified by
  wire-JSON assertion in handler tests).
- Real-MySQL HTTP regression tests use the actual handlers, service and
  repository: root JSON matches detail for pending, rejected, canceled,
  approved and absent reviews; related nodes carry the same owner review
  state; pending takes precedence over newer terminal reviews. Non-owners,
  foreign-Space rows and hidden roots cannot disclose review metadata.
- For a system team in the local dev DB, the endpoint returns its 4 embedded
  members plus reachable embedded skills with a single HTTP call; children do
  not carry `plugin_json`.
- Leaf (skill/connector) roots return empty edges/related with no edge query
  issued; expert roots issue one edge query; expert_team roots issue two.
- Children the caller cannot see are silently omitted (edge + node both),
  embedded and standalone alike; a node that vanishes between the edge scan and
  the payload query drops both itself and every edge touching it.
- Node cap (630) and edge cap (2000) both fire mid-scan, before the payload
  query issues; over-cap returns 413 with `details.max_nodes` /
  `details.max_edges`. A wide-but-shallow graph that stays under the node cap
  while exceeding the edge cap is pinned by test, and a maximum-size legal
  container import (30 members × 20 embedded skills = 630 nodes, 630 edges) is
  pinned as renderable.
- Missing `plugin_id` → 400; unknown id → 404. 401 is enforced by the router's
  authenticator middleware and is not exercised by the handler tests, whose
  shared `testEngine` always stamps a fixed dev identity.
- `go vet ./...`, `gofmt`, `make build`, `go test ./internal/...`,
  `make openapi-check` (with regenerated `docs/openapi/swagger.yaml`
  committed) all pass.
- No regression: existing `/plugins/detail` returns unchanged shape and
  relation counts; full `go test ./...` green.
