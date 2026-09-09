---
type: Task
title: "Task: plugin space review"
description: Space-level review workflow — members create private plugins freely, submit for Space visibility, Space admins approve (via web or IM notification card) before the plugin is listed in the org market.
tags: ["plugin", "marketplace", "review", "approval", "visibility", "im", "card-action"]
timestamp: 2026-08-31T12:00:00+08:00
slug: plugin-space-review
source: self
---

# Task: plugin space review

## Goal

Introduce an organization (Space) level review workflow on the unified plugin
surface. Members create and self-test `private` plugins without friction; the
act of requesting Space-wide visibility creates a review request that a Space
admin (owner/admin) processes either through a new "组织管理" page in the
market **or directly inside an approval card delivered to their DM by the
"通知助手" bot**. Only an approved request makes the plugin (or a new version
of an already-listed plugin) visible in the org market. Platform-wide
(`system`) publication and its octo-admin review queue are explicitly out of
scope for this task, but the model reserves room for them.

Confirmed product decisions (owner, 2026-08-31):

- **Trigger**: `private` create/self-test is free and unreviewed; submitting
  for Space visibility is what enters the review queue.
- **Version re-review**: a new version of an already-listed plugin must be
  re-reviewed; the currently listed version stays live during review.
- **Coverage**: all plugin types (`skill`, `connector`, `expert`,
  `expert_team`).
- **Version history is release history, not edit history**: personal edits are
  a mutable draft and never mint versions; a version is frozen at submit time
  and enters the org-facing version history only on approval. Rejected or
  canceled submissions never enter version history. Author edit history stays
  in `plugin_audit_logs`.
- **Manual version label**: the applicant types the version label in the
  submit form (pre-filled with a suggested bump, editable).
- **IM approval (2026-08-31 追加)**: on submit, the marketplace notifies all
  Space owners/admins via 通知助手 using an `approval_card` over
  `OCTO_CARD_ACTION_ROUTES`; the card carries 默认 approve/deny 双按钮
  (positive/destructive 配色). IM "拒绝" uses a hard-coded default reason
  (`管理员在消息卡片中拒绝,未填写原因`) and stamps the audit log with
  `decision_source=im`. 多管理员并发点击依赖接收方 CAS,第一个有效决策胜出,
  其他卡片各自渲染权威终态。

## Load-bearing behavior

### Review request model (new table `plugin_review_requests`)

Review state lives on an independent request entity, NOT on a plugin status
column. Rationale: version iteration stays coherent (listed v1 and in-review
v2 coexist), the same model later serves platform-wide review via
`target_scope`, and it cannot violate the existing invariant that an admin
metadata edit never changes a tenant plugin's visibility.

Columns (uuidv7 id, standard timestamps, soft delete):

- `plugin_id`, `space_id`
- `target_scope` — fixed `space` in this task; `system` reserved.
- `status` — `pending` / `approved` / `rejected` / `canceled`.
- `kind` — `first` (initial listing) / `upgrade` (new version of a listed
  plugin); derived server-side, never client-supplied.
- **Frozen submission snapshot**: `version` (applicant-typed label),
  `changelog`, `manifest_json`, `plugin_json`, `manifest_hash`, `plugin_hash`.
  The snapshot is stored on the request, NOT appended to `plugin_versions` at
  submit time — `plugin_versions` remains exactly the org-visible release
  history, and a rejected label never occupies the `(plugin_id, version)`
  unique key (so a fixed resubmission may reuse the same label).
- `applicant_uid/name`, `reviewer_uid/name`, `reason` (required on reject),
  `decision_source` (`web` default / `im` when the decision came from the
  notification card), `submitted_at`, `reviewed_at`.

Constraints: at most one `pending` request per `plugin_id` (enforced by a
database partial unique index or transactional `SELECT ... FOR UPDATE` +
1062→`ErrConflict`, following the existing `20260720-01-category-live-name-unique.sql`
pattern for soft uniqueness); version label is required, format-checked, and
must not collide with an already-published version of that plugin.

### State machine

- **Upload lands private.** The tenant import path no longer force-publishes
  Space-visible: `importConsumedTask`
  (`internal/service/plugin/import.go:193-224`) currently forces
  `visibility=space` + a visible default placement immediately — this changes
  to land as `private` (owner-visible draft, self-testable). The reviewed
  submit is the only path from `private` to `space`.
- **Submit** (owner-only): freezes the current draft content plus the typed
  version/changelog into a `pending` request. Draft edits after submit do not
  mutate the frozen snapshot; the admin reviews exactly what will be listed.
  After the submit transaction commits, marketplace **best-effort** dispatches
  an IM approval card to Space admins (see IM section below); notification
  failure is logged and never rolls back the submit.
- **Approve** (source ∈ {web, im}): in one transaction — append the frozen
  snapshot as a `plugin_versions` row, move `current_version_id`, apply the
  snapshot to the `plugins` row, and for `kind=first` flip `visibility`
  `private→space` and attach the visible `default` placement. For
  `kind=upgrade` the listed content/version swaps atomically;
  visibility/placement are already in place. Writes a `plugin_audit_logs` row
  with action `review_approve` and `remark` carrying `decision_source=<src>`.
  On IM-sourced approve the callback response sets `requester_uid` so
  octo-server's `StandardActionFinalizer` DMs an applicant result card.
- **Reject** (source ∈ {web, im}): `reason` required on web; on IM reject the
  server fills in `defaultIMDenyReason` (`"管理员在消息卡片中拒绝,未填写原因"`)
  — the card surface does not support text input. The plugin stays as-is
  (private draft, or the old version stays listed). The author may edit and
  resubmit. Writes `plugin_audit_logs` with action `review_reject` and
  `decision_source=<src>`.
- **Cancel**: the applicant may cancel a `pending` request. Cancel is web-only
  (no IM cancellation button on the card).
- **A Space admin CAN approve their own submission, but must do so explicitly**
  (no auto-approve on submit). An admin's submit lands `pending` like anyone
  else's; the admin then approves it from the queue or the IM card, which is one
  extra click and produces the same audit trail (reviewer = self,
  `decision_source=web` or `im`). Rationale: auto-approving on submit would make
  a single admin action silently flip live org-visible content with no
  confirmation step — an accidental-publish footgun on the exact path where
  content review matters most — while buying nothing that the extra click does
  not already provide. Divergence from the original "Space-admin self-submit
  auto-approves"; deliberate, see divergence item 26.
- **Grandfathering**: existing Space-visible plugins are treated as approved;
  no retroactive requests, no backfill.
- **Containers**: `expert`/`expert_team` review at the top container; embedded
  children follow the container and are never independently reviewable.
- **Concurrency**: two admins approving/rejecting concurrently (web or IM)
  produces at most one winning decision. The repository uses
  `SELECT ... FOR UPDATE` on the request row plus a state-CAS predicate
  (`WHERE status='pending'`) so the second committer hits `ErrConflict` and
  surfaces the already-committed outcome; for IM, the card-action receipt
  table (`card_action_receipt`) provides per-`event_id` idempotency per the
  octo-server consumer contract.

### Authorization prerequisite (cross-repo)

The marketplace does not know per-Space roles today; this task requires it.

- **octo-server `space_roles` on verify** (already in scope): extend
  `POST /v1/auth/verify?include=context` (handler `authVerifyToken`,
  `modules/user/api.go:4728`) with a `space_roles` map.
  `queryUserSpaceContext` (`modules/user/api.go:4838`) already reads
  `space_member sm` and only needs to also select `sm.role`. Wire encoding:
  `space_roles: { "<space_id>": <0|1|2> }`.
- **octo-server NEW internal Space-admin list endpoint**:
  `GET /v1/internal/spaces/:space_id/admins` (recommended location:
  `modules/space/`, NOT `modules/internal_resolve/` which has route-shape
  source-string assertions). Wraps existing `db.queryAdminsAndOwner`
  (`modules/space/db.go:672`, `status=1 AND role>=1`), returns
  `{admins:[{uid,name,role}]}`. Auth uses a dedicated
  `X-Internal-Token` bound to new env `OCTO_MARKETPLACE_ADMIN_LIST_TOKEN`
  (≥32 bytes), constant-time compare, per-IP rate limit, registered into
  `ValidateNotifyTokenExclusions` in `main.go` so it cannot collide with
  notify/docs/bot-mention/drive tokens.
- **octo-marketplace**:
  - `model.Identity` gains `SpaceRoles map[string]int` to consume the
    `space_roles` map from the verify response.
  - Reviewer-side endpoints enforce server-side that the caller's role in the
    request's Space is admin-or-owner (`role >= 1`). Encoding is normalized
    to the server's `space_member.role` values — 0=member, 1=admin, 2=owner;
    reviewer check is `role >= 1`. (octo-web displays the inverse
    1=owner/2=admin/3=member; the frontend gate is cosmetic only.)
  - Cross-Space access to a request returns `NOT_FOUND` without revealing
    existence, per the unified-plugin-backend brief.

### IM notification & approval card (new)

**Topology**: marketplace calls octo-server `POST /v1/internal/notify` with
`approval_card`; octo-server delivers DMs from the global `notification` bot
to every admin and, when a decision lands, its `StandardActionFinalizer`
mutates that admin's card to the terminal state and sends a separate
octo/v1 result card to the applicant (free — no marketplace work). All
traffic goes through the existing octo-server card-action dispatch pipeline
(`OCTO_CARD_ACTION_ROUTES`); marketplace hosts the callback endpoint.

**octo-server deployment config (no code change, route-only)**:
`OCTO_CARD_MESSAGE_ENABLED=true` plus one entry in `OCTO_CARD_ACTION_ROUTES`:

```json
{
  "sender_uid": "notification",
  "owner": "marketplace",
  "action_type": "marketplace.plugin_review.decision",
  "url": "http://<marketplace-internal>/v1/card-actions/decide",
  "secret_env": "OCTO_MARKETPLACE_CARD_ACTION_SECRET",
  "notify_token_env": "OCTO_MARKETPLACE_NOTIFY_TOKEN",
  "timeout_ms": 3000,
  "max_attempts": 5,
  "base_backoff_ms": 1000,
  "max_backoff_ms": 60000,
  "max_in_flight": 8
}
```

Secrets (`OCTO_MARKETPLACE_NOTIFY_TOKEN`, `OCTO_MARKETPLACE_CARD_ACTION_SECRET`,
`OCTO_MARKETPLACE_ADMIN_LIST_TOKEN`) are ≥32 bytes, pairwise distinct, and
distinct from `NOTIFY_INTERNAL_TOKEN`/`OCTO_DOCS_NOTIFY_TOKEN`/
`OCTO_DOCS_BOT_MENTION_TOKEN`/`OCTO_DRIVE_INTERNAL_TOKEN`; startup fails on
collision (uses existing `ValidateNotifyTokenExclusions`).

**marketplace outgoing client** (`internal/notify/`, pattern copied from
`internal/fleet/client.go`): 3s `http.Client`, `CheckRedirect` rejection,
`io.LimitReader` response bound, typed `APIError`, no retries. Two methods:
`ListSpaceAdmins(ctx, spaceID) ([]AdminInfo, error)` against the new
`/v1/internal/spaces/:space_id/admins` endpoint; `NotifyReviewCard(ctx, req)`
against `/v1/internal/notify`. Call sites MUST inspect the response
`delivered[]` AND `filtered{uid:reason}`; logging is mandatory but never
bubbles up to fail a submit.

**Dispatch timing & reliability**: notification fires **after** the submit
transaction commits, using the same best-effort shape as
`internal/service/expert/install.go:109-131` (trackInstall):
`context.WithoutCancel(ctx)` + tight (2s) deadline, failure is Warn-logged
and dropped. No outbox, no retry queue, no queue table in v1 — if the
best-effort send fails, admins still see the request in the "组织管理" web
queue.

**Card payload** (server template owned by octo-server `pkg/cardtmpl`;
marketplace only supplies bounded fields):

- `space_id`, `service="marketplace"`, `targets=<admins from ListSpaceAdmins>`,
  `actor_uid=<applicant_uid>`
- `approval_card`:
  - `action_type`: `"marketplace.plugin_review.decision"`
  - `title`: `"插件上架申请 · {plugin_name}"` (≤200 runes after truncation)
  - `description`: assembled as
    `"类型：{type} · 申请人：{applicant_name}\n版本：{version}（{first: 新上架 | upgrade: 当前 v{current_version}}）\n{changelog 前缀}"`
    (≤300 runes)
  - `actions: null` (use the server-default localized 允许/拒绝 buttons with
    `style: positive` / `style: destructive`; never construct custom actions
    because they drop style per `approval_request.go:194-199`)
  - `data`: `{ "review_id": "<uuid>", "plugin_id": "<id>" }` only; keys
    lower-case, ≤32 entries, values ≤500 runes. `owner`/`action_type`/
    `decision` are reserved and injected server-side.
- Content rules: all strings go through the server's `escapeMarkdown`; no
  markdown, no URLs, no images (the approval template body is only two
  `TextBlock`s, there is no FactSet/Image/OpenUrl — no "查看详情" link is
  possible on this card).
- marketplace does NOT inspect or construct `type=17` AdaptiveCard JSON
  (doing so is rejected as `err.server.notify.card_not_allowed`).

**Callback endpoint** (`POST /v1/card-actions/decide`, mounted OUTSIDE the
tenant `Authenticator`):

