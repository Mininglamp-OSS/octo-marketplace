# Plugin capability installations

This is an additive Marketplace endpoint for Workspace recruitment. The existing
`POST /api/v1/plugins/install` remains available with its original request and
response shape; because it has no selector, new operations through its adapter
default to Workspace connector recruitment. New operations through the new
endpoint also default `workspace_connectors` to `true`.

## Availability / verified source contract

The new endpoint is available whenever the existing `OCTO_FLEET_URL` is configured;
there is no additional enable flag. The removed
`OCTO_FLEET_CAPABILITY_INSTALL_ENABLED` variable is ignored, even if an older
deployment still sets it to `false`. Without a configured Fleet installer, the
endpoint returns `503 UPSTREAM_UNAVAILABLE` with `details.reason = "not_configured"`
without calling Fleet. Both installers reuse `OCTO_FLEET_URL`, without an `/api` suffix. For a
direct service base such as `http://fleet:8093`, the new adapter appends
`/v1/capabilities/install`. When the base path ends in the OCTO gateway mount
`/fleet`, it appends `/api/v1/capabilities/install` instead (for example,
`https://octo.example.test/fleet/api/v1/capabilities/install`). Other base paths
retain direct-service behavior; the hostname is not used to infer routing.
Legacy `/api/agents`, `/api/skills` and `/api/squads` paths are unchanged. There
is no extra configuration, route probing, retry or fallback between paths.

## Fleet receipt API and deployment order

Market uses Fleet's read-only receipt API before caller-keyed, selector-omitted
non-pinned plugin requests, including first attempts, and before caller-keyed
direct expert/team requests. Deploy Fleet with both receipt endpoints before
deploying the Market version that calls them; Market does not fall back to a
legacy installer or suppress an unsupported-endpoint error.

The presence request is `GET /v1/capabilities/install/receipt`, with the same
`Token`, `X-Space-Id`, `X-Workspace-Id`, and `Idempotency-Key` scope as an
installation. It returns `200 {"data":{"has_receipt":true|false}}`. A matching
retry uses `POST /v1/capabilities/install/receipt` with the original capability
request body. Fleet returns the original result with `Idempotency-Replayed:
true`; it performs no install, claim, or resource mutation. A mismatched body
returns `409 DUPLICATE` for `idempotency_key`; an absent or expired receipt
returns `404 NOT_FOUND` for `capability_installation_receipt`.

For a selector-omitted non-pinned plugin request with a caller key, Market checks
for a receipt and compares the currently assembled published content against
the pre-upgrade MCP wire first, then the current connector wire. A hit is
recovered only by read-only replay of a matching Fleet fingerprint. Market
does not retain a historical content snapshot: if current catalog content,
runtime, environment, or other request input has changed, recovery can conflict.
Keyless requests use a generated key and skip the presence probe.

Presence-check authentication and permission failures retain Fleet's `401` and
`403` statuses and dispatch no install. Transport failures and undecodable
responses remain uncertain outcomes and likewise stop before any mutation.

The adapter follows Fleet's capability Definition contract, including Workspace
connectors from Fleet commit `489c2ad`. The source contract is recorded in
`octo-fleet/docs/specs/2026-10-27-workspace-mcp-connectors-design.md`;
the wire implementation lives in Fleet's `pkg/capability` and installation handlers.

| Marketplace behavior | Fleet wire field |
| --- | --- |
| New operations on the new route or legacy `/plugins/install` adapter | `definition.connectors[]` plus each Expert's `connector_names`; selector-omitted pinned retries may resume their frozen mode, while eligible caller-keyed non-pinned retries use read-only receipt matching |
| New operation with explicit `workspace_connectors: false` | Existing `definition.experts[].mcp_config` behavior |
| Team summary and collaboration instructions | `definition.expert_team.description` / `instructions` |
| Runtime-specific environment values, including provider selection | `definition.experts[].custom_env` (string map) |

