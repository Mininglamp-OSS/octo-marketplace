# Plugin capability installations

This is an additive Marketplace endpoint for Workspace recruitment. The existing
`POST /api/v1/plugins/install` remains unchanged and is not deprecated. Client
migration is separate work.

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

The adapter follows Fleet's capability Definition contract, including Workspace
connectors from Fleet commit `489c2ad`. The source contract is recorded in
`octo-fleet/docs/specs/2026-10-27-workspace-mcp-connectors-design.md`;
the wire implementation lives in Fleet's `pkg/capability` and installation handlers.

| Existing behavior | Fleet wire field |
| --- | --- |
| Per-expert MCP configuration | `definition.experts[].mcp_config` (JSON object) |
| Team summary and collaboration instructions | `definition.expert_team.description` / `instructions` |
| Runtime-specific environment values, including provider selection | `definition.experts[].custom_env` (string map) |

These shapes are isolated in `internal/fleet/capability_types.go`. The public
Fleet route responds with `{ "data": ... }`, not the internal handler's bare
result. Runtime access, deployment configuration, atomicity and replay still
require integration verification in the target environment before switching clients.

## OpenAPI compatibility

Schema-diff reports for these installation changes have the following wire
disposition:

| Schema change | Consumer impact |
| --- | --- |
| Legacy `/plugins/install` body: overlapping `oneOf` removed and body typed as the actual request object | Generator correction. Both branches previously matched valid request objects, making `oneOf` reject them. The HTTP body fields and opaque legacy IDs are unchanged. |
| New `/plugins/{plugin_id}/installations` body: `if` / `else` Runtime requirement | Additive connector mode. Default-mode requests still require Runtime; connector-mode requests may omit it at the schema boundary. Expert/team targets still require it during graph validation. |

Existing legacy callers keep their current request bodies. Clients opting into
connector-only installation omit `runtime_id` when no Runtime is selected;
an explicitly supplied value must be a UUID string.

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

`workspace_connectors` is an optional boolean. Omitting it or setting it to
`false` preserves the existing expert/team installation request, Fleet
definition, MCP configuration path, validation, and response shape. Set it to
`true` only to opt into Workspace connector storage:

- Expert and expert-team installations still require a valid `runtime_id`.
- An independent connector installation may omit `runtime_id`; if supplied,
  it must still be a valid UUID. In this mode `resource_name` names the
  Workspace connector. A non-empty `custom_env` is rejected; it is not treated
  as connector configuration.
- For expert/team installs, `resource_name` retains its existing expert/team
  naming behavior and `custom_env` remains the expert execution environment.

To import an independent catalog connector, use its plugin ID in the same
installation path and send `{"workspace_connectors": true}`. No Runtime is
needed. To recruit an expert/team with saved connector dependencies, add
`"workspace_connectors": true` to the expert/team request above.

The new mode includes explicit market connector dependencies where present.
Shared dependencies are sent once in the Fleet Definition and each expert is
bound only to its own references. If same-name dependencies have different
sources or content and cannot be uniquely referenced, preparation fails rather
than merging them by name. MCP configuration continues to be carried as a
complete snapshot. Definitions carry the real Marketplace source ID where
known, but never a pre-existing Workspace connector UUID.

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
- `runtime_id` is a required UUID on this route when Workspace connector mode is
  disabled, and for expert/team installations when `workspace_connectors` is true.
  It may be omitted only for an independent connector installation in the new
  mode. If supplied, it must be a UUID string: empty, whitespace-only, `null`,
  and non-string values return `400 VALIDATION_ERROR` before installation.
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
  attempts freeze the authorized catalog definition, including MCP configuration,
  in the caller/Space/Workspace-scoped attempt store. Definitions may contain MCP
  credentials and must not enter logs, events or Git.
- Keys contain 1–200 visible ASCII characters without spaces. Unknown JSON
  fields, compressed bodies, and non-JSON requests are rejected. The HTTP body
  limit is 3 MiB; the assembled Fleet request is limited to 16 MiB.

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

When `PLUGIN_PINNED_INSTALLATION_ENABLED` is enabled, this endpoint keeps the
existing version-management, published-root, and dependency-audience rules.
Skills come from the selected immutable release, including its nullable
`DeclaredVersion` and real plugin ID; a later draft does not change the selected
release, Skill name or original file bytes. Workspace connector mode is supported
on this path as well. Authorized current connector snapshots are frozen with the
first plan, so later edits do not change an existing operation. Connectors do not
acquire fabricated immutable release IDs. The pinned path's existing
inline-dependency rejection remains in force.