- Raw body middleware (mirror octo-server consumer doc §"Run a raw-body
  middleware before any JSON parser"); HMAC-SHA256 verification mirrors
  `internal/cardactiondispatch/signature.go` verbatim (33 lines). Canonical
  string: `"v1\n{POST}\n{escapedPath}\n{X-Octo-Timestamp}\n{X-Octo-Event-ID}\n{sha256hex(rawBody)}"`;
  signature header `X-Octo-Signature: v1=<hex>`; timestamp freshness
  ±300s; `body.event_id` must equal `X-Octo-Event-ID`; verify first, parse
  second; constant-time compare.
- Request body unmarshals as octo-server `DecisionRequest` (`event_id` uses
  `json:"event_id,string"`). `operator_uid` is an identity assertion, NOT an
  authorization grant — marketplace re-checks that the operator has role≥1
  in `space_id` via the resolver (cached by `auth.CachedResolver`),
  otherwise return `{"disposition":"forbidden","state":"pending"}`
  (HTTP 200, per protocol).
- Idempotency + ordering follows the consumer doc transaction template
  exactly:
  1. `BEGIN`
  2. `SELECT stored_response FROM card_action_receipt WHERE event_id=?`; if
     found, `COMMIT` and replay that stored_response verbatim.
  3. Validate `decision` is `"approve"` or `"deny"`; reject other values
     with `{"disposition":"forbidden","state":"pending"}` (the reserved
     decisions approve/deny are produced by the default button set).
  4. `SELECT ... FROM plugin_review_requests WHERE id=? AND status='pending' FOR UPDATE`;
     if not found or not pending → `disposition:"conflict"` (already decided)
     with the actual final `state` and `requester_uid`.
  5. Re-verify operator is still a reviewer of that Space.
  6. Apply the approve/reject service logic (reuse the same tx path as the
     web handlers; reject fills `defaultIMDenyReason` and stamps
     `decision_source=im`).
  7. `INSERT INTO card_action_receipt(event_id, stored_response_json, created_at) VALUES (...)`.
  8. `COMMIT`.
  9. Return the stored response. UNIQUE-key collisions must re-read and
     replay the winner's response.
- Response is exactly the 4-field `DecisionResult`
  (`disposition`/`state`/`requester_uid`/`display`), `DisallowUnknownFields`;
  `state ∈ {approved,denied}` requires `requester_uid` set to
  `applicant_uid` so the octo-server finalizer can DM them; `display.title`
  carries the plugin name for the terminal card header.
- HTTP status semantics per protocol: 2xx with typed body for all handled
  outcomes (including business rejection); 408/429/5xx for transient failure
  (octo-server retries with backoff); other 4xx goes to DLQ (avoid except
  for truly permanent errors).
- The `card_action_receipt` table stores `event_id` as a decimal-string
  `VARCHAR(32) PRIMARY KEY` (int64 over JS Number.MAX_SAFE_INTEGER), plus
  `stored_response_json`, `created_at`; no replay UI, ops use octo-server's
  `tools/card-action-dlq` for replay.

**Not in v1 (called out explicitly)**:

- Card "查看详情" / Action.OpenUrl button — not supported by
  `BuildApprovalRequestCard`, which only emits `Action.Submit`
  (`pkg/cardtmpl/approval_request.go:194-199`); admins use the web "组织管理"
  queue for deep review.
- Card-wide auto-invalidation when one admin decides — each admin gets an
  independent DM card; later clicks hit CAS conflict and render "该申请已由
  {first-decider} 处理: {approved|denied}" as the terminal state.
- Proactive "you were approved/rejected" push on web-side decisions — only
  IM decisions trigger the automatic applicant result card (for free via
  `StandardActionFinalizer`). Web decisions do not send an extra IM in v1.
- Per-admin delivery failure alerts beyond the server log; the web queue is
  the source of truth.
- Inline reject-reason input — approval cards only support
  `Action.Submit`, not `Input.Text`; the brief for docs approval used
  inputs, but the generic approval-card template for non-docs owners does
  not.

### API surface (tenant, `/api/v1/plugins`, existing `Authenticator`)

- `POST /plugins/review_requests` — submit `{plugin_id, version, changelog}`;
  snapshot and `kind` derived server-side.
- `GET /plugins/review_requests?mode=mine|space&status=&page=&page_size=` —
  `mine` is applicant-scoped; `space` requires the Space reviewer role.
- `GET /plugins/review_requests/:id` — detail incl. plugin/version summary
  and snapshot preview (e.g. SKILL.md).
- `POST /plugins/review_requests/:id/approve`
- `POST /plugins/review_requests/:id/reject` — `reason` required for web
  calls; IM callback injects the default.
- `POST /plugins/review_requests/:id/cancel` — applicant only.

Internal card-action endpoint (separate route group, NO `Authenticator`):

- `POST /v1/card-actions/decide` — HMAC-signed callback from octo-server
  (see IM section above).

Standard `{data}` / `{error}` envelopes, fixed OCTO error codes, `$octo-api`
workflow (`make openapi-check`, `make openapi-diff`) apply.

### Frontend (octo-web, implemented in the `dmworkskillmarket` package)

- **Navigation / Tabs**: `SkillListPage` (`packages/dmworkskillmarket/src/pages/SkillListPage.tsx`)
  extends `TabId` from `"skills" | "mine"` to `"skills" | "mine" | "org"`;
  the "org" tab renders a new review queue and is shown only when the
  current user's role in the active Space is owner/admin (read from the
  existing space-role model; note that `dmworkbase` uses the inverse
  encoding `role=1 → owner, role=2 → admin`; this is a cosmetic gate —
  server-side enforcement is authoritative). A pending-count badge is shown
  on the tab. (No `dmworkbase` sidebar/`MarketSidebar` change required in
  v1; the tab lives inside the existing market module.)
- **Skill card states** (`components/SkillCard.tsx`): render status badges
  derived from the join of `visibility` + the latest pending/rejected
  review: 私有 / 审核中 / 已拒绝·展示理由 / 已上架 / 已上架·新版审核中;
  action buttons 「提交组织审核」/「撤回申请」/「重新提交」/「发布新版本」
  are gated by the derived state.
- **New-Skill / Upload flow** (`components/NewSkillModal.tsx`): replace the
  hardcoded `visibility: "space"` final step with an explicit publish-scope
  choice (「仅自己可见(私有)」/「提交组织审核,通过后组织内可见」); the latter
  reveals editable `version` and `changelog` fields pre-filled from the
  parsed metadata and a version-bump suggestion. Submit in the second case
  calls `POST /plugins/review_requests`; success copy states "已提交,待审核".
- **"我的插件" tab**: shows the status badges above; rejected entries show
  the reject reason inline with a resubmit entry; pending entries show a
  cancel entry.
- **"组织管理" tab** (new, in `src/components/ReviewQueue.tsx`): two sub-tabs
  待审核 / 已处理; list rows show icon, name, type tag, 首次上架/版本更新
  tag, applicant + submitted time, rejected entries inline the reason; row
  actions: 「通过」(direct), 「拒绝」(opens a reason-required modal matching
  the current `DeleteConfirmModal`/`BotPublishModal` pattern); clicking the
  row opens a detail drawer with metadata, SKILL.md snapshot preview,
  upgrade callout ("当前在架 vX → 申请上架 vY"), and approve/reject-with-reason
  footer actions.
- **API client** (`api/skillApiReal.ts`): add functions for the six
  review endpoints (list/get/submit/approve/reject/cancel), plus a
  `useReviewRequests` hook in `hooks/` following the `useSkills` pattern.
- **Types** (`types/skill.ts`): add `ReviewStatus`, `ReviewKind`,
  `ReviewRequest` (camelCase) plus `RawReviewRequest` (snake_case wire
  form); no new visibility enum values; `Skill` does not gain a review
  status column (review is a separate entity, derived at query time).
- **i18n**: new keys under `skillMarket.review.*`; copy uses `t()` via the
  existing `useI18n()` in `SkillListPage.tsx:12`.
- **Identity**: read `space_roles` from the existing auth context; the
  role gate is a display hint only (server 404/403 on unauthorized
  accesses).

## In scope

- Migration for `plugin_review_requests` (+ down step), plus
  `card_action_receipt` for IM callback idempotency.
- Repository + service + handlers for the seven endpoints above (six
  tenant + one card-action callback), with the approve transaction
  described in the state machine and the CAS/idempotency behavior.
- Import-path change: tenant uploads land `private`, no auto-placement.
- Identity extension consuming octo-server `space_roles` (marketplace side)
  and the new internal admin-list endpoint (octo-server side).
- IM notification dispatch on submit (fail-open, best-effort) and the
  card-action callback endpoint with HMAC verification and transactional
  CAS.
- The octo-web market pages listed in the Frontend section above.
- Configuration on both sides for the three new marketplace secrets
  (`OCTO_MARKETPLACE_NOTIFY_TOKEN`, `OCTO_MARKETPLACE_CARD_ACTION_SECRET`,
  `OCTO_MARKETPLACE_ADMIN_LIST_TOKEN`) and the `OCTO_CARD_ACTION_ROUTES`
  entry.
- OpenAPI regeneration.

**SCOPE DIVERGENCE at head (commit `f151687` onwards):** the code that
shipped also adds the two-axis listing model (`listing_state`) and two
extra tenant endpoints, `POST /plugins/publish` and `POST /plugins/delist`
(`internal/api/handler/plugin/handler.go:86-87`; handlers in
`internal/api/handler/plugin/listing.go`, service in
`internal/service/plugin/listing.go`, repo in
`internal/repository/plugin/listing.go`), four additional migrations
(`20260902-00-plugin-listing-state.sql`, `...-01-...backfill`, `...-02-...reindex`,
`...-03-plugin-review-submitted-index`), and a rewritten `visibilitySQL`
(`internal/repository/plugin/repo.go:105`) plus a new `listedSQL` predicate
(`internal/repository/plugin/read.go:143`) that together touch nine read
sites and one write site. The only authorization narrative for these is
items 29–37 below, which were written AFTER the code landed; none of the
original in-scope bullets name them. Treat those items as the
retroactive scope statement, not as pre-existing plan.

## Out of scope

- Platform-wide (`system`) publication requests and the octo-admin review
  queue (`target_scope=system` reserved; octo-admin untouched).
- Proactive IM notification to the applicant on web-side decisions (only
  IM decisions auto-notify via `StandardActionFinalizer`; follow-up to add
  a symmetric push when acting from the web).
- A custom terminal-card visual for marketplace reviews — uses the
  `StandardActionFinalizer` localized 申请已允许/已拒绝 copy; extending
  the finalizer registry for `owner=marketplace` is an octo-server change
  and explicitly deferred.
- Card auto-invalidation across recipients (one admin deciding does not
  mutate other admins' cards; later clicks return the CAS conflict
  terminal state via the callback).
- Card "查看详情" deep-link / `Action.OpenUrl` / reject-reason input on the
  card (not supported by `BuildApprovalRequestCard`).
- An org-wide "manage all Space plugins" console (delist/transfer) beyond
  the review queue.