These shapes are isolated in `internal/fleet/capability_types.go`. The public
Fleet route responds with `{ "data": ... }`, not the internal handler's bare
result. Runtime access, deployment configuration, atomicity and replay still
require integration verification in the target environment before switching clients.
Ordinary Expert `mcp_config` CRUD, editing, validation, claiming and runtime
configuration synthesis are unchanged.

## OpenAPI compatibility

Schema-diff reports for these installation changes have the following wire
disposition:

| Schema change | Consumer impact |
| --- | --- |
| New `/plugins/{plugin_id}/installations` body: `workspace_connectors` default | New operations default to `true` and use Workspace connectors. An eligible selector-omitted pinned retry resumes its frozen mode; an eligible caller-keyed non-pinned retry may read-only recover a receipt matching current content. Explicit `false` on a new operation preserves the legacy MCP storage path and requires a Runtime. Expert/team installs require Runtime in both modes; connector roots are available only in connector mode and may omit Runtime. |

Existing legacy callers keep their current request bodies. For a new operation,
the new endpoint's optional `workspace_connectors` field defaults to `true`;
`false` retains the previous behavior. Any supplied `runtime_id` must be a UUID
string. A selector-omitted pinned retry follows the frozen-plan rule below.

## Request

```http
POST /market/api/v1/plugins/{plugin_id}/installations
Authorization: Bearer <end-user-token>
X-Space-Id: <current-space>
X-Workspace-ID: <target-workspace>
Idempotency-Key: <unique-installation-operation-key>
Content-Type: application/json

{
  "resource_name": "My reviewer",
  "runtime_id": "7f63db70-6b66-4c2d-a52f-fb6de1d78ad9",
  "custom_env": {
    "OCTOBUDDY_PROVIDER_ID": "provider-id"
  }
}
```

`workspace_connectors` is an optional boolean that defaults to `true` for new operations:

- Expert and expert-team installations use Workspace connector snapshots by
  default. Explicit `false` retains the existing `mcp_config` path. Both paths
  require a valid `runtime_id`.
- An independent connector installation is available when connector mode is
  enabled (omitted or `true`); it may omit `runtime_id`, and `resource_name`
  names the Workspace connector. A non-empty `custom_env` is rejected.
- For expert/team installs, `resource_name` retains its existing expert/team
  naming behavior and `custom_env` remains the expert execution environment.

For new operations, `/plugins/install` has no selector and defaults to connector
mode for expert/team dependencies. On an eligible pinned frozen-plan retry, it
may instead resume the original mode. It accepts only expert and expert-team
roots; independent connectors must use `/plugins/{plugin_id}/installations`,
which returns their resource identities. The original route's request and
response fields remain unchanged.

Connector mode includes explicit market connector dependencies where present
and the Expert package's own non-empty `mcp.json`. Shared dependencies are sent
once in the Fleet Definition and each expert is bound only to its own
references. If same-name dependencies have different sources or content and
cannot be uniquely referenced, preparation fails rather than merging them by
name. Complete MCP configuration is carried as a connector snapshot. Definitions
carry the real Marketplace source ID where known, but never a pre-existing
Workspace connector UUID. The legacy `false` path continues to send MCP through
`mcp_config`.

`/market` is the gateway mount; Marketplace mounts `/api/v1`. The OpenAPI path
is `/plugins/{plugin_id}/installations` relative to `/v1`.

- Authentication also accepts the existing `Token` header. Identity and Space
  access come from Marketplace authentication, not request-body fields. Bot
  identities are rejected. The same end-user credential is forwarded to Fleet,
  which authorizes the selected Workspace and, when applicable, runtime.
- `X-Workspace-ID` and `Idempotency-Key` must each occur exactly once. This new
  Marketplace route is **ID-only** and rejects `X-Workspace-Slug`, including when
  an ID is present. A future Client migration must stop sending the legacy slug
  selector on this route. No Workspace identifier is accepted in the body.