Signed frozen attempts created with the earlier Skill wire shape retain their
original fields and fingerprints on same-key replay. New attempts include
source/version metadata; switching `workspace_connectors` with a previously used
key is a conflict. Recovery still requires the original adapter, valid eligibility
HMAC, available key-ring entry, current authorization and original retry deadline.

Each expert uses its own manifest description, including team members; the
team itself uses the team manifest description. This is intentional metadata
improvement over the legacy team's shared summary; the legacy route is unchanged.

The adapter preserves expert AGENTS.md/MCP and team AGENTS.md. Instructions
must be inline (`content_type: raw`). Legacy MCP must also be inline;
Workspace connector mode additionally uses the existing verified object reader
for storage-backed `mcp.json`, checking trusted keys, size, and checksum before
Fleet. Experts without MCP remain supported. Same-name
Skills within one request are shared only if their normalized content, description,
configuration, and complete file tree match. Skill descriptions come from the
Skill manifest rather than the installing expert/team, so shared content has a
stable fingerprint. Conflicting content is rejected, not overwritten.

For requests that omit `workspace_connectors` or set it to `false`, known empty
MCP defaults (`{}` or `{"mcpServers":{}}`) are omitted. Non-empty legacy
`mcp_config` uses Fleet's closed stdio/remote structure: command or HTTP(S) URL,
never both; unknown fields, invalid transports and explicit null values are
rejected. Command/URL/type are normalized as Fleet does, while environment,
arguments and headers retain their values. Normalized JSON is capped at 64 KiB.
For stdio, `command` selects the transport: a non-empty `type` (including
`"stdio"`) or non-empty `headers` is unsupported and returns 400
`VALIDATION_ERROR` before Fleet. Marketplace does not strip those fields or
reinterpret them as environment variables.

With `workspace_connectors: true`, MCP dependencies become Workspace connector
snapshots rather than legacy `mcp_config`. Their complete `mcp.json` configuration
is retained, including actual server keys, headers, environment, arguments, and
service extension fields; it is not narrowed through the legacy MCP normalizer.
No MCP process is started and no MCP URL is fetched by Marketplace.

Team leadership uses the first explicitly flagged leader, otherwise the first
member, matching the legacy selection rule. Tied display orders are resolved by
plugin ID for deterministic retries. Exactly one leader is emitted. Duplicate
expert names and non-leader members with the reserved `leader` role are rejected.

This route materializes **text-only** assets into `inline_text` sources. It fails
closed on binary/non-UTF-8/NUL content, unsafe or duplicate paths, missing files,
untrusted object keys, and recorded size/checksum mismatches. Unlike the legacy
best-effort reader, it never silently omits an unreadable supporting file. Binary
asset support is out of scope for this draft. Managed legacy ZIPs are expanded
under the same limits and rooted at SKILL.md; no artifacts are modified.

Limits include 30 experts, 20 Skill relations per expert, 600 unique source
Skills, 50 supporting files per Skill, 500 supporting files per installation,
and 1 MiB per text item. Names are at most 128 characters; Expert/Team descriptions
255, Skill descriptions 512, and member roles 500. Overlong values are rejected,
not truncated. Supporting files cannot alias SKILL.md by case. The configured archive limit may further reduce the
16 MiB aggregate budget.

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

An expert or team installation in Workspace connector mode keeps its original
`agent_id` or `squad_id` and may additionally return `connectors`. Connector
results contain only `connector_id`, `name`, nullable `version`,
nullable `market_mcp_id`, and `created`; configuration is never included.
Legacy mode omits `connectors`, even if Fleet returns connector metadata.

Fleet test currently returns no `Idempotency-Replayed` header. Marketplace only
forwards `true` when Fleet explicitly supplies it; its absence is not proof of a
new installation. Fleet response names/IDs, resource type, leader, Skill
references, nullable Skill versions and Marketplace source IDs, connector
metadata, and each expert's connector bindings are checked before success.

For new-mode connector results, `connector_id` must be a UUID and each result
must include both nullable fields, `version` and `market_mcp_id`; explicit
`null` is valid. Legacy Expert and Skill IDs remain opaque.

Skill versions and `market_skill_id` must exactly match the requested definition;
`null` is distinct from any string value. Historical
pinned receipts replayed with the original Skill wire format omit those metadata
fields, so only their Skill metadata comparison is skipped; identity and binding
checks still apply. Missing, duplicate, unknown, or mismatched Skill or connector
results leave the Fleet outcome uncertain; retry only with the same key and
input. Errors use the standard `{ "error": { "code", "message", "details", "hint" } }`
envelope. By explicit product requirement, Marketplace-generated `message` and
`hint` are Chinese and may be displayed directly by the client; machine codes
and `details` keys remain stable. This is a route-specific exception to the
default API convention of English messages. Authentication failures on this
route also use Chinese; existing routes keep their original text.

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