- Backfilling review requests for existing listed plugins.
- Any change to the admin (`/api/v1/admin/*`) surface semantics. **DIVERGED at head — see the annotation below.**
  The shipped code does change admin semantics and was not re-scoped in prose:
  `applyStoredVersionRules` (`internal/service/plugin/admin.go:199-256`) binds
  `AdminUpdate` and the admin container reupload (`admin_import.go:162-165`) to
  the same forward-only-version + stored-label grandfathering the tenant update
  applies, closing a gap the admin routes had simply never been visited to
  expose; and `adminEffectiveWrite` (`admin.go:141-146`) re-stamps
  `ListingState = published` on the admin create path (inert on update, because
  the UPDATE statement does not write `listing_state`). Neither was in the
  original scope; both are real behavioral changes. `adminEffectiveWrite` still
  preserves the row's existing `visibility`/`space_id`/`owner` on update, so
  the "admin metadata edit must never force-publish a tenant-private row"
  invariant (A1 in AdminUpdate's doc) still holds.
- New per-message delivery guarantees beyond best-effort + server log; the
  web queue is the source of truth.

## Acceptance criteria

- Migration up/down tests; `go build ./...`, `go vet`, `gofmt` clean,
  `go test ./...` green.
- State-machine tests: submit freezes snapshot; post-submit draft edits do
  not leak into the reviewed snapshot; approve(first) flips
  visibility+placement and mints the version row; approve(upgrade) swaps
  listed content atomically while the old version was live until then;
  approve self-heals the default placement (hidden → visible, missing →
  inserted, already visible → untouched); reject requires reason (web) or
  fills default (IM) and changes nothing
  else; cancel by applicant only; single-pending constraint; version-label
  collision with a published version rejected, reuse of a rejected label
  accepted.
- Authorization negative tests: non-member 404, member-but-not-admin cannot
  list `mode=space` or approve/reject (403/404 per enumeration rules),
  cross-Space request access 404, applicant-only cancel; card-action
  callback with an `operator_uid` that is no longer a reviewer returns
  `forbidden/pending` without mutating state.
- Tenant upload no longer auto-lists: fresh import is `private` with no
  visible placement.
- Card-action protocol tests: signature verification using the fixed test
  vector in `docs/card-action-callback-consumer.md:178-187` must pass in
  Go; request body without valid signature is rejected; stale timestamp
  (>300s skew) rejected; `body.event_id` vs `X-Octo-Event-ID` mismatch
  rejected; unknown fields in the response JSON cause validation failure
  (mirrors `DecodeDecisionResult` rules); concurrent callbacks with
  distinct `event_id` for the same `review_id` produce exactly one
  committed decision and the other returns the authoritative terminal
  result; duplicate `event_id` replays the stored response verbatim
  (including on a different process/instance); approved/denied responses
  set `requester_uid=applicant_uid`.
- Notification fail-open: submit succeeds and commits even when octo-server
  is unreachable, returns 5xx, or returns partial `filtered`; the failure
  path logs, and a successful submit with a reachable octo-server logs its
  delivered/filtered counts. **Metrics deferred** to a follow-up once the
  notify surface has a counter registry — the module has no in-process
  counter framework today; rationale in divergence item 27.
- IM-reject audit: IM reject rows have `reason = defaultIMDenyReason` and
  audit `remark` contains `decision_source=im`; web reject keeps the
  caller-supplied reason and `decision_source=web`.
- `make openapi-check` and `make openapi-diff` pass with regenerated spec
  committed.
- Frontend smoke (manual or via the existing `market-browser-test.cjs`
  flow): tab visibility gated by role; submit from upload modal creates a
  pending request; "组织管理" queue shows pending count; approve/reject
  transitions propagate to the card badges on "我的插件".

## Prototype

`prototype.html` in this directory — self-contained mock covering upload
scope choice, submit with manual version + changelog, review queue (web),
reject-with-reason (web), resubmit, upgrade re-review with old version
staying live, **plus** a 右下角 "通知助手" floating panel simulating the IM
approval-card surface with 通过/拒绝 buttons, default reject reason,
applicant result receipt, and a cross-admin conflict state when another
admin has already decided.

## Implementation divergence record (marketplace backend, rebuilt 2026-09-01)

The sections above were written against an older version/placement model and an
octo-server contract that has since changed. Where they disagree with what
shipped, **what shipped wins**. Each item below is a deliberate decision, not an
oversight — do not "restore" any of them without revisiting the reasoning.

### 1. The gate is VISIBILITY ONLY. A placement is never HIDDEN — but approve self-heals it forward.

The brief has approve "attach the visible `default` placement" and has the import
land a *hidden* placement. The hidden import placement does not happen.

The market list already applies two independent filters: an INNER JOIN on a
`visible=1` placement, AND
`(visibility IN ('public','system') OR (space_id=? AND (visibility='space' OR owner_uid=?)))`.
The second filter alone is a complete review gate: a `private` plugin is readable
by its owner and invisible to everyone else in the Space. Hiding the placement
adds nothing and breaks something — the author's own "我的插件" list uses the same
INNER JOIN, so a hidden placement would hide the draft from the person who
created it.

So: `defaultMarketPlacement` and `syncDefaultPlacement` are unchanged in
behaviour, every create still attaches a visible default placement, and nothing in
the review workflow ever sets `visible=0`.

**Amended 2026-09-02 (review of PR #74).** Approve DOES write the placement, in
one direction only: `ensureVisibleDefaultPlacement` makes the default placement
exist and be visible, inside the same transaction as the status/visibility swap.
The earlier "approve writes exactly one column" purity had a real cost — a legacy
row with no default placement (pre-auto-placement) or a publish-era `visible=0`
row would be flipped to `space` and still not appear in the market, and the author
could no longer repair it by saving, because item 19's clamp makes a listed
plugin's write path a 409. Approval is the moment the product promises "this is in
the market now", so it must make that true.

The helper is idempotent and never hides: already-visible placements are left
untouched (no `updated_at` churn), a `visible=0` row is flipped to 1, and a
missing row is inserted with `ON DUPLICATE KEY UPDATE visible=1` (the unique key
is `(placement_code, plugin_id, category_key)`, so a concurrent writer must not
turn an approval into a duplicate-key error). Category is not touched;
`syncDefaultPlacement` still owns category reconciliation on the write path.
Covered by `TestApproveSelfHealsTheDefaultPlacement` (all three cases) against
real MySQL.

### 2. `kind` derives from visibility, never from whether a version exists.

`InsertReviewRequest` classifies `first` as `visibility == 'private'`. It does
NOT look at `current_version_id`.

This is load-bearing. Main's import path snapshots a version as part of the
upload, so a private draft normally HAS a `current_version_id`. Gating on its
absence classifies every real upload as an `upgrade`, and the `isFirst` branch of
approve — the only code that flips visibility — never runs: the request reaches
`approved`, a version is minted, and the plugin stays invisible forever, with no
error anywhere. A fixture built through the plugin-write API hides the bug
because that path also leaves `current_version_id` NULL and lands on the correct
branch by accident, which is why
`TestInsertReviewRequestDerivesKindFromVisibility` seeds the state a real import
produces.

### 3. The applicant's version label is NEVER written to `plugin_versions.version`.

The brief says approve should "append the frozen snapshot as a `plugin_versions`
row" carrying the submitted version. That column is not a label — it is a
per-plugin auto-increment counter (`"1"`, `"2"`, …) computed by
`snapshotVersion`. Writing a semver string into it corrupts the sequence for
every later save.

Approve therefore calls main's own `snapshotVersion`, which mints the next
counter value and writes the applicant's label to `plugins.current_version` (the
only place a caller-declared label has ever lived).

The knock-on: `plugin_versions` no longer records published labels at all, so
label uniqueness cannot be a DB unique key. `publishedVersionLabels` reconstructs
it from the review table — the labels of every `approved` request for that plugin
— plus the plugin's own `current_version` when the plugin is already listed
(grandfathering rows that were Space-visible before this feature, with no
backfill). A private draft's `current_version` is deliberately EXCLUDED: it is a
draft label, and a first listing at the import default `1.0.0` is the normal
case. Labels from `rejected`/`canceled` requests stay reusable.

### 4. The frozen snapshot includes the RELATION GRAPH.

`plugin_review_requests` carries `relations_json` alongside
`manifest_json`/`plugin_json`. For `expert`/`expert_team` the membership graph IS
the reviewable content; freezing only the documents ships the reviewed manifest
next to whatever the live membership happened to be at approve time, and records
zero relations on the minted version.

Approve applies the frozen graph through main's `syncRelations`, after
`reconcileFrozenRelations` clears any frozen relation_id that is no longer live
(so an edge the author deleted post-submit is re-created rather than failing the
approval) and normalizes a JSON-`null` `Data` back to nil — `json.RawMessage`
round-trips nil as the four bytes `null`, which
`chk_plugin_relations_json_object` rejects.

Relation targets are re-locked under the **plugin owner's** scope, not the
reviewer's: an expert's bundled skills are `private` rows owned by the applicant,
and resolving them as the reviewer 404s every container approval.

**Not frozen: `attachment_keys_json`.** The storage sidecar stays whatever the
live row has. A reupload between submit and approve therefore pairs the frozen
package with the new object keys. Same family as item 7; closing it needs the
draft/live split. **SUPERSEDED by item 22 — the sidecar is frozen now.**

### 5. Approving a container promotes its embedded children.

Not in the brief. An `expert`/`expert_team` whose embedded children stay
`private` is uninstallable by anyone but its author: `resolveInstallDetail`
refuses an install whose declared relation count exceeds the targets the caller
can see. `promoteEmbeddedChildren` lifts exactly the `is_embedded`, same-Space,
still-`private` children of the reviewed graph (derived AFTER the relations are
synced, so a member dropped from the frozen graph is not promoted).

### 6. Two tenant write paths are clamped, and only two.

**SUPERSEDED by items 19 and 29.** The clamp model described below was removed
when `listing_state` landed. `tenantVisibilityAllowed` is gone
(`internal/service/plugin/validation.go:111-118` records the removal), and
`Service.createWithID` now says explicitly "there is no longer anything to
clamp here" (`internal/service/plugin/service.go:483-487`): declaring
`visibility=space` on a draft is legal and lists nothing until `Publish`
routes it through review and `ApproveReview` stamps `published`. The gate
that replaced it is `listing_state`: `ErrListedRequiresReview` (item 19)
refuses edits to a `published`+`space` row, and only `ApproveReview` and
`Publish` can set `published`.

The original text is kept below for the record; it does not describe the
shipped code.

**Original text (clamp model, never shipped in this form):**

- `createWithID` (tenant create + fresh import) forces `private`.
- `Service.update` (tenant update + reupload) refuses to RAISE visibility;
  keeping it or lowering to `private` (self-delisting) stays allowed.

Both VALIDATE the requested value before overriding it, so `visibility:"public"`
or a garbage value is still a 400 rather than a silent downgrade — the existing
tenant contract for those inputs is preserved.

Both exempt `IsSystemAdmin`. A platform operator reaches every one of these
transitions through `/api/v1/admin/*` anyway, and `system` rows are not
tenant-owned. `AdminCreate`/`AdminUpdate`, container import/reupload and the
admin skill import are untouched.

### 7. A re-import of a LISTED plugin goes through review. (Superseded by item 19.)

**Original text (kept for the record):** "`resolveImportFields` preserves an
already-listed plugin's visibility on reupload … That means the content of a
listed plugin can be replaced without a new review. Out of scope; follow-up."

**Resolved by item 19, and the follow-up is closed.** The tenant re-import runs
through `Service.update`, which now refuses a listed plugin outright with
`ErrListedRequiresReview` → 409. So a re-import can no longer replace live
content: the author submits a review request (a skill may attach the re-uploaded
zip via `parse_task_id`) and approval is the only thing that swaps what the Space
reads. Visibility preservation in `resolveImportFields` still stands — it only
means the reupload does not silently DEMOTE a listed row — but it is no longer a
hole, because the reupload never reaches the write.

A re-import of a PRIVATE draft still replaces the draft directly; nobody else can
read it, so there is nothing to review.

Tests: `TestReimportOfAListedPluginIsRefused`,
`TestReimportPreservesTheExistingVisibility`.

### 8. Single-pending is a STORED generated column, not a partial index.

MySQL has no partial indexes — `CREATE UNIQUE INDEX ... WHERE` is PostgreSQL
syntax and fails outright. `pending_plugin_id` projects NULL for every
non-pending or soft-deleted row, and MySQL treats NULLs in a UNIQUE index as
distinct. Same trick as `20260720-01-category-live-name-unique.sql`. Both
mechanisms are in place: the index is the hard guarantee, and the transactional
`SELECT ... FOR UPDATE` gives the loser a typed `ErrConflict`.

### 9. Losing a decision race is `ErrConflict`, and cancel-after-decision is 409.

`classifyMissingPending` distinguishes "already decided" (`ErrConflict` → 409,
carrying the committed outcome) from "not visible to this Space"
(`ErrNotFound` → 404 that does not confirm the id exists). `CancelReview` does
the same: a 404 would tell an applicant whose request was just approved that it
never existed.

### 10. Web authorization reads `Caller.SpaceRole`, never the notifier.

`Caller.SpaceRole` is populated by the HTTP layer from
`identity.SpaceRoles[spaceID]` — the Space THIS request is for, so an admin of
another Space arrives as a member. System admins are treated as owner.

It is deliberately independent of the IM wiring. An earlier draft resolved the
web role through the notifier, which meant nobody could approve anything unless
IM notification happened to be configured.

`writeServiceError` gained a 403 branch (`ErrReviewForbidden` →
`errcode.PermissionDenied`); before this, a permission error fell through to 500.

`adminCaller` (`internal/service/plugin/admin.go`) builds a Caller with
`SpaceID: ""` and `IsSystemAdmin: true`. The admin surface does not route into
review, but `isReviewer` short-circuits on `IsSystemAdmin` first so such a Caller
can never be demoted by the empty-Space comparison.

### 11. octo-server's contract changed: no roster, role lookup + `target_role`.

`GET /v1/internal/spaces/:space_id/admins` has been **deleted** (it leaked
verified legal names cross-tenant, and empty-vs-non-empty was a Space-existence
oracle). Marketplace therefore makes **no roster call at all**:

- Submit dispatches with `target_role: "space_admin"`; octo-server resolves the
  recipients, excludes robots, and reports them in `delivered`. With
  `target_role` the caller never learns the roster, so `delivered` is the only
  delivery record there is — an empty one means the Space has no active human
  admin (a legitimate state, logged as a warning, not an error).
- The card callback re-verifies the operator with
  `GET /v1/internal/spaces/{space_id}/members/{uid}/role`, whose `role` is a
  nullable int (0 is a real role) and whose "absent" answer is byte-identical for
  non-member, removed member, unknown Space and disbanded Space.
- The shared token env is **`OCTO_MARKETPLACE_INTERNAL_TOKEN`** (renamed from
  `OCTO_MARKETPLACE_ADMIN_LIST_TOKEN`). The brief's three-secret list is now two:
  the internal token and `OCTO_MARKETPLACE_CARD_ACTION_SECRET`.

### 12. Card dispatch is entirely post-commit. Nothing runs before the response.

`dispatchReviewCard` builds nothing and calls nothing on the request goroutine:
the whole payload assembly and the octo-server call happen inside
`notify.BestEffort`, on a detached context with a tight deadline. An earlier
design fetched the roster synchronously, which put a 3-second remote timeout in
front of every submit and lost the card entirely when the client disconnected
right after commit.

### 13. A role-lookup failure is a retryable 5xx, NOT `forbidden`.

`disposition:"forbidden"` is a 200 that octo-server acks and never redelivers.
Reporting "octo-server is unreachable" that way silently discards a real admin's
click. `operatorRole` returns `(nil, nil)` for "not a member" and an ERROR for
"could not find out"; the handler turns the error into 503. An unconfigured
internal token is also a fault, not a refusal — a deployment that mounted the
callback without it cannot authorize anyone, and answering `forbidden` would burn
the event.

### 14. The card-protocol enums are octo-server's vocabulary, not ours.

Our `ReviewStatus` is `pending/approved/rejected/canceled`; the callback protocol
is `pending/approved/denied/cancelled`. `DecodeDecisionResult` validates the
enum, so emitting our spelling makes octo-server reject the entire response.
`cardState()` in `internal/service/plugin/review.go` is the single translation
point; never hand it `string(status)`.

### 15. `readme_content` and `plugin_icon` are actually populated.

- `readme_content` is extracted from the FROZEN package (`SKILL.md`, then
  `README.md`/`AGENTS.md`, falling back to the manifest description so connectors
  are not blank). Storage-backed attachments are not fetched on this read path.
  The previous build never assigned the field, so reviewers approved a blank body.
- `plugin_icon` goes through the same `resolveIcon` path the plugin list uses. An
  uploaded icon is stored as an object key; emitting it raw 404s in the browser.
- The list query does not select `manifest_json`/`plugin_json`/`relations_json`;
  only the detail read does.

### 16. Wiring, spec, and lint notes.

- `router.Public` gained a `ReviewConfig` parameter (octo-server URL, internal
  token, card-action secret, timeouts) rather than a package-level global. With
  an empty value the six review endpoints work identically and simply dispatch no
  card; the callback route is mounted but permanently closed.
- The callback is mounted on the ROOT engine at `/v1/card-actions/decide`,
  outside the tenant `Authenticator`, and is deliberately ABSENT from the OpenAPI
  spec: it is not a client contract. octo-server's
  `docs/card-action-callback-consumer.md` is authoritative, and its published
  signature vector is pinned in a test so a canonical-string drift fails a test
  instead of 401ing every callback in production.
- `internal/api/handler/plugin/review.go` is registered in `OPENAPI_SCAN_DIRS`.
- Two spectral WARNINGS remain on the new endpoints (`octo-auth-has-403` on
  `GET /plugins/review_requests/{review_id}` and on `.../cancel`). Left as-is on
  purpose: neither operation can return 403 — the detail read folds "not yours"
  into 404 so it cannot confirm existence, and cancel is applicant-only with no
  role gate. Declaring an unreachable status would make clients write dead code.
- Review request bodies use a size-bounded but unknown-field-TOLERANT decoder,
  unlike `/plugins/upsert`'s strict one: the octo-web client is already built and
  shipped against this contract.

## Product amendment (owner, 2026-09-01): an upgrade review must review content

Items 1–16 above stand. This section supersedes the parts of them that assumed
`SubmitReview` always snapshots the `plugins` row.

### 17. Why the upgrade path was theatre

`POST /plugins/review_requests` originally took only `{plugin_id, version,
changelog}` and froze whatever was on the `plugins` row. For a plugin already
listed to the org, that row IS what the org is reading — and a tenant could edit
it directly through `/plugins/upsert`, visible Space-wide the instant it
committed. The reviewer therefore approved content that had already shipped, and
approval only minted a version label. Hiding the edit button in the UI removes
the affordance, not the bypass: it was one curl away, the same class of hole as
the upsert-visibility one.

### 18. Submit carries the content; for an upgrade it MUST.

`POST /plugins/review_requests` body:

```jsonc
{
  "plugin_id": "…",              // required
  "version":   "2.0.0",          // required, applicant-typed label
  "changelog": "…",              // optional, <= 1000 runes
  "parse_task_id": "…",          // optional, skill-only; see item 21.
                                 // Mutually exclusive with manifest_json/plugin_json.
  "manifest_json": { … },        // see below
  "plugin_json":   { … },        // see below
  "relations": [                 // optional; see semantics below
    {
      "relation_id":      "rel-1",        // optional: omit to create a new edge
      "source_plugin_id": "plugin-1",     // optional; must address plugin_id if sent
      "target_plugin_id": "skill-1",      // required
      "relation_type":    "expert_skill", // required
      "sort_order":       0,
      "data":             { … }           // optional object
    }
  ]
}
```

- `manifest_json` / `plugin_json` are supplied **together or not at all**; half a
  pair is a 400. Filling the missing half from the live row would reintroduce
  exactly the no-op above.
- **`kind=upgrade` (plugin is `space`): required.** Absent → 400
  `VALIDATION_ERROR` with `details.field = "manifest_json"`.
- **`kind=first` (plugin is `private`): optional.** Absent → the draft row is
  snapshotted, which is honest: while private, that row is nobody else's business.
- `relations` are FULL OBJECTS in the same shape `/plugins/upsert` already uses —
  not a list of ids — so the frontend reuses its existing relation serializer.
  Target-state semantics, also as in upsert: present (even `[]`) replaces the
  reviewed graph. **ABSENT (the key omitted, or `null`) inherits the plugin's live
  graph**, so a client that only edits documents cannot silently empty an expert
  team by forgetting the field. That absent-vs-empty distinction is load-bearing
  and is pinned by a handler test.
- Submitted content goes through the SAME canonicalization an ordinary write uses
  (`CanonicalizeDocuments`), against the plugin's EXISTING name / type / tags —
  this endpoint reviews content, not market metadata. A malformed manifest is a
  400 at submit, not a surprise when a reviewer clicks approve.
- **Submit never writes to `plugins`.** Asserted at both layers: the service test
  checks the fixture row is unchanged, and the MySQL test reads every content
  column plus the relation set before and after and requires them identical.

Storage attachments: a **declared-JSON** submission must reference exactly the
object keys the live row already holds — `reinjectUpdateStorageKeys` restores them
for unchanged storage content, and a sidecar that differs afterwards is a 400.
Introducing or changing a storage attachment goes through the zip path
(`parse_task_id`, item 21) or the import/reupload path, which own object
lifecycle.

Relation graphs: a reviewed graph may add **standalone** catalog targets and drop
existing edges, but cannot mint new EMBEDDED children — `lockRelationTargets`
refuses to adopt an embedded child that is not already this source's own, because
a later container reupload would soft-delete it out from under the adopter. New
embedded children come only from the container import path.

### 19. A listed plugin cannot be modified through the ordinary write path.

**This item SUPERSEDES item 7 and the first half of item 6.** Item 7 recorded the unreviewed-live-content hole
as an accepted follow-up; this gate closes it, for upsert and for the tenant
re-import alike.

`Service.update` (which backs `/plugins/upsert` AND the tenant re-import) refuses
outright when `old.listing_state == 'published' AND old.visibility == 'space'`,
with `ErrListedRequiresReview` →
**409 CONFLICT**, `details.conflict_reason = "listed_requires_review"`, hint
"Submit a review request, or set visibility to private first." **(CORRECTED
relative to an earlier draft of this item.)** The earlier text below said the
predicate was `old.visibility == 'space'` unconditionally; that is wrong. The
gate is the conjunction `(published AND space)` — see
`internal/service/plugin/service.go:603-604`. A `private`+`published` row is
NOT refused on this gate (it is editable by its owner, and lowering/raising
visibility against it has the effects described in item 31), and a
`space`+`draft` or `space`+`delisted` row IS freely editable — which is what
makes "edit and publish again" work after a takedown. The service comment at
`service.go:572-578` spells this out.

409 rather than 403 because it is a STATE conflict, not a permission problem: the
owner may change this plugin, just not through this door.

**No field is carved out.** Every field a tenant can change through upsert is
org-visible once the plugin is listed:

| field | why it is org-visible |
| --- | --- |
| `manifest_json` / `plugin_json` | the content itself |
| `plugin_name`, `tags` | `CanonicalizeManifest` requires them to match the manifest, so changing either IS a content change (and both are rendered in the market) |
| `category_id` | decides which market category the plugin is listed under, and is mirrored onto the placement |
| `publisher`, `icon` | rendered on the market card |
| `version` | the label the org sees as the current version |

That leaves nothing that could not affect what the Space reads, so a per-field
allowlist would be all list and no allow.

**CORRECTED: lowering to `private` in the same call is NOT an escape hatch.**
The earlier text below said "Lowering to `private` in the same call stays
allowed. That is how an author takes their plugin down." That was the
self-delist loophole and it is closed in shipped code
(`internal/service/plugin/service.go:574-576`: "Self-delisting by lowering
visibility to private is NO LONGER a way out: taking a listed plugin down is
a Space-admin action (Delist)"). Visibility is free to change only on an
UNLISTED row, where it declares intent rather than changing what the org
reads; on a `published`+`space` row the refusal fires before visibility can
be lowered to evade it, and `Delist` is the only takedown path.

A system admin is exempt, as with the other gates: `/api/v1/admin/*` already
reaches all of this, and `system` rows are not tenant-owned. **(See item 36
for the precise role set that holds this exemption — it is wider than
"super-admin only".)**

Two pre-existing reupload tests
(`TestImportReuploadPreservesIconWhenOmitted`,
`TestImportReuploadKeepsStoredVersionWhenOmitted`) used a `space` fixture purely
as scenery; they now use `private`, which is the state a reupload can actually
target. `TestReimportPreservesTheExistingVisibility` lost its `space` case for the
same reason — that outcome is now a refusal with its own test.

**Original (pre-correction) text, kept for auditability of what the earlier
draft claimed:**

- `Service.update` refuses outright when `old.visibility == 'space'`, with
  `ErrListedRequiresReview` → 409, hint "Submit a review request, or set
  visibility to private first."
- "Lowering to `private` in the same call stays allowed. That is how an author
  takes their plugin down in order to work on it; the content then lands on a
  row nobody else can see. `space → private` alone also still works."

Both statements above describe a gate that predates `listing_state` and do
not match the shipped predicate or the self-delist closure.

### 20. Approve is where the listed content changes, and only there.

Unchanged in shape, but it now genuinely matters for upgrades: the `upgrade`
branch applies the frozen manifest, package, hashes, version label AND relation
graph. `TestUpgradeSubmitLeavesTheListedRowUntouchedAndApproveSwapsIt` drives the
whole sequence against MySQL and fails if either half moves at the wrong time.

## Product amendment (owner, 2026-09-01): a skill upgrade may be a zip

Items 1–20 stand except where noted below. This section closes the gap item 4 and
item 18 both left open: a review submission could not introduce or change a
storage attachment, so the "发布新版本" flow could not accept a re-uploaded zip.

### 21. Submit accepts `parse_task_id`, and materializes the package server-side.

`POST /plugins/review_requests` now takes content one of **three** mutually
exclusive ways:

| input | who uses it | what happens |
| --- | --- | --- |
| `parse_task_id` | skill "发布新版本" with a fresh zip | the package is built server-side from the completed parse task |
| `manifest_json` + `plugin_json` | connectors, experts, expert teams, and skill text edits with no reupload | declared JSON, canonicalized as in item 18 |
| neither | `kind=first` only | the private draft row is snapshotted |

`parse_task_id` together with `manifest_json` is a 400
(`details.field="parse_task_id"`, `reason="mutually_exclusive_with_manifest_json"`).
The browser picks one door; sending both would leave which one wins to the
server, and the two produce different sidecars.

The materialization reuses `buildImportedSkillWrite` — the same code
`/plugins/import` runs — so the zip is size- and SHA-256-verified, frontmatter is
rewritten, and binary/oversize files are spilled to content-addressed keys in the
Space's managed prefix through `buildSkillAttachmentTree`. Nothing about archive
safety is reimplemented on this path.

`parse_task_id` is **skill-only**: a parse task is the product of a skill zip
upload and nothing else. Supplying one for a connector/expert/expert_team is a
400 (`reason="only_valid_for_skill_plugins"`).

**Identity is pinned to the live row, not to the zip.** `importFields` is built
from the existing plugin (`plugin_name`, tags, category, icon), because this
endpoint reviews CONTENT, not market metadata — the same rule a package-only
reupload already follows. A zip whose declared name disagrees with the live row is
rejected up front (`reason="name_mismatch"`) rather than failing later inside
`CanonicalizeManifest`, where the message would not say which side was wrong.

**Consume before materializing, release on failure.** `MarkParseTaskConsumed` is
the same optimistic CAS `Import` uses and runs *before* the download, so two
concurrent submits of one task cannot both proceed. Materialization or insert
failure then releases the task (best-effort, detached context) and deletes the
objects the attempt uploaded. A failure *after* the row commits deliberately
releases nothing: the request is persisted and the keys it references are live.
`skill_id` is passed as `""` because the "发布新版本" upload is not bound to a
plugin — binding happens at approve, not submit.

### 22. `attachment_keys_json` IS now frozen. Item 4's last paragraph is superseded.

Migration `20260901-01-plugin-review-attachment-keys.sql` adds a JSON-NULL
`attachment_keys_json` to `plugin_review_requests`, mirroring
`plugins.attachment_keys_json` exactly. Without it, approving a zip-submitted
review would apply a frozen package whose `storage_uri` paths the live row's
sidecar had no entry for — the reviewer approves one set of bytes and the org
receives another, or a 404.

The sidecar is now part of the snapshot on every path: the zip path stores the
keys it just uploaded, the declared-JSON path stores the live row's keys, and the
draft-snapshot path clones them. `ApproveReview` writes
`plugins.attachment_keys_json` from the frozen value instead of leaving whatever
the live row held.

Declared-JSON submissions still may **not** introduce or change a storage
attachment — that is a 400 (`field="plugin_json"`,
`reason="storage_attachment_change_requires_zip_upload"`), because a raw upsert
cannot mint storage content either, and the object lifecycle belongs to the
import path. The zip path is how storage content changes.

### 23. Validation errors name the field, not `body`.

`ReviewFieldError{Field, Reason, Err}` is a typed error the review service
returns and `writeServiceError` unwraps with `errors.As` into
`details: {field, reason}`. Previously every cause — bad version label, overlong
changelog, half a document pair, an unparseable manifest — collapsed to
`{"field":"body","reason":"invalid"}`, which told the browser nothing it could
put next to an input. Reasons in use: `invalid`, `too_long`,
`manifest_and_package_required_together`,
`mutually_exclusive_with_manifest_json`, `only_valid_for_skill_plugins`,
`name_mismatch`, `invalid_or_consumed`, `already_consumed`, `invalid_package`,
`storage_attachment_change_requires_zip_upload`. The wire code stays the fixed
`VALIDATION_ERROR`; only `details` gained precision.

`SubmitReview` also declares `413` now, because the zip path can exceed the
upload bound.

### 24. An unrecognized card decision is a 400 to the DLQ, not a 200 `forbidden`.

`ErrCardBadDecision` splits off from `ErrReviewInvalid`. The distinction is the
same reliability argument as item 13, one level up:

- **`ErrCardBadDecision`** — a decision value outside `approve`/`deny`, or a
  missing `event_id`/`operator_uid`/`review_id`. A well-formed card always
  carries these, so this is permanent protocol drift between marketplace and
  octo-server. It returns **400**, which octo-server routes to the DLQ where
  `tools/card-action-dlq` makes it visible. The old behaviour answered
  `forbidden/pending` — a 200 that octo-server acks and never redelivers — so
  every admin whose click hit a vocabulary mismatch saw 无权限 and the event was
  silently destroyed.
- **`ErrReviewInvalid`** — a handled refusal, e.g. the operator is no longer a
  reviewer. Still **200** with `{"disposition":"forbidden","state":"pending"}`,
  because the card should render "no permission" and stop retrying.

### 25. Reject and cancel garbage-collect the submission's objects.

`RejectReview`/`CancelReview` now return `(frozenKeys, liveKeys, error)` — both
sidecars read inside the decision transaction — and
`cleanupOrphanedReviewObjects` deletes every frozen key the live row does not
reference at the same path. Keys both sides share are content-addressed and still
in use. This runs on a detached context and swallows errors: a storage failure
must never roll back a committed decision.

IM-sourced rejects deliberately skip the GC. **SUPERSEDED by item 34.** Reaching
the frozen sidecar means loading the snapshot, which would add a round trip to
every IM deny; the orphans are content-addressed and deduped across versions,
so the leak is bounded. **CORRECTED in item 34** — the "extra round trip"
justification was false (`RejectReview` already returns both sidecars out of
its transaction; the GC is just a call), and `DecideReviewFromCard` now wires
it (`internal/service/plugin/review.go:906`). The original line is kept above
for the audit trail; the shipped behavior is the one item 34 describes.

**A consumed parse task stays consumed after reject or cancel.** Releasing it
would let the exact bytes a reviewer rejected be resubmitted without a fresh
upload, and the release is not a CAS against the request, so a quick
upload-and-resubmit could race it and release the *new* task. Retrying means
uploading again — which is also what the import path does for a deleted plugin.


## Review response (PR #74, 2026-09-02)

### 26. Space-admin self-submit does NOT auto-approve. Declined deliberately.

The state-machine section originally read "Space-admin self-submit auto-approves".
The shipped code inserts a `pending` request for every applicant, admin or not,
and that stays.

Auto-approving on submit means one admin action silently swaps live, org-visible
content with no second step and no confirmation — an accidental-publish footgun on
the one path where reviewing content is the entire point. The thing it saves is a
single click: an admin approving their own submission is already permitted and
works today, from the review queue or the IM card, and it produces exactly the
same row (`reviewer_uid` = self, `decision_source=web|im`) the auto-approve was
supposed to produce for audit uniformity. So the uniformity argument is satisfied
by the explicit path, and the risk argument only cuts one way.

Consequence: an admin who submits sees their own request in the pending queue.
That is intended — it is the confirmation step.

**Named residual: self-approval is PERMITTED, and nothing refuses it.**
`Service.ApproveReview` gates on `isReviewer(caller)` and nothing else; the repo's
locked read carries `review_id`, `status='pending'` and `space_id` but no
`applicant_uid` predicate; the IM path checks only `*role >= SpaceRoleAdmin`. So a
Space admin or owner can approve their own submission on either surface. Stated here
because the rest of this design went the other way — self-delist was removed, and
round 15 added the Delete gate on the reasoning that "a plugin the org depends on
cannot vanish at its author's discretion" — and listing is the mirror operation.

It is not refused, and the reason is not oversight: `Publish` routes `space` intent
through review rather than listing directly, so refusing `applicant_uid ==
reviewer_uid` would leave the ONLY admin of a single-admin Space permanently unable
to list their own plugin. A workable version of that rule needs a fallback (a second
reviewer role, an org-level approver, or an explicit single-admin exemption), and
that is a product decision rather than a patch. Until then the honest reading of
"listing to the org requires admin approval" is "requires an admin decision, which
may be the applicant's own" — recorded, audited (`reviewer_uid` = self,
`decision_source`), and reversible by Delist.

### 27. Notification metrics are deferred; the paths log.

The acceptance criteria ask the notify failure path to "log AND increment a
metric" and the success path to record delivered/filtered counts "via
logs/metrics". Only the logs shipped.

There is no in-process counter registry to hang a metric on. `internal/repository/metrics`
is a MySQL table of per-resource business counters (view/install/download) that
the market list reads back — not an operational metrics surface — and the module
pulls in no Prometheus/OTel/expvar dependency. Adding one for three counters would
mean introducing a metrics subsystem, an exposition endpoint, and its scrape/auth
story in a review PR, which is out of proportion to the change under review.

What ships instead: `dispatchReviewCard` logs `review: approval card dispatched`
with `delivered`/`filtered` counts on success, `review: approval card reached no
admin` (WARN) when the roster resolved empty, a per-target WARN for each filtered
recipient, and `notify_best_effort_failed` (WARN, with the error) for a dispatch
that failed outright — so every outcome the metric would have counted is
queryable from logs today. On the callback side `card_action_decided` (INFO)
carries `disposition`/`state` for every handled decision, and the 401 paths now
emit `card_action_unauthorized` (WARN) with a fixed `reason`
(`stale_timestamp` / `bad_signature` / `event_id_mismatch`) — they used to be
silent, which is the one refusal octo-server does NOT route to the DLQ, so a
rotated secret or a clock skew looked exactly like no IM traffic at all. The
reason label is the only thing logged there: pre-verification bytes are not
trustworthy enough to put in a log line.

**Amended acceptance criterion:** logs emitted for dispatch outcomes (delivered /
filtered / no-admin / failure) and for card-action decisions and auth failures;
metrics deferred to a follow-up once the notify surface has a counter registry.
`dispatchReviewCard` carries a TODO naming the missing counters
(`review_card_dispatch_delivered_total`, `_filtered_total`, `_errors_total`, and
the card-action decision/auth-failure counters) so the follow-up does not have to
rediscover them.

### 28. The review-submit body cap equals the /plugins/upsert cap.

`maxReviewBodyBytes` is now literally `= maxBodyBytes` (3 MiB), not its own
number. The 64 KiB it used to be was correct when a submit was "a handful of short
strings", and wrong the moment item 18 made the submit carry the full declared
manifest and package: `parse_task_id` exists only for skills, and a direct edit of
a listed plugin is 409 (item 19), so a connector / expert / expert_team whose
content crossed 64 KiB had NO path to a new version — a 413 with no workaround.

Both constants carry a comment pointing at the other. Tests
`TestSubmitReviewAcceptsAnUpsertSizedBody` (asserts the equality AND drives a
256 KiB body through the handler) and `TestSubmitReviewRejectsBodiesPastTheSharedCap`
(the cap is still a cap, 413 with `PAYLOAD_TOO_LARGE`) hold the invariant.

`maxCardActionBody` stays 64 KiB: an IM callback body is a decision plus a couple
of ids, sized by octo-server's own limits, and it is hashed before parsing.


## Listing model (marketplace backend, 2026-09-02)

Everything above was written when `visibility` alone decided marketplace
presence. It no longer does. The items below record the two-axis model that
replaced it, the publish/delist semantics built on it, and the version-label rule
that came with them. Same rule as the section above: **what shipped wins**, and
each item is a decision rather than an oversight.

### 29. Two axes: `visibility` declares intent, `listing_state` decides presence.

`migrations/sql/20260902-00-plugin-listing-state.sql` adds
`listing_state ENUM('draft','published','delisted') NOT NULL DEFAULT 'draft'`
after `visibility`. Before it, `private` doubled as "draft", so saving a plugin
and publishing it were the same act — the moment an author created a row it was
already in their own marketplace grid, and there was no way to say "org-visible,
but not yet".

`visibility` now means WHO should see the plugin once it is listed. `listing_state`
means WHETHER it is listed. They are independent, and the separation is what the
rest of this section is built on.

**On read, two different predicates, deliberately not one.**

- `visibilitySQL` (`internal/repository/plugin/repo.go:70`) is the SCOPE rule and
  gates `listing_state` **only inside the `space` disjunct**:
  `(p.visibility IN ('public','system') OR (p.space_id = ? AND ((p.visibility = 'space' AND p.listing_state = 'published') OR p.owner_uid = ?)))`.
  The owner disjunct is deliberately NOT gated — an author has to read their own
  draft in order to edit and publish it. The public/system disjunct is not gated
  either (item 36). `'published'` is a literal, not a placeholder, so the nine
  call sites embedding the constant keep their argument lists.
- `listedSQL` (`internal/repository/plugin/read.go:143`) is the GRID rule,
  ` AND p.listing_state='published'`, and is a separate concern: it is what makes
  a draft absent from the market grid *including for its own author*, which is the
  only thing distinguishing "save as draft" from "publish" to the person who just
  pressed one of them. `buildListQuery` appends it on the non-`Mine`, non-`AllSpaces`
  path; `ListTags` and `ListPlacementCategories` append it too, or a chip filters
  to an empty grid and a category count overstates its page (item 37's facet
  tests). `Mine` (我的发布) and `AllSpaces` (admin) omit it on purpose — both manage
  rows in every state.

**On write, the column moves at exactly six SQL sites**, and no ordinary tenant
save is one of them: `insertPlugin` (`write.go:125`, taking whatever the service
stamped — `buildWrite` mints `draft`, while `AdminCreate` and the admin container
import re-stamp `published`, because the admin surface has no draft step and the
create IS the publish), the `ResetListingToDraft` branch of `Repo.Update`
(`write.go:534`, item 31), `ApproveReview`'s `isFirst` branch (`review.go:435`),
`promoteEmbeddedChildren` (`review.go:597`), `PublishPlugin` (`listing.go:92`) and
`DelistPlugin` (`listing.go:185`).

Two notes for a reader of the surrounding comments. The doc comment on
`model.Plugin.ListingState` says the column is written by "exactly four paths" and
that "the UPDATE statements omit the column entirely" — that undercounts: the
`ResetListingToDraft` branch does write it from `Repo.Update`, and
`promoteEmbeddedChildren` writes it from inside the approve transaction. The
narrower claim it is reaching for — *a content-only save cannot change the listing
state* — does hold. And the DB DEFAULT is `draft` on purpose: `RebuildGraph`
re-inserts a container's children, so a `published` default would silently list a
draft container's children (item 35).

Grandfathering: `UPDATE plugins SET listing_state='published' WHERE deleted_at IS NULL`.
Every live row keeps exactly the reach it had. Soft-deleted rows are deliberately
left `draft` so a manual undelete cannot republish something the org had already
lost sight of. `draft` is therefore a purely new state no existing row can be in.

### 30. `display_status` is derived at read time, and the review half is owner-only.

`Plugin.DisplayStatus(hasPendingReview, latestReview)` (`internal/model/plugin.go:88`)
is the single status a client renders. It folds the listing axis (`listing_state`,
on the plugin) with the review axis (`plugin_review_requests`, a separate entity),
in this precedence:

1. an open request wins outright — including for an already-listed plugin whose
   NEW version is in review, because showing 已发布 there hides that the author is
   waiting on somebody;
2. `delisted`, then `published` — what the listing axis says;
3. a `rejected` latest review, so an author sees why their draft is not live;
4. otherwise `draft`. A CANCELED latest review lands here on purpose: withdrawing
   a request returns the plugin to 草稿 rather than leaving a "withdrawn" state.

It is never stored. Storing it would be the collapse item 26 forbids — a listed v1
with an in-review v2 is two simultaneous facts and one column cannot hold both —
and the vocabulary is marketplace-only, never handed to `cardState`.

The two inputs are populated in exactly two places, and both are effectively
owner-scoped:

- the mine listing — `ListFilter.IncludeReviewState`, set as `IncludeReviewState: p.Mine`
  (`service.go:290`), which appends three correlated subqueries
  (`pluginReviewStateColumns`) rather than a join;
- `Service.Detail`, which calls `LatestReviewForPlugin` **only when
  `p.OwnerUID == caller.UID`** — everyone in the Space may read a listed plugin,
  but only its author has any business knowing an update is sitting in the queue.

Everywhere else both fields are zero, so `display_status` collapses to the listing
axis alone. `LatestReviewForPlugin` orders pending first, so one lookup answers
both "is a request open" and "what happened last".

### 31. Publish is one button; the row's declared visibility picks the branch.

`Service.Publish` (`internal/service/plugin/listing.go`) takes no "submit for
review" flag and no content. The caller says "publish this" and the plugin's
declared visibility decides what that means:

- `private` → listed immediately (`PublishPlugin`). There is no org audience to
  protect and no review channel for a row nobody else can read.
- `space` → a pending review request (`SubmitReview`); the plugin stays a draft
  and `ApproveReview` is what lists it.
- anything else → `ErrInvalidRequest`. `system` cannot reach here.

Putting that rule in the service rather than the client is the point: a browser
that guesses wrong either lists something unreviewed or strands a plugin in draft
forever. Guards, in order: non-owner and embedded both answer `ErrNotFound` (a
non-owner must not learn the plugin exists); already-published is
`ErrAlreadyPublished`, not a no-op, because a second press means the client's view
is stale; a pending request is `ErrReviewPending`.

`Repo.PublishPlugin` **re-derives `private` from the FOR UPDATE-locked row**, not
from the service's decision. The service decided from an unlocked read several
round trips earlier, and between the two the owner can raise the draft to `space`
through an ordinary upsert (legal on a draft) — the UPDATE would then stamp
`published` onto org-visible unreviewed content with no review request ever
created. `ApproveReview` re-derives `isFirst` the same way and for the same reason.
The check is restated in the CAS predicate, which is
`... AND visibility=? AND listing_state<>'published'`. Zero rows is `ErrConflict`
via `mustChangeState`, deliberately not `ErrNotFound`: after a successful locked
read the row provably exists, so reporting "not found" would 404 a plugin the
caller is looking at.

Publish mints no version — every save already snapshots one, and
`plugin_versions.version` is a per-plugin counter (item 3). It does self-heal the
default placement forward (`ensureVisibleDefaultPlacement`), for the same reason
approve does (item 1 as amended): listing promises "this is in the market now",
and a row with a missing or hidden placement is not.

The complementary rule lives in `Service.update`: **widening the declared audience
of a listed plugin un-lists it** (`ResetListingToDraft`). A published private
plugin is editable by design, but writing `space` onto it while it stays published
would list it org-wide with no review. The condition in code is "the visibility
changed", not "it widened" — for a tenant the widening one is the only change that
can reach there (published+space is already refused with `ErrListedRequiresReview`),
and for the exempt system admin (item 36) dropping to draft on a narrowing is the
safe direction anyway.

### 32. Delist is moderation of ORG content, and both refusals are `ErrNotFound`.

Space admins only, using the SAME predicate as approve and reject (`isReviewer`):
taking something down is the same authority as putting it up, and a second notion
of "who moderates this Space" would drift from the first. The author deliberately
cannot self-delist, so a plugin the org depends on cannot vanish at its author's
discretion. The row is locked by SPACE (`getReviewedPluginForUpdate`), not by
owner, because the actor is by definition not the author.

`Repo.DelistPlugin` then refuses two shapes from the locked row, **both with
`ErrNotFound`**:

- **an embedded child.** It is listed and un-listed by its container —
  `ApproveReview` promotes the whole graph in one transaction. Taking one child
  down leaves the container published while a member it declares is hidden, and
  every non-owner install of the PARENT then fails with `ErrDependencyHidden`: a
  takedown of one row that silently breaks another.
- **anything whose declared visibility is not `space`.** This one is the security
  refusal and the reason the error is not distinguishable. `visibilitySQL` admits
  a private plugin only to its owner, so an admin who cannot GET the row would
  learn it exists by successfully delisting it — an existence oracle, and a way to
  interfere with a colleague's private plugin at will. `ErrNotFound` keeps the
  answer identical to the read the same admin is allowed to make. This is the
  CLAUDE.md rule "Cross-Space failures must not leak resource existence" applied
  within a Space, where the boundary is ownership rather than tenancy.

Both guarantees are restated in the CAS predicate — `... AND listing_state='published'
AND visibility=? AND is_embedded=0` — exactly as publish carries its visibility
check into the UPDATE, so the guarantee survives if the statement is ever reached
without that read in front of it. A CAS miss is `ErrConflict`, which the service
maps to `ErrNotPublished` (409); a plugin in another Space never reaches the CAS
and stays a 404 even for a real admin of that other Space.

The transaction also **cancels any pending request** (item 34's helper): a request
on a plugin that just left the market has nothing left to decide, leaving it open
would let a later approval silently relist the plugin behind the admin's back, and
canceling releases the single-pending slot so the author can edit and resubmit.

Placements are deliberately untouched — hiding them would also hide the plugin
from its own author's 我的发布, which shares the placement join, and `listing_state`
already removes it from every other reader.

Deliberate consequence, stated in the code and worth repeating: **a published
PRIVATE plugin has no takedown path at all.** It needs none (published+private is
"listed to its owner alone"), and the owner can still drop it back to a draft by
widening the visibility, which un-lists it.

### 33. Version labels are `x.y.z` and forward-only; the exemption is byte-equality with the STORED label.

`versionPattern` is `^\d{1,9}\.\d{1,9}\.\d{1,9}$`. The old pattern accepted any
identifier-ish string, which is how `v999`, `1.0.0lll` and `oooo1.0.0` reached
production — none of them can be ordered against another, so "the version may only
go up" was unanswerable.

**Client-facing behavioral break, undisclosed until this line.** Tightening from
`^[0-9A-Za-z][0-9A-Za-z._+-]{0,63}$` to three numeric parts means a client that
sends `1.0`, `v1.2.3`, `2.0.0-beta.1`, a SHA, or any other previously-legal
identifier as a **new** label gets a 400 (`VALIDATION_ERROR`,
`details.field=version`). Existing rows are grandfathered (see below): if they
echo back their stored label byte-for-byte the write goes through, which is what
octo-web's fetch-edit-save does today, so the steady state is not broken. But a
client that ever computes a label (semver pre-release, date stamp, git SHA) is
now rejected at write time, and `make openapi-diff` does not see it because the
wire schema for `version` is still `type: string`. It is a semantic break, not
a structural one, and was not in the original acceptance criteria.

**Why the grandfathering exemption exists.** Tightening the format alone would
strand every row already carrying such a label. Every UI here is fetch-edit-save
and echoes `version` straight back (octo-web's edit modal seeds the input from
`current_version`, and its patch bump returns the label unchanged for anything with
fewer than three dot-parts), so a legacy-labelled row would 400 on every save —
**permanently**, because the only way to correct the label is a save.

The exemption is `WriteRequest.grandfatheredVersion`: unexported on purpose, set
only from `*old.CurrentVersion`, never from a request body. `buildWrite`
(`service.go:765`) accepts a malformed label only when it is byte-equal (modulo
surrounding space) to that stored value; **a malformed NEW label is still
refused**. `isStoredVersionLabel` (`import.go:264`) is the import path's copy of
the same byte-equality, likewise read from the stored row so a caller cannot
smuggle a malformed label past the format gate by asserting it is grandfathered.

Where the two rules now live:

- `Service.update` — the tenant upsert. Both checks sit **above** the
  `IsSystemAdmin` branch, so a super-admin on `/plugins/upsert` is bound by
  forward-only too.
- `applyStoredVersionRules` (`admin.go:229`) — the admin twin, called from
  `AdminUpdate` and from `adminImportConsumedTask`. Neither route had either rule
  before; that was an omission, not an exemption. Its ordering check is gated on
  the submitted label being well-formed, which is the one way it reads differently
  from the tenant copy — the set of accepted writes is identical, only the
  attribution differs (a format problem should be reported as a format problem,
  not as "use a higher version").
- `resolveImportFields` (`import.go:231`) — the skill import/reupload path, with a
  three-way split: a caller-SUBMITTED label is validated (they can fix it) unless
  it is the row's own stored one; a PACKAGE-derived label that no longer parses
  falls back to `defaultCurrentVersion` (`"1.0.0"`) rather than failing the whole
  upload over a field the author never typed and cannot edit without repackaging.
  The refusal is a `*ReviewFieldError{Field: "version"}`, not a bare
  `ErrInvalidRequest`, and it runs **before** the parse task is consumed so the
  upload stays retryable.

**The real stakes, precisely, because the obvious guess is wrong.**
`plugin_versions.version` is a per-plugin auto-increment counter, not this label
(item 3), so a bad label corrupts no snapshot and collides with nothing there. The
damage is on the review axis: `SubmitReview` refuses a submission that is not
`versionNotRegressed` against `plugins.current_version`, and
`publishedVersionLabels` folds `current_version` into the set of labels the org has
already seen for every non-draft row. Dropping a listed plugin from `2.0.0` to
`1.5.0` therefore **re-opens the entire range below an already-approved label** —
the next reviewed upgrade can land at `1.6.0` and every installer watches the
plugin go backwards.

`versionNotRegressed` refuses a malformed NEXT first and unconditionally (checking
CURRENT first would let one legacy value wave another through), and treats an
unorderable CURRENT as blocking nothing — which is what preserves the admin
surface's data-repair role: correcting a stranded `v999` to a real label still goes
through. What stays refused everywhere is a downgrade between two WELL-FORMED
labels. That is a real if rare need with no route left, and the trade is taken
deliberately: refusing fails loudly with a 400 naming the version field, while
allowing it fails silently for every consumer of the plugin. A genuinely stuck
well-formed label needs a DB fix.

### 34. Removing a plugin from circulation cascade-cancels its pending review.

`cancelPendingReviewFor` (`review.go:856`) settles a plugin's pending request
inside the caller's transaction. Four call sites: `Repo.Delete`, `Repo.DeleteGraph`
(the top), `softDeleteRebuiltChild` (each embedded child) and `DelistPlugin`.

**Why a cascade is the only fix that reaches the row.** Once `plugins.deleted_at`
is set, the request outlives its plugin in a state nobody can leave, in both
directions at once: `ListReviewRequests`, `GetReviewRequest` and
`LoadReviewSnapshot` all carry `p.deleted_at IS NULL`, so neither the applicant nor
a reviewer can even SEE the row; and `CancelReview`, `ApproveReview` and
`RejectReview` each load the plugin through `getReviewedPluginForUpdate`, which
refuses a deleted one — so the applicant's own cancel answers "plugin not found".
Relaxing any single one of those would be dead code, because the request is
undiscoverable before it is unsettleable.

The UPDATE is a CAS on `status='pending'`, not a blind write, so a request another
transaction just approved or rejected keeps the decision it actually got, with its
reviewer and reason intact. Zero rows is the ordinary case and is not an error.
Reasons are stored on the row: `plugin delisted by a Space admin`, `plugin deleted`.

Placing the cascade at `softDeleteRebuiltChild` rather than at its two callers is
deliberate: it makes the invariant structural — no path that soft-deletes a
`plugins` row leaves a pending request pointing at it — and it also covers
`RebuildGraph`'s replaced children, which are removed exactly as permanently.
Reaching a child's request through the service requires a row that became embedded
*after* submitting (`SubmitReview` refuses `is_embedded`), so in practice it matches
zero rows; that is the point.

**Soft-delete semantics, and why delete collects no storage objects.** The
`plugins` row, its whole `plugin_versions` history and its live attachment sidecar
all survive, and nothing in this service ever deletes a plugin's own objects —
neither `Service.Delete` nor the admin delete touches storage. Collecting only the
canceled submission's spill would make delete the single place a soft delete
performs an irreversible external side effect, on the smallest slice of what it is
knowingly leaving behind. The asymmetry settles it: not collecting is recoverable
(every row a later sweeper needs survives the soft delete, and
`retainedAttachmentKeys` is not filtered by `deleted_at`), collecting is not. It
would also prejudge whether `deleted_at` means "recoverable" — and the migration
already fails closed against a manual undelete, which is not the posture of a
codebase that treats a soft-deleted plugin as destroyed.

Delist's identical non-collection rests on a **different** argument: the author
never abandoned that submission, a third party's takedown canceled it as a side
effect, and they are expected to edit and republish. Do not merge the two
justifications; they would not survive the same change.

Contrast, changed in this branch: an **IM-sourced reject now DOES collect**.
`DecideReviewFromCard` wires `cleanupOrphanedReviewObjects` from the two sidecars
`RejectReview` already returns out of its own transaction. The comment that
justified leaving them behind claimed the GC would cost an extra round trip, which
was false — so the same decision leaked or did not leak depending on which button
the admin happened to press.

### 35. A container rebuild carries the listing state forward with visibility.

`ReuploadContainer` stamps the top and every child from an UNLOCKED pre-parse read;
`RebuildGraph` then re-stamps `visibility` / `listing_state` / `space_id` /
`owner_uid` from the row locked with FOR UPDATE, so a concurrent edit, delist or
approve during the multi-second parse wins over the pre-parse view. The top's
`listing_state` is preserved by omission from its UPDATE; every child is freshly
inserted, so all four fields are re-stamped there.

Children must agree with the top in both directions: a child left `draft` under a
published top makes `resolveInstallDetail` resolve fewer visible targets than
`CountDeclaredRelations`, so every non-owner install of the container fails with
`ErrDependencyHidden` — silent until somebody other than the author tries it; and a
child `published` under a draft top is independently readable when it should not be.

### 36. Out of scope and named residuals of the listing model.

- **A super-admin can edit an already-listed row's live content with no review
  request and no `review_approve` audit row.** **CORRECTED.** The role set that
  holds this escape hatch is `superAdmin ∪ marketAdmin` (NOT "super-admin only"
  as an earlier draft had it): every `/api/v1/admin/plugins` group is mounted
  with `adminAuth.Handler(marketmiddleware.RoleMarketAdmin)`
  (`internal/api/handler/plugin/admin.go:69`), and
  `middleware/admin.go:142-152` admits `RoleSuperAdmin` unconditionally on every
  group plus any `alsoAllow` role the group names. Both roles reach tenant-owned
  Space-reviewed rows in every Space through the admin surface's `adminScope`,
  and only the ordinary `update` audit row records the change (no
  `review_approve`). This is the platform-operator escape hatch and is kept
  deliberately. It is narrower than it looks and the boundary is worth stating:
  neither role can LIST anything through `/plugins/upsert` (`listing_state` is
  not settable on the tenant write path, and changing visibility on a
  published row still drops it to draft), and `AdminUpdate` stamps
  `old.Visibility` via `adminEffectiveWrite` rather than taking it from the
  request. It is a known residual, not an oversight — and see item 37 for the
  fact that no test currently pins that boundary.
- **`system` rows have no listing lifecycle.** `visibilitySQL`'s public/system
  disjunct is deliberately not gated on `listing_state`. A `system` row is
  admin-owned, reaches every Space and has no per-Space listing lifecycle; gating
  it would make every admin-created connector and global skill vanish the moment a
  write path forgot to stamp `published`. Admin creates do stamp it, so the value
  stays consistent, but nothing depends on that. A future 全平台可见 tenant flow
  would have to extend this — it is not extended today, on purpose.
- **No takedown path for a published private plugin** (item 32).
- **Delist is an existence-oracle divergence from the nine-surface list above.**
  The nine surfaces in the next item's table are all driven through
  `visibilitySQL` and answer identically ("not found") for an unknown id, a
  colleague's private draft, and a colleague's `space`-intent draft. `Delist`
  answers differently: a `draft`+`space` row that the caller CAN see as a
  Space reviewer makes the CAS UPDATE match zero rows against
  `listing_state='published'`, so `mustChangeState` returns `ErrConflict`,
  which `service/plugin/listing.go:177` maps to `ErrNotPublished` → a 409
  carrying `not_published`. A private draft or unknown id reaches the
  `visibility != 'space'` guard or the `getReviewedPluginForUpdate` miss and
  returns `ErrNotFound` → 404. That lets a Space admin distinguish "a
  `space`-intent plugin exists in this Space and is not published" from "no
  such row in scope" — a tenth surface that was missed when the nine-surface
  oracle argument was written. It is judged acceptable because (a) only a
  Space admin can delist, (b) they can already see `space`-intent drafts in
  their 我的发布 if they are the author or in the review queue if a request is
  open, and (c) the alternative (returning 404 for an unpublished-but-in-scope
  row) hides a state the admin is acting on; but it is a real divergence from
  the "every refusal is 404" claim item 32 makes and should be recorded as
  such rather than papered over.
- **No object GC on delist or delete** (items 32, 34). The canceled submission's
  spilled objects leak until some future sweeper collects them; bounded by one
  submission per takedown, and content-addressed. Reclaiming them is one deliberate
  decision about plugin-delete object GC — live sidecar, version sidecars and any
  frozen submission together — not a fragment bolted onto a cascade.
- **No backfill of review requests for grandfathered rows.** The existing
  out-of-scope bullet still holds; `publishedVersionLabels` covers those rows by
  folding `current_version` into the already-seen set for any non-draft plugin.
- **No undelete.** There is no undelete path in this repository, and the migration
  fails closed against a manual one by leaving soft-deleted rows `draft`.
- **`listing_state` must never acquire review vocabulary.** No `pending`, no
  `rejected` — that is exactly the collapse item 26 forbids, and 审核中 is derived
  (item 30), not stored. The migration comment says so and a migration test holds
  the enum at those three values.

### 37. Acceptance criteria for items 29–36, as what the tests actually prove.

Every `internal/db` test below is a real-MySQL integration test and **skips**
without `TEST_MYSQL_DSN`; a green `go test ./...` on a machine without it proves
none of them.

- **Scope (the security half).**
  `TestSpaceIntentDraftIsInvisibleToTheSpaceButVisibleToItsOwner` drives a
  `space`-intent draft through nine surfaces — `Get`, `List`, `List(mine)`,
  `GetWithRelations`, `ListTags`, `ListTags(mine)`, `ListVersions`,
  `ListPlacementCategories`, and the WRITE path via `lockRelationTargets` (a
  colleague trying to adopt the draft as a relation target of their own published
  expert) — and asserts a same-Space colleague reaches it through none of them,
  while the owner reaches it through the ones where that is meaningful. It is
  table-driven precisely so a tenth call site added without `visibilitySQL` fails
  here; add new surfaces as cases rather than asserting on the constant's text.
  `TestCrossSpaceDraftLeakage` proves the parenthesis nesting: the owner disjunct
  lives INSIDE `space_id = ?`, so the owner acting in another Space gets
  `ErrNotFound`. `TestDelistedPluginLeavesTheMarketButStaysReadableByItsOwner`
  proves `delisted` behaves like a draft for everyone else while staying on its
  author's 我的发布. `TestHasPendingReviewIsScopedThroughThePlugin` proves the
  pre-check cannot be used to probe whether an arbitrary plugin has a review open,
  and that canceling releases it.
- **Facets agree with the grid.** `TestGridFacetsAgreeWithTheGrid` asserts from the
  AUTHOR's scope — the only scope where the two sets diverge — that
  `ListPlacementCategories`' `plugin_count` equals the number of rows `List`
  actually returns, and that a draft-only tag is absent from the grid's chips,
  while `ListTags(mine)` still carries it with an every-state count.
- **Publish / delist transactions.** `TestPublishImmediatelyListsAPrivatePlugin`
  proves, across a healthy / hidden / missing default placement, that publish ends
  with exactly one visible default placement, leaves `visibility` at `private`
  (仅自己可见 + 已发布 is a real state), mints **zero** `plugin_versions` rows, and
  writes one `publish` audit row.
  `TestPublishRefusesAPluginThatBecameOrgVisibleMidFlight` (the locked re-derivation
  of item 31), `TestConcurrentPublishAndDelistProduceOneWinner` (exactly one nil
  and one `ErrConflict`), `TestDelistCancelsThePendingReviewAndFreesTheSlot`,
  `TestWideningVisibilityOnAPublishedPluginUnlistsIt`,
  `TestPublishedLabelsIncludeADelistedPluginsCurrentVersion`.
  `TestDelistRefusesRowsThatAreNotOrgContent` proves all three refusals return
  `ErrNotFound`, leave `listing_state` untouched and write **zero** delist audit
  rows — including the case that names the residual: **the admin's OWN published
  private plugin**. `TestDelistFromAnotherSpaceIsNotFound` proves the cross-Space
  answer is indistinguishable from nonexistence.
  `TestDelistRequiresTheReviewerRole` proves the authority is the approve/reject
  predicate, including that the plugin's own owner acting as a plain member cannot
  delist.
- **Derived status.** `TestMineListingCarriesTheDerivedStatus` proves the mine
  listing carries `display_status` without a client-side join, and pins the
  precedence that mattered most: a LISTED plugin with a pending upgrade reads
  `pending_review`, not `published`. `TestReviewQueueHidesRequestsForDeletedPlugins`
  covers the review-side read.
- **Version labels.** `TestAdminUpdateAcceptsTheRowsStoredLegacyVersionLabel`,
  `TestImportReuploadAcceptsTheRowsStoredLegacyVersionLabel` and
  `TestAdminSkillReuploadAcceptsTheRowsStoredLegacyVersionLabel` prove a
  fetch-edit-save round-trip of a stored `1.0` / `v1.2.3` / `2.0.0-beta.1` survives
  on each surface. `TestAdminUpdateStillRejectsANewMalformedVersion`,
  `TestImportReuploadStillRejectsALegacyLabelThatIsNotTheRowsOwn` and
  `TestAdminUpdateGrandfatheringIsNotCallerSupplied` prove the exemption is
  byte-equality with the STORED label and nothing wider, and that a rejected write
  never reaches the store. `TestImportFallsBackWhenThePackageDeclaresALegacyVersion`,
  `TestImportRejectsACallerSubmittedLegacyVersion` and
  `TestImportUsesTheSubmittedVersionOverALegacyPackageLabel` pin the
  package-derived vs caller-submitted split, and that the guard runs before the
  parse task is consumed. `TestAdminUpdateRefusesAVersionThatMovesBackwards` and
  `TestAdminSkillReuploadRefusesAVersionThatMovesBackwards` prove forward-only now
  holds on both admin routes (and that the reupload releases its task, so the
  upload stays retryable). `TestAdminUpdateStillRepairsAnUnorderableStoredLabel`
  proves the repair path — a stranded legacy label corrected to a real one — still
  goes through.
- **The wire shape of the version field error.**
  `TestImportFieldErrorNamesTheVersionInputInTheEnvelope` proves the import handler
  renders a `*ReviewFieldError` as `VALIDATION_ERROR` with
  `details.field=version`. Scope note: it drives a **fake service** that returns
  the error, so it pins the envelope wiring on a route that does not otherwise go
  through the review path — it does NOT prove `resolveImportFields` produces that
  error. That half is `TestImportRejectsACallerSubmittedLegacyVersion`.
- **Delete cascade.** `TestDeleteCancelsThePendingReviewRequest` proves the request
  becomes `canceled` with `reviewer_uid`, a reason and `reviewed_at` set; that a
  second plugin's open request is untouched (the cascade is scoped, not a queue
  sweep); and that the frozen attachment sidecar survives.
  `TestDeleteDoesNotOverwriteAlreadyDecidedRequests` proves the CAS leaves an
  already-rejected request's reviewer and reason intact.
  `TestDeleteGraphCancelsPendingReviewsAcrossTheSubtree` proves both the top and an
  embedded child are canceled, with the fixture making the child embedded *after*
  its submission — the only way a real one gets there.
- **Object policy.** `TestDeletingAPluginCollectsNoObjects` proves delete destroys
  no storage object, neither the live sidecar's nor the submission's. It cannot be
  made red by reverting the cascade; it guards the opposite mistake, so wiring any
  object GC into the delete path turns it red — which is the signal to re-argue the
  comment rather than quietly change it. `TestIMDenyCollectsTheSubmissionsOrphanedObjects`
  proves an IM deny now collects exactly the orphaned key and keeps a key the live
  row still references; `TestIMDenyThatLosesTheRaceCollectsNothing` proves a lost
  CAS collects nothing, because the winner's outcome owns those objects.
- **Migration.** `TestPluginListingStateMigrationUpDownMySQL` proves up / down /
  re-apply, the fail-closed `draft` DEFAULT, the enum being exactly
  `('draft','published','delisted')` and no more, `listing_state` as the third
  column of `idx_plugins_scope_category_created`, the new
  `idx_review_plugin_submitted_at`, and the grandfathering (a live row becomes
  `published`, a soft-deleted one stays `draft`).
- **Migration replay (round 16).** MySQL implicitly commits every DDL, so
  sql-migrate's transaction protects nothing: a crash between the statements and the
  `gorp_migrations` insert replays the file. `TestMigrationsConvergeOnReplayAfterALostRecord`
  reproduces exactly that state — run Up, delete the six records, run Up again — and
  asserts both that it succeeds and that the schema fingerprint is unchanged. It
  fails with the real error codes against the pre-fix files (1060 on the two
  `ADD COLUMN`s, 1061 on the `ADD INDEX`), which is why the three are now guarded on
  `information_schema` rather than bare. **The DROP+ADD shape that makes
  `20260902-02` self-idempotent is NOT the fix for the other three** and was
  measured, not argued: it rebuilds an index that already exists, so its DROP always
  has a target, whereas applying it to a file that CREATES one fails with 1091 on
  every clean install (MySQL has no `DROP INDEX IF EXISTS`).
  `TestSubmittedIndexMigrationRemovesTheStaleDuplicate` covers the other collision:
  an environment that ran an intermediate branch head carries
  `idx_review_plugin_submitted` over the same columns, and `20260902-03` now drops it
  instead of promising a follow-up migration — which is also what makes its `Down`
  converge.
- **Stated gap, so nobody infers coverage from the list above.** The super-admin
  escape hatch in item 36 has **no test**. Nothing drives an `IsSystemAdmin` caller
  through `Service.update` against a published `space` row to pin either half of
  the claim — neither that the edit is allowed, nor that it cannot list the row.
  `TestSystemAdminIsAlwaysAReviewer` and the system-admin case of
  `TestDelistRequiresTheReviewerRole` cover the review/delist authority only. A
  reviewer who wants that boundary held has to ask for the test; do not read item
  36 as an asserted invariant.

---

## Invariant coverage table (round 16, 2026-09-03 — ships in the same commit as the code below)

Both reviewers flagged the recurring defect pattern "an invariant enforced in
three places and missed in a fourth". The table below records, for each
load-bearing invariant, every enforcement site I could verify in the shipped
code, whether that site holds a MySQL row lock (i.e., the predicate is
evaluated inside a transaction against a `SELECT ... FOR UPDATE` row and/or
restated in a CAS `UPDATE ... WHERE` predicate that makes a stale pre-check
harmless), and which surface reaches it: **T**enant, **A**dmin (`/api/v1/admin/*`),
or **IM** card callback.

**Anchoring.** This table was wrong in BOTH directions across two rounds — first
it named a real Delete gap the code then closed, then it kept asserting that gap
after the fix landed — so it is no longer pinned to a SHA that the next commit
invalidates. It is re-derived against, and committed together with, the code it
describes: the round-16 commit on top of `080fc75`. If you are reading it at a
later head, the rule is unchanged and worth keeping: re-derive a row before
trusting it, because a table that lags the code is worse than no table (it is what
the next change to this area gets planned against). What is different is that it no
longer lags by construction — a gap row closed by a commit is closed in the same
commit.

Legend:
- **lock = yes**  — site runs inside a transaction and either reads the
  relevant row `FOR UPDATE` or restates the predicate in a CAS `WHERE` clause
  (or both), so a concurrent writer cannot slip past it.
- **lock = pre**  — unlocked pre-check; advisory only. Correctness relies on
  an authoritative locked re-check downstream, or on the predicate being
  unreachable from a race.
- **lock = no**   — site decides the outcome without a lock and with no
  downstream re-check; a TOCTOU window exists if marked this way.

### 1. Forward-only version label (label may not move backwards)

| site | file:line | lock | surface | notes |
|---|---|---|---|---|
| Tenant upsert (pre-check) | `internal/service/plugin/service.go:592-593` | pre | T | Compared against `*old.CurrentVersion` from an unlocked `GetWithRelations`; sits ABOVE the `IsSystemAdmin` branch. |
| SubmitReview (service pre-check) | `internal/service/plugin/review.go:163-164` | pre | T | Against `detail.Plugin.CurrentVersion` from unlocked read. |
| InsertReviewRequest (locked re-derivation) | `internal/repository/plugin/review.go:154-157` | yes (`getOwnedForUpdate` at :109) | T | Submit's locked twin. |
| ApproveReview (locked re-derivation at apply time) | `internal/repository/plugin/review.go:464-467` via `getReviewedPluginForUpdate` at :448 | yes | T, IM | Authoritative for the moment the label is stamped onto the live row; covers the "admin moved current_version forward while request was pending" race. |
| `applyStoredVersionRules` (AdminUpdate) | `internal/service/plugin/admin.go:246-249`, called at :297 | pre | A | Service-layer pre-check against unlocked old. |
| `applyStoredVersionRules` (admin skill reupload) | `internal/service/plugin/admin_import.go:162-165` | pre | A | Same shape as AdminUpdate. |
| Tenant skill re-import (reupload) | `internal/service/plugin/import.go:298` → `s.update` → service.go:592 | pre | T | Relies on service.update pre-check. |
| Container reupload (`ReuploadContainer`) | — | n/a (trivially holds) | A | Container rebuild preserves `old.CurrentVersion` when no new label is declared; the archive-derived top gets `defaultCurrentVersion="1.0.0"` but `container.go:225-227` overwrites with the stored label, so no new label is ever introduced on this path. |
| Grandfathering (stored label always accepted) | `service.go:595` sets `req.grandfatheredVersion`; `service.go:811` lets it through; `admin.go:253-255` same; `import.go:264` (`isStoredVersionLabel`) same | yes when inside a tx | T, A | |
| `versionNotRegressed` / `model.VersionNotRegressed` shared impl | `internal/service/plugin/validation.go:128` and `internal/model/version.go:42` | n/a | n/a | Comment pledges one implementation across unlocked and locked sites. |
| `Repo.Update` locked re-derivation (`EnforceForwardOnlyVersion`) | `internal/repository/plugin/write.go` — beside the `EnforceListingGate` block | yes (`getOwnedForUpdate`) | T, A | **CLOSED (round 16).** Was the third of this table's three highest-confidence defects. Both update surfaces now set the flag (`Service.update`, and `AdminUpdate` — the reachable route, since it sets `SnapshotVersion` without `EnforceListingGate`), and the repo restates `model.VersionNotRegressed` against the locked `before.CurrentVersion`. Evaluated on the same two values as the pre-check, so a caller the pre-check exempted (no stored label, omitted version, legacy unorderable current) is exempt here for the same reason rather than by a separate rule. |

### 2. Published-label uniqueness (label reuse across approved versions)

| site | file:line | lock | surface | notes |
|---|---|---|---|---|
| Submit service pre-check — `versionNotRegressed` | `service/plugin/review.go:163-164` | pre | T | |
| InsertReviewRequest locked label-set | `repository/plugin/review.go:159-167` (`publishedVersionLabels` at :240) | yes (FOR UPDATE) | T | Set = every `plugin_versions.version_label` for this plugin plus `current_version` when non-draft. |
| ApproveReview locked label-set | `repository/plugin/review.go:489` | yes (FOR UPDATE) | T, IM | Re-derived against the live locked row to close the submit-vs-approve race. |
| `(plugin_id, version_label)` UNIQUE on `plugin_versions` | (schema) | yes (DB constraint) | T, A, IM | Last-resort guard; should never fire if the two checks above are right. |

### 3. Visibility enum on write ({system, space, private}; `public` retired on write)

| site | file:line | lock | surface | notes |
|---|---|---|---|---|
| Tenant create | `service.go:493` | pre → yes | T | `validVisibility(req.Visibility, false)`; `buildWrite` re-checks at `service.go:793`. |
| Tenant update | `service.go:600` | pre → yes | T | Same; `buildWrite` re-checks. |
| `buildWrite` (authoritative) | `service.go:793` | yes (inside tx) | T, A | Single chokepoint for the INSERT/UPDATE; admin path routes through it with `c.IsSystemAdmin=true`. |
| Legacy `public` normalization on admin update | `admin.go:132` (`NormalizeLegacyVisibility`) | pre | A | Rewrites a preserved `public` to `system` before buildWrite sees it; admin create paths set visibility directly to `system` via `conventionVisibility`. |

### 4. `listing_state` enum and mutability (`draft`/`published`/`delisted` only)

| site | file:line | lock | surface | notes |
|---|---|---|---|---|
| Column type + fail-closed DEFAULT | `migrations/sql/20260902-00-plugin-listing-state.sql` | n/a | n/a | `ENUM('draft','published','delisted') NOT NULL DEFAULT 'draft'`. |
| Migration test holds enum at three values | `internal/db/plugin_listing_state_migration_test.go` (referenced from item 37) | n/a | n/a | |
| INSERT stamps `draft` on tenant create | `service.go:490-493` → `buildWrite`; `write.go:148` inserts the stamped value | yes | T | |
| INSERT stamps `published` on admin create | `admin.go:141-146` | yes | A | Inert on admin UPDATE (UPDATE statement does not write `listing_state`, per admin.go:144 comment). |
| Tenant `ResetListingToDraft` (visibility widen on published) | `write.go:588-605` | yes (FOR UPDATE) | T | Only path by which a tenant write mutates listing_state to `draft`; fires only when the service has decided widen-to-space unlists. |
| ApproveReview `isFirst` sets `published` | `review.go:435` (approx) | yes (FOR UPDATE) | T, IM | |
| `promoteEmbeddedChildren` sets children to `published` | `review.go:597` | yes (under parent tx) | T, IM | |
| PublishPlugin sets `published` (CAS) | `listing.go:110-120` UPDATE with `listing_state<>? AND visibility=? AND owner_uid=?` CAS | yes (FOR UPDATE via `getOwnedForUpdate`) | T | |
| DelistPlugin sets `delisted` (CAS) | `listing.go:210-220` UPDATE with `listing_state=? AND visibility=? AND is_embedded=0` CAS | yes (FOR UPDATE via `getReviewedPluginForUpdate`) | T (Space admin) | |
| `demoteEmbeddedChildren` on delist | referenced from `listing.go:228` | yes (under delist tx) | T (Space admin) | |
| No ordinary tenant upsert writes `listing_state` | asserted in `write.go` (UPDATE statement omits the column except for ResetListingToDraft) | yes | T | |

### 5. Single-pending review per plugin

| site | file:line | lock | surface | notes |
|---|---|---|---|---|
| Generated-column UNIQUE `uq_review_plugin_pending` | `migrations/sql/20260901-00-plugin-review-requests.sql:66-77` | yes (DB) | T, IM | `pending_plugin_id` is NULL for non-pending rows, so the unique index admits one pending per plugin. |
| Submit pre-check (HasPendingReview) — submit itself inserts the pending row so no pre-check needed; Publish service pre-check | `service/plugin/listing.go:83` and tenant visibility-change guard `service.go:612` | pre | T | Fast-fail; not authoritative. |
| InsertReviewRequest races guarded by 1062 mapping | `review.go:167` comment | yes | T | Turns a unique-index violation into ErrConflict (CAS loser). |
| Publish locked re-check | `repository/plugin/listing.go:91-110` | yes (FOR UPDATE) | T | Prevents Publish from racing Submit to produce private+published+pending. |
| Cancel/approve/reject CAS on `status='pending'` | `review.go:575` (mustChangeState), `review.go:648`/`:902`/`:1018` (mustAffect) | yes (FOR UPDATE) | T, IM | |
| cascade-cancel UPDATE is CAS on `status='pending'` | `review.go:856` (`cancelPendingReviewFor`) | yes (under caller tx) | T, A | Zero rows is not an error. |

### 6. Reviewer / delist authority (who can approve/reject/delist)

| site | file:line | lock | surface | notes |
|---|---|---|---|---|
| `Service.isReviewer` | `service/plugin/review.go:1049-1051` | pre | T, IM | `caller.IsSystemAdmin || caller.SpaceRole >= SpaceRoleAdmin`. `Caller.SpaceRole` is populated by the HTTP layer from verified octo-server identity, never from the request body or IM payload. |
| Approve service gate | `service/plugin/review.go:567` | pre | T, IM | |
| Reject service gate | `service/plugin/review.go:611` | pre | T, IM | |
| Cancel service gate (applicant only) | `service/plugin/review.go:643` | pre | T | |
| Delist service gate | `service/plugin/listing.go:162` | pre | T (Space admin) | Uses same `isReviewer(caller)`. |
| DecideReviewFromCard role check (IM) | `service/plugin/review.go:818` onward (retries octo-server role lookup) | pre | IM | Re-resolves operator's role against octo-server (never trusts a card-supplied role); `TestDecideReviewFromCardRefusesNonAdminOperator` pins it. |
| Admin middleware gate (`/api/v1/admin/*`) | `middleware/admin.go:142-152` admits `RoleSuperAdmin` on every group plus `alsoAllow` | pre (HTTP) | A | Every admin plugins group is mounted with `Handler(RoleMarketAdmin)` (`handler/plugin/admin.go:69`), so the admitted set is `{superAdmin, marketAdmin}` — NOT superAdmin only; see item 36 correction. |
| Tenant Authenticator populates Caller (IsSystemAdmin, SpaceRole) | `handler/plugin/handler.go:613` sets `IsSystemAdmin = (identity.Role == RoleSuperAdmin)`; SpaceRole is populated from verified `identity.SpaceRoles[spaceID]` in a helper a few lines above | pre (HTTP) | T | |

### 7. Owner-scope / Space-scope on reads (`visibilitySQL`)

| site | file:line | lock | surface | notes |
|---|---|---|---|---|
| `visibilitySQL` definition | `repository/plugin/repo.go:105` | n/a | T, A | `(p.visibility IN ('public','system') OR (p.space_id = ? AND ((p.visibility='space' AND p.listing_state='published') OR p.owner_uid = ?)))`. Owner disjunct deliberately NOT gated on `listing_state`; public/system disjunct NOT gated (item 36 system-row residual). |
| `Get` | `repository/plugin/read.go:75` | no (plain SELECT) | T | |
| `List` (grid + mine variants) | `repository/plugin/read.go:159` and `read.go:201` (appends `listedSQL` for non-mine) | no | T | |
| `ListVersions` count + rows | `repository/plugin/history.go:18`, `history.go:32` | no | T | |
| `ListTags` | `repository/plugin/tags.go:41` (grid adds `listedSQL`) | no | T | |
| `ListPlacementCategories` | `repository/plugin/history.go:115` + `history.go:162` (`listedSQL` for chip counts) | no | T | |
| `GetWithRelations` | `repository/plugin/read.go:246` | no | T, A (through adminScope which overrides SpaceID/UID for cross-Space reads) | |
| Relation-target `lockRelationTargets` | `repository/plugin/write.go:987` | yes (FOR UPDATE on target rows) | T | The one write-site embed; refuses adopting a colleague's space-intent draft as a relation. |
| Container-graph relock | `repository/plugin/write.go:915` | yes (FOR UPDATE) | T, A | |
| Owner existence checks (`lockRelationTargets` target lock) | `repository/plugin/write.go:991` | yes | T | |
| Admin list uses a different predicate (`AllSpaces`) | mounted via `handler/plugin/admin.go` group | no | A | adminScope bypasses the owner disjunct; cross-Space reads deliberate. |

### 8. Self-delist forbidden (tenant cannot take a `published`+`space` row down via upsert/delete)

| site | file:line | lock | surface | notes |
|---|---|---|---|---|
| Upsert service gate (ErrListedRequiresReview) | `service.go:603-605` predicate `old.ListingState == published AND old.Visibility == space` | pre | T | Refuses ALL edits to published+space rows, which closes the "lower visibility to private" loophole; comment at `service.go:574-576` says so explicitly. |
| Upsert LOCKED re-derivation (`EnforceListingGate`) | `write.go:554-557` | yes (`getOwnedForUpdate` at :514) | T | Authoritative; restates the published+space conjunction from the locked row, so a concurrent Approve that flips a draft published between the service read and the lock is caught. |
| Delete service gate | `service.go:750-753` | pre | T | Identical conjunction; closes the "delete to self-delist" loophole at the service layer. |
| Delete / DeleteGraph LOCKED re-derivation | `write.go` — after `getOwnedForUpdate` in both `Repo.Delete` and `Repo.DeleteGraph` | yes | T | **CLOSED (round 15).** Restates the (published AND space) conjunction against the locked row, mirroring `Repo.Update`'s `EnforceListingGate`, so an Approve that publishes a draft between the service's unlocked read and the FOR UPDATE lock can no longer let a tenant delete a just-listed plugin. `!scope.Admin` exempts the system admin, who is the Delist actor and must be able to remove abusive listed content. Pinned by `TestDeleteRefusesAPluginApprovedMidFlight` and `TestDeleteGraphRefusesAContainerApprovedMidFlight` (real-MySQL interleavings; removing the gate makes them fail). |
| Delist (only takedown path) is Space-admin-gated | `service/listing.go:162` + repo lock by Space not owner | yes | T (Space admin) | `getReviewedPluginForUpdate` (`review.go:814-824`) locks by `space_id` not `owner_uid` so a non-owner admin can take it down. |
| Delist CAS restates visibility + listing_state + is_embedded | `listing.go:210-216` | yes (CAS) | T (Space admin) | Defense in depth. |
| Visibility change while a review is pending | `service.go:658-667` (`HasPendingReview`) + locked re-derivation `write.go:524-534` (`RefusePendingReview`) | pre + yes | T | The actual invariant here (visibility cannot change while pending) is enforced by the service pre-check and re-checked under lock in Repo.Update. ApproveReview's isFirst branch at `review.go:562-570` decides its branch from the LOCKED `current.Visibility/ListingState` and restates the expected (space,published) in the CAS WHERE, so a visibility flip after the service read does not cause a silent wrong-branch apply — it causes mustChangeState to return ErrConflict. The distinct race where an author flips visibility to private WHILE an approval is in flight is safe: isFirst fires, the UPDATE sets visibility=space back (a no-op for re-approve), and mustChangeState on the upgrade branch is satisfied only when the row is genuinely in the upgrade state. |
| Widening-to-space unlists (ResetListingToDraft) | `service.go:686-689` decision; locked recompute `write.go:558` | yes | T, A | |

### 9. Deadlock classification (InnoDB 1213 → retryable ErrDeadlock)

| site | file:line | applied? | surface | notes |
|---|---|---|---|---|
| `classifyDeadlock` maps 1213 AND 1205 → ErrDeadlock | `repository/plugin/review.go` (`classifyDeadlock`) | n/a | n/a | **1205 added in round 16.** It was previously excluded with the note "deliberately", which did not survive scrutiny: `getOwnedForUpdate` queues on the plugin row for a whole approve transaction (relation sync, version snapshot, child promotion, audit), so a waiter can cross `innodb_lock_wait_timeout` with no cycle forming — 1205 is the LIKELIER of the two here, and it was falling through to HTTP 500. Both mean "lost a lock race, nothing committed, retry"; the wire value stays `conflict_reason=deadlock` so the client contract does not move. |
| InsertReviewRequest (Submit) | `review.go:100` | yes | T | |
| ApproveReview | `review.go:421` | yes | T, IM | |
| RejectReview | `review.go:864` | yes | T, IM | |
| CancelReview | `review.go:979` | yes | T | |
| `mapStoreError` surfaces to service | `service/service.go:917-918` | yes | T, A, IM | |
| Web handler → retryable 409 | `handler.go:746-747` | yes | T, A, IM | Body carries `conflict_reason="deadlock", retryable=true`. |
| IM card path treats ErrDeadlock as non-terminal | `review.go:916` comment + :941 mapping | yes | IM | Surfaces as `fault` so octo-server re-delivers. |
| PublishPlugin | `repository/plugin/listing.go` — `defer` after the signature | yes | T | **CLOSED (round 16).** |
| DelistPlugin | `repository/plugin/listing.go` — `defer` | yes | T | **CLOSED (round 16).** This was the highest-confidence race: ApproveReview takes the REQUEST row FOR UPDATE first then the PLUGIN row; DelistPlugin takes the PLUGIN row first, then `cancelPendingReviewFor` UPDATEs the request row (taking an exclusive lock on it) — textbook AB-BA. |
| Repo.Update / Repo.Delete / Repo.DeleteGraph / Repo.RebuildGraph | `write.go` — `defer` on each | yes | T, A | **CLOSED (round 16).** All four reach `plugin_review_requests` (pending-check or `cancelPendingReviewFor` cascade), so all four sit on the plugin-row-first side of the AB-BA. |
| **NOT applied, and correctly so:** Repo.Create / CreateGraph | `write.go` BeginTx — no defer | **no** | T, A | A plugin being created cannot yet have a review request, so these never take the second lock and cannot participate in the cycle. Named here so the omission reads as a decision rather than the next round's finding. |
| **NOT applied:** admin category mutations | `admin_category.go:68, 140` | no | A | Low contention, and no review-request lock. |

Score: **10 of 10 transactions that touch both `plugins` and `plugin_review_requests`
are classified** (the four review paths plus Publish, Delist, Update, Delete,
DeleteGraph, RebuildGraph). The reason this had to be all-or-nothing: one physical
deadlock produces exactly one victim, and which transaction loses is up to InnoDB —
so classifying half of them meant the SAME race answered a retryable 409 or a raw
1213 → HTTP 500 depending on scheduling. Create/CreateGraph and the admin category
mutations are deliberately excluded (no second lock).

### 10. Published + space edits refuse (ErrListedRequiresReview)

| site | file:line | lock | surface | notes |
|---|---|---|---|---|
| Tenant upsert service gate | `service.go:603-605` | pre | T | Predicate is the conjunction (published AND space), NOT `visibility == 'space'` alone (corrected in item 19 above). |
| Tenant upsert LOCKED re-derivation (`EnforceListingGate`) | `write.go:554-557` | yes (`getOwnedForUpdate`) | T | Authoritative; closes the TOCTOU the pre-check leaves. |
| Tenant delete service gate | `service.go:750-753` | pre | T | Identical conjunction. **Gap: no locked re-derivation in Repo.Delete** (see item 8 row). |
| Publish service pre-check (immediate vs review) | `service/listing.go:67-89` | pre | T | |
| PublishPlugin locked re-derivation of `visibility=private` | `listing.go:80-95` + CAS at :115-123 | yes | T | |
| ApproveReview CAS on state transition | `review.go:571-575` `mustChangeState` | yes | T, IM | |

### 11. Embedded children follow their container (cannot be published/delisted standalone)

| site | file:line | lock | surface | notes |
|---|---|---|---|---|
| Submit refuses embedded target | `service/review.go:168` | pre → repo FOR UPDATE | T | |
| Publish refuses embedded target | `service/listing.go:77` | pre → repo CAS `is_embedded=0` (`listing.go:115`) | T | |
| Delist refuses embedded target | `listing.go:185-186` + CAS `is_embedded=0` (`listing.go:215`) | yes | T | |
| Promote on approve | `review.go:597` `promoteEmbeddedChildren` | yes (under approve tx) | T, IM | Promotes exactly same-Space, still-private embedded children derived AFTER relation sync. |
| Demote on delist | `listing.go:228` `demoteEmbeddedChildren` | yes (under delist tx) | T | |
| AdminUpdate refuses embedded children | `admin.go:285-288` (`old.IsEmbedded → ErrNotFound`) | pre | A | A standalone PATCH must not content-edit a bundled child. |

### 12. Admin role admission = {superAdmin} ∪ {marketAdmin}

| site | file:line | lock | surface | notes |
|---|---|---|---|---|
| Constants | `middleware/admin.go:19` `RoleSuperAdmin="superAdmin"`, `:27` `RoleMarketAdmin="marketAdmin"` | n/a | n/a | |
| `roleAdmitted` | `middleware/admin.go:142-152` | pre (HTTP) | A | Admits `RoleSuperAdmin` unconditionally; otherwise any entry in `alsoAllow`. |
| Every admin plugins route mounted with `RoleMarketAdmin` | `handler/plugin/admin.go:69` | pre (HTTP) | A | The `alsoAllow` on every live admin group. |
| `Service.IsSystemAdmin=true` is set by `adminEffectiveWrite` | `admin.go:128` (`eff.IsSystemAdmin = true`) | n/a | A | This is what gates the "edit listed content with no review" escape hatch (item 36) — and because every admin route routes through AdminCaller → adminEffectiveWrite, both roles hold that exemption. |

### How to use this table

A reviewer looking for the "enforced in three places, missing in a fourth"
pattern should:

1. Pick an invariant column.
2. Look for rows marked **lock = pre** whose downstream "yes" site is on a
   different surface (e.g., service-layer pre-check on tenant path but locked
   re-check only in admin, or vice versa).
3. Look for transactions under "NOT applied to" rows in the deadlock section —
   those are the obvious candidates for the same miss.
4. **All three of the highest-confidence defects this table named are now closed**
   (Delete's locked listing gate in round 15; deadlock classification and
   forward-only under the `Repo.Update` lock in round 16). The list is kept rather
   than deleted, because "the table predicted the next round's findings three times
   running" is the useful fact about it — and because a reviewer should be able to
   check the closures rather than take them on faith. What remains open at this head
   is smaller and is stated as such, not left for inference:
   - **Self-approval is permitted.** A Space admin or owner can approve their own
     submission on both surfaces — see item 26, where the residual and the reason
     it is not simply refused are recorded.
   - **The super-admin content escape hatch has no test** (item 36, first bullet;
     item 37's stated gap). Unchanged.
   - **Terminal card refusals are unreceipted on purpose**, so one `event_id` is
     never double-applied but is not answered verbatim across deliveries either.
     The trade-off is stated at each branch in `service/plugin/review.go`.
5. Every cell was re-derived against the code it ships with, and the gap rows that
   closed were closed in the same commit as the fix. No UNVERIFIED cells remain.