- `runtime_id` is required for expert/team installations in both connector and
  legacy modes. Explicit `workspace_connectors: false` also requires it for all
  accepted targets. It may be omitted only for an independent connector
  installation in default/true connector mode. If supplied, it must be a UUID
  string: empty, whitespace-only, `null`, and non-string values return
  `400 VALIDATION_ERROR` before installation.
- `resource_name` is optional, trimmed, and limited to 128 characters without
  control characters. It defaults to the catalog name. For a single expert it
  sets that expert's name; for a team it sets only the team name; for an
  independent connector it sets the Workspace connector name. Member names
  remain their catalog names. No catalog record is renamed.
- `custom_env` is optional and is copied into every Expert Definition, not the
  runtime bindings or Marketplace persistence. Fleet persists the values per
  Expert; ordinary Expert reads exclude them. Limits: 100 keys; key length 128 and
  pattern `[A-Za-z_][A-Za-z0-9_]*`; value length 32 KiB; 256 KiB total key/value
  bytes; UTF-8 values without NUL. Values must be strings: `null`, invalid UTF-8,
  and unpaired Unicode surrogate escapes are rejected instead of being silently
  converted. Empty strings and whitespace are preserved. Do not log credentials
  or environment values.
- Tokens and `custom_env` remain request-local and are not persisted. Pinned
  attempts freeze the authorized catalog definition, including the selected
  connector snapshots or legacy MCP configuration, in the caller/Space/Workspace-
  scoped attempt store. Definitions may contain credentials and must not enter
  logs, events or Git.
- Keys contain 1–200 visible ASCII characters without spaces. Unknown JSON
  fields, compressed bodies, and non-JSON requests are rejected. The HTTP body
  limit is 3 MiB. The complete assembled Fleet install JSON is independently
  limited to 20,000,000 UTF-8 bytes after JSON serialization.

## Assembly and compatibility boundaries

Marketplace resolves the root expert/team and every dependency under the
authenticated caller's visibility. An embedded member cannot be installed as a
standalone root. Hidden dependencies reject the entire operation before Fleet.
Connector relations must belong to the source Expert and resolve to their exact
target ID and actual connector type. Each source's publication audience is
checked before materializing the dependency and again on shared-cache reuse;
the caller's ability to read a private connector alone does not authorize its
distribution through a broader-audience Expert.
For pinned installation, the root and every dependency must currently be
published, active, undeleted and authorized, on both first dispatch and frozen-plan
retry. A retained release/pin does not exempt a delisted dependency. Blocked
retries retain the original plan, deadline and any earlier unknown outcome;
reconcile that outcome before starting a new operation. Conversion preserves
listing state and is an explicit operator action, never an installation fallback.
See [the rollout procedure](../marketplace-v2-release.md#3-下架历史与按需转换).

For a single expert, `definition.experts` contains one item and `expert_team` is
absent; referenced Skills still appear under `definition.skills`. A team contains
its member experts, their deduplicated Skills, and `expert_team`. Bindings identify
each expert by the assembled name. `definition.name` is the stable catalog
identity `marketplace:<plugin_id>`, not the custom display name.

Both current and pinned adapters send each Expert's `market_expert_id` and its
nullable selected source version; for a formal snapshot this is that snapshot's
`DeclaredVersion`. An expert team sends its own `market_expert_team_id` and its
nullable selected source version, likewise using the formal snapshot's
`DeclaredVersion`. A missing source version remains JSON `null`.

If a team relation has `TargetVersionID`, both adapters load that exact member
snapshot. The member name, description, instructions, MCP configuration,
dependencies, and declared version all come from that same snapshot; the pinned
flag does not change member-version meaning. The current adapter uses the
published root snapshot. The pinned adapter uses the current published root and
its captured member snapshots. Arbitrary historical root selection is not
exposed by this request.

When `PLUGIN_PINNED_INSTALLATION_ENABLED` is enabled, the pinned path keeps the
existing version-management, published-root, and dependency-audience rules.
Skills come from the selected immutable release, including its nullable
`DeclaredVersion` and real plugin ID; a later draft does not change the selected
release, Skill name or original file bytes. New operations on this path default
to connector mode; explicit `workspace_connectors: false` selects the legacy MCP path.
Authorized current connector snapshots are frozen with the first plan, so later
edits do not change an existing operation. Connectors do not acquire fabricated
immutable release IDs. The pinned path's existing inline-dependency rejection
remains in force.

Signed frozen attempts preserve their original fingerprints, key ID, stable
attempt key and retry deadline. A retry selects a historical wire shape only
when its bytes match the request HMAC already stored on that attempt. The
earliest shape omits Skill and Expert/Team version/source metadata; the
`3f04192` shape retains Skill version/source metadata but predates Expert/Team
version/source metadata; current requests include all metadata. The same
fingerprint selection applies to default/true connector requests. New attempts
always use the current shape. Recovery requires the stored serialization
adapter version, valid eligibility HMAC, available key-ring entry and current authorization.

**Pinned frozen-plan retry mode.** A selector-omitted retry can resume the
original mode only when the same key resolves to an existing pinned attempt with
a frozen plan, the rest of the install intent matches, and the request is within
that attempt's original retry window. The frozen plan, request/input
fingerprints, Fleet idempotency key and retry deadline remain unchanged.
Eligibility and HMAC checks, current authorization, and lease/window checks
still apply. Explicit `true` or `false` remains a mode selection; if it differs
from the frozen mode, the same-key request conflicts. This mode compatibility
applies only to existing pinned frozen plans; non-pinned receipt recovery is the
separate read-only, exact-fingerprint behavior described above. New operations
continue to default to connector mode.

Each expert uses its own manifest description, including team members; the
team itself uses the team manifest description. The legacy route keeps its
public request/response shape; new operations use the connector-default service
adapter. Pinned retries of frozen plans follow their original mode, while
eligible caller-keyed non-pinned retries can only recover a matching receipt
read-only, as described above.

The adapter preserves expert AGENTS.md/MCP and team AGENTS.md. Instructions
must be inline (`content_type: raw`). Explicit-false legacy MCP must also be
inline; connector mode additionally uses the existing verified object reader
for storage-backed `mcp.json`, checking trusted keys, size, and checksum before
Fleet. Experts without MCP remain supported. Same-name
Skills within one request are shared only if their normalized content, description,
configuration, and complete file tree match. Skill descriptions come from the
Skill manifest rather than the installing expert/team, so shared content has a
stable fingerprint. Conflicting content is rejected, not overwritten.

For new operations, an explicit `workspace_connectors: false` selects the legacy
MCP path. A selector-omitted retry may write that mode only when resuming an
existing pinned operation whose frozen plan selected legacy mode, with the same
key and within its original retry window. A non-pinned retry may use the legacy
wire only to perform the read-only receipt match described above; it never
switches a new operation into a legacy-mode write. Explicit `true` or `false`
does not switch a pinned frozen operation to another mode; a mismatch conflicts.

Known empty MCP defaults (`{}` or `{"mcpServers":{}}`) are omitted. Non-empty legacy
`mcp_config` uses Fleet's closed stdio/remote structure: command or HTTP(S) URL,
never both; unknown fields, invalid transports and explicit null values are
rejected. Command/URL/type are normalized as Fleet does, while environment,
arguments and headers retain their values. Normalized JSON is capped at 64 KiB.
For stdio, `command` selects the transport: a non-empty `type` (including
`"stdio"`) or non-empty `headers` is unsupported and returns 400
`VALIDATION_ERROR` before Fleet. Marketplace does not strip those fields or
reinterpret them as environment variables.

For new operations, omitting `workspace_connectors` or setting it to `true`
uses Workspace connector snapshots for MCP dependencies instead of legacy
`mcp_config`. A pinned retry may resume its original mode only under the pinned
frozen-plan rule above. A non-pinned selector-omitted retry with a caller key may
read-only match a Fleet receipt against current content, including the
pre-upgrade MCP wire; it does not recover a historical catalog snapshot.
Connector snapshots' complete `mcp.json` configuration is retained, including
actual server keys, headers, environment, arguments, and service extension
fields; it is not narrowed through the legacy MCP normalizer. No MCP process is
started and no MCP URL is fetched by Marketplace.

Team leadership uses the first explicitly flagged leader, otherwise the first
member, matching the legacy selection rule. Tied display orders are resolved by
plugin ID for deterministic retries. Exactly one leader is emitted. Duplicate
expert names and non-leader members with the reserved `leader` role are rejected.

This route materializes **text-only** assets into `inline_text` sources. It fails
closed on binary/non-UTF-8/NUL content, unsafe or duplicate paths, missing files,
untrusted object keys, and recorded size/checksum mismatches. Unlike the legacy
best-effort reader, it never silently omits an unreadable supporting file. Binary
asset support is out of scope for this draft. Managed legacy ZIPs are expanded
and rooted at SKILL.md; their stored compressed object bytes retain the configured
archive transport limit, independent of the install content budget.

The final Fleet request is limited to **20,000,000 UTF-8 bytes** after serialization,
including the full definition and bindings, file contents, metadata, JSON escaping,
and framing. Supporting-file count and per-text-item size are not separately
limited; files are accepted in full while the complete request stays within that
wire-byte limit. The archive transport limit never reduces this content budget.
Structural limits remain: 30 experts, 20 Skill relations per expert, and 600
unique source Skills. Names are at most 128 characters; Expert/Team descriptions
255, Skill descriptions 2048 Unicode code points (not UTF-8 bytes), and member
roles 500. Overlong values are rejected, not truncated. Supporting files cannot
alias SKILL.md by case.

## Success and errors

Success retains the legacy response mapping:

```json
{"data":{"agent_id":"<fleet-expert-id>"}}
```

or:

```json
{"data":{"squad_id":"<fleet-expert-team-id>"}}
```

Connector-only success has no `agent_id` or `squad_id`:

```json
{"data":{"connectors":[{"connector_id":"<workspace-connector-uuid>","name":"Calendar","version":null,"market_mcp_id":"market-calendar","created":true}]}}
```

An expert or team installation using connector mode keeps its original
`agent_id` or `squad_id` and may additionally return `connectors`. Connector
results contain only `connector_id`, `name`, nullable `version`,
nullable `market_mcp_id`, and `created`; configuration is never included.
Explicit-false legacy-mode responses keep their previous shape without connector
result metadata.
Legacy mode omits `connectors`, even if Fleet returns connector metadata.

Fleet test currently returns no `Idempotency-Replayed` header. Marketplace only
forwards `true` when Fleet explicitly supplies it; its absence is not proof of a
new installation. Response type, root IDs and root version identity are checked
before success. Newly created Expert/Team resources are validated against their
version identity and source metadata present in the matched wire shape. A
created Team member graph and each created Expert's Skill/Connector bindings
must match the request's resource identities and point to returned IDs.

Reused or restored Experts and Teams describe their actual local resources.
Their existing Team members and leader, Expert bindings, and returned resource
IDs may differ from the request graph; root identity still matches the requested
version key, while first-persisted source metadata is retained. Locally removed
market members stay removed. Locally deleted Skill/Connector bindings may leave
both the returned binding list and the corresponding resource collection empty,
even when the market request declares those dependencies. Local replacements
and additions retain their actual names, versions and IDs.

Expert/team results must close references in both directions: each Expert's
Skill and Connector IDs point to returned resources, and every returned Skill
or Connector is referenced by at least one returned Expert. Different Experts
may share a resource; duplicate bindings within one Expert are invalid. A
single-Expert result contains exactly its root Expert, and a Team's leader ID
points to a returned Expert. An orphan resource is an inconsistent result, not
a local graph change. Standalone Connector installation keeps its separate
resource-identity rules. Connector IDs are UUIDs with nullable `version` and
`market_mcp_id`; Expert and Skill IDs remain opaque.

Historical pinned receipts replay in the matching authenticated wire shape.
The oldest shape omits Skill, Expert and Team version/source metadata; the
`3f04192` shape retains Skill metadata and omits Expert/Team metadata. Checks
follow the fields present in the matched shape, while identity and reference
closure still apply. Missing, duplicate, unknown, unreferenced, or
identity-mismatched resources leave the Fleet outcome uncertain; retry only
with the same key and input.
Errors use the standard
`{ "error": { "code", "message", "details", "hint" } }`
envelope. By explicit product requirement, Marketplace-generated `message` and
`hint` are Chinese and may be displayed directly by the client; machine codes
and `details` keys remain stable. This is a route-specific exception to the
default API convention of English messages. Authentication failures on this
route also use Chinese; existing routes keep their original text.

Install-capacity 413 `PAYLOAD_TOO_LARGE` responses include `details.limit_bytes`
(`20000000`), `actual_bytes`, and `field` (`definition` or a `definition.*`
location). Asset-related failures may also include the Skill identifier and full
content path. Exact serialized sizes are reported when available; bounded asset
reads report `actual_bytes = limit_bytes + 1` with
`actual_bytes_lower_bound = true`. The short Chinese `message` repeats the limit,
actual size, and field or location for clients that only show messages. It never
includes file contents or secrets. This install-wire limit is distinct from the
3 MiB Marketplace HTTP request-body guard.

Fleet 413 responses retain HTTP 413 and `PAYLOAD_TOO_LARGE`. Capacity fields
are exposed only when Fleet supplies valid, recognized measurements. Responses
without those measurements use a general upstream-rejection message and retain
`details.upstream = "fleet"` and `details.phase = "fleet"`.

Recognized Fleet `DUPLICATE`, legacy `IDEMPOTENCY_KEY_REUSED`, and `CONFLICT`
identifying a Skill/Expert/Team resource collision return HTTP **409 with fixed
Chinese safety messages**. Arbitrary Fleet `error.message` text is discarded,
including on conflicts; it is neither retained in the typed error nor returned
or logged. Structured, whitelisted metadata alone selects the message:

| Conflict | Fixed `message` |
| --- | --- |
| Idempotency key reuse | 幂等键已用于其他安装参数，请核对原安装记录。 |
| Skill resource | 安装发生技能资源冲突，请核对工作区已有资源。 |
| Expert resource | 安装发生专家资源冲突，请核对工作区已有资源。 |
| Expert team resource | 安装发生专家团资源冲突，请核对工作区已有资源。 |
| Other recognized conflict | 安装发生冲突，请检查已有资源或安装参数。 |

Fleet's `DUPLICATE` is retained. For `details.resource = "idempotency_key"`,
Marketplace adds `details.conflict_reason = "idempotency_key_reused"` without
forwarding the raw name/key or arbitrary upstream details. The earlier draft's
`IDEMPOTENCY_KEY_REUSED` mapping remains supported as `CONFLICT`.
This applies to both installation URLs and both content modes. The approved
safety policy supersedes the earlier verbatim-conflict-text requirement.

Preparation failures such as unreadable/corrupt artifacts or catalog failures
return `500 INTERNAL_ERROR` with `details.phase = "preparation"`. Unknown errors
after invoking the Fleet adapter return `503 UPSTREAM_UNAVAILABLE` with
`details.phase = "fleet"`: the installation may have committed. Known Fleet
errors keep the status-specific mapping (400/401/403/404/409/413/415/429), and
other Fleet statuses map to 503. Unrecognized/generic/in-progress 409 responses
remain uncertain; a display message alone never proves rejection. Valid numeric
`Retry-After` is forwarded on 429. A definite rate limit can retry the original
key and request within the original receipt window; it does not extend that window.
For pinned attempts, an earlier uncertain send remains unknown even if a later
send returns a definite 409 or 429.
Safe phase/reason/status and request identity are logged, never upstream message,
request body, tokens, environment values, or raw internal error text.

The phase describes the **current attempt** only. A preparation failure on a
retry does not prove that an earlier uncertain attempt failed; keep the same
key/input and reconcile that earlier outcome before starting a new operation.

## Retry / coexistence rules

The connector default applies to newly initiated operations. For an existing
signed pinned frozen plan, an unchanged same-key request with no explicit
selector resumes the original mode and Fleet request through either plugin
installation URL, within the original retry deadline. An explicit mode must
match the frozen operation; changing other installation parameters still
conflicts. Recovery does not extend Fleet receipt retention or rewrite existing
experts and receipts.

For non-pinned operations there is no frozen plan or historical content
snapshot. A caller-keyed selector-omitted retry may recover only a Fleet receipt
whose fingerprint matches the content and inputs currently assembled by Market;
otherwise it conflicts. Reconcile uncertain outcomes against the original
Workspace resources before starting a new operation.

- Select **one** installation route for an operation; parallel availability
  does not mean dual-writing to both routes.
- Generate one key before starting. Reuse that key and unchanged input on a
  timeout, lost response, or malformed upstream success. Never automatically
  fall back to the legacy installer or create a new key after an uncertain result:
  Fleet may already have committed.
- Arrays and files are sorted and JSON serialization is stable. Fleet hashes
  the normalized Definition + bindings JSON, so object-key order and whitespace
  do not alone cause conflicts. Actual receipt retention is **24 hours**, not
  the old proposal's seven days. Retry within 24 hours of the first request;
  after expiry, reconcile the Workspace before creating
  another installation. The adapter itself makes no automatic retry.
- With pinned installation enabled, Marketplace preserves the first signed
  definition, selected versions, names and file bytes. Later publications or
  connector edits do not rebuild the plan. Current root/dependency eligibility
  and source audiences are rechecked before every dispatch. Token rotation does
  not change the operation; callers resupply the same environment values, which
  are verified by a keyed input fingerprint rather than persisted. Without pinned
  installation, sources are re-read and changed content can produce a safe 409
  instead of replay.
- Pinned installation metrics count the first locally completed attempt once;
  completed receipt replays do not increment the counter. The non-pinned path
  counts successful API invocations, including same-key replays. Metrics remain
  best-effort and do not replace Fleet idempotency.
- Both installation adapters use the two-minute operation deadline, including
  asset preparation and the Fleet HTTP call. Earlier caller cancellation still
  applies. Recovery uses the original operation and frozen input, never another
  installer. For keyless legacy calls with uncertain outcomes, inspect the
  Workspace rather than starting a replacement operation.

Before switching Client, verify single expert and multi-member team installs,
custom naming, MCP, provider environment, complete Skill files, cross-Space and
runtime denials, same-key replay, changed-input conflict, and uncertain-response
recovery against Fleet. Keep the legacy route available throughout migration.

## Expired receipt recovery

The original resend deadline applies to completed and uncertain attempts alike:
after Fleet's receipt window, another mutation is not licensed by the old key.
`409 CONFLICT` with `reason = "installation_attempt_expired"` and
`can_retry_same_key = false` does not mean the original installation failed.
A successful local receipt and its IDs remain stored; earlier uncertainty is
retained. The response asks the caller to inspect the original workspace record,
use the existing resource if completed, or stop and reconcile with an operator
if unclear. It does **not** instruct automatic replacement of the key.

Do not bypass current Workspace access by returning stored IDs locally, or
resend after the remote window merely because a local success exists. Follow
[receipt reconciliation](../pinned-installation.md#expired-receipt-reconciliation).
A new key is a distinct, explicitly authorized operation only after reconciliation
proves it appropriate; expiry alone is not such proof. Both installation URLs
retain the same expiry response and recovery boundary.
