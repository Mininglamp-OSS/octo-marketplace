# Plugin capability installations (gated integration)

This is an additive Marketplace endpoint for Workspace recruitment. The existing
`POST /api/v1/plugins/install` remains unchanged and is not deprecated. Client
migration and enabling the new Fleet integration are separate work.

## Rollout gate / verified source contract

`OCTO_FLEET_CAPABILITY_INSTALL_ENABLED` defaults to `false`. Until explicitly
enabled, the new endpoint returns `503 UPSTREAM_UNAVAILABLE` without calling
Fleet. Both installers reuse `OCTO_FLEET_URL`, without an `/api` suffix. For a
direct service base such as `http://fleet:8093`, the new adapter appends
`/v1/capabilities/install`. When the base path ends in the OCTO gateway mount
`/fleet`, it appends `/api/v1/capabilities/install` instead (for example,
`https://octo.example.test/fleet/api/v1/capabilities/install`). Other base paths
retain direct-service behavior; the hostname is not used to infer routing.
Legacy `/api/agents`, `/api/skills` and `/api/squads` paths are unchanged. There
is no extra configuration, route probing, retry or fallback between paths.

The adapter is aligned with Fleet `origin/test` commit `49a8266`, fetched on
2026-09-24, specifically `pkg/capability/{definition,validation}.go`, the published
Definition schema, `internal/handler/capability_installation.go`, and the public
API facade. This supersedes the earlier draft's provisional field locations.

| Existing behavior | Fleet wire field |
| --- | --- |
| Per-expert MCP configuration | `definition.experts[].mcp_config` (JSON object) |
| Team summary and collaboration instructions | `definition.expert_team.description` / `instructions` |
| Runtime-specific environment values, including provider selection | `definition.experts[].custom_env` (string map) |

These shapes are isolated in `internal/fleet/capability_types.go`. The public
Fleet route responds with `{ "data": ... }`, not the internal handler's bare
result. No live Fleet installation has been performed in this change; runtime
access, deployment configuration, atomicity and replay still require integration
verification before enabling the gate.

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

`/market` is the gateway mount; Marketplace mounts `/api/v1`. The OpenAPI path
is `/plugins/{plugin_id}/installations` relative to `/v1`.

- Authentication also accepts the existing `Token` header. Identity and Space
  access come from Marketplace authentication, not request-body fields. Bot
  identities are rejected. The same end-user credential is forwarded to Fleet,
  which authorizes the selected Workspace and runtime.
- `X-Workspace-ID` and `Idempotency-Key` must each occur exactly once. This new
  Marketplace route is **ID-only** and rejects `X-Workspace-Slug`, including when
  an ID is present. A future Client migration must stop sending the legacy slug
  selector on this route. No Workspace identifier is accepted in the body.
- `runtime_id` is a required UUID; all team members use this runtime.
- `resource_name` is optional, trimmed, and limited to 128 characters without
  control characters. It defaults to the catalog name. For a single expert it
  sets that expert's name; for a team it sets only the team name. Member names
  remain their catalog names. No catalog record is renamed.
- `custom_env` is optional and is copied into every Expert Definition, not the
  runtime bindings or Marketplace persistence. Fleet persists the values per
  Expert; ordinary Expert reads exclude them. Limits: 100 keys; key length 128 and
  pattern `[A-Za-z_][A-Za-z0-9_]*`; value length 32 KiB; 256 KiB total key/value
  bytes; UTF-8 values without NUL. Values must be strings: `null`, invalid UTF-8,
  and unpaired Unicode surrogate escapes are rejected instead of being silently
  converted. Empty strings and whitespace are preserved. Do not log credentials
  or environment values.
- Definition may contain actual secrets in custom_env and MCP env/headers. It
  exists only in this request's memory and must not enter logs, events or Git.
- Keys contain 1–200 visible ASCII characters without spaces. Unknown JSON
  fields, compressed bodies, and non-JSON requests are rejected. The HTTP body
  limit is 3 MiB; the assembled Fleet request is limited to 16 MiB.

## Assembly and compatibility boundaries

Marketplace resolves the root expert/team and every dependency under the
authenticated caller's visibility. An embedded member cannot be installed as a
standalone root. Hidden dependencies reject the entire operation before Fleet.

For a single expert, `definition.experts` contains one item and `expert_team` is
absent; referenced Skills still appear under `definition.skills`. A team contains
its member experts, their deduplicated Skills, and `expert_team`. Bindings identify
each expert by the assembled name. `definition.name` is the stable catalog
identity `marketplace:<plugin_id>`, not the custom display name.

The adapter preserves expert AGENTS.md/MCP and team AGENTS.md. These documents
must be inline (`content_type: raw`); a declared storage-backed MCP is rejected
before Fleet rather than omitted. Experts without MCP remain supported. Same-name
Skills within one request are shared only if their normalized content, description,
configuration, and complete file tree match. Skill descriptions come from the
Skill manifest rather than the installing expert/team, so shared content has a
stable fingerprint. Conflicting content is rejected, not overwritten.

Known empty MCP defaults (`{}` or `{"mcpServers":{}}`) are omitted. Non-empty
MCP uses Fleet's closed stdio/remote structure: command or HTTP(S) URL, never
both; unknown fields, invalid transports and explicit null values are rejected.
Command/URL/type are normalized as Fleet does, while environment, arguments and
headers retain their values. Normalized JSON is capped at 64 KiB. No MCP process
is started and no MCP URL is fetched by Marketplace.

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

Fleet test currently returns no `Idempotency-Replayed` header. Marketplace only
forwards `true` when Fleet explicitly supplies it; its absence is not proof of a
new installation. Fleet response names/IDs, resource type, leader, and Skill
references are checked before returning success. Errors
use the standard `{ "error": { "code", "message", "details", "hint" } }`
envelope. By explicit product requirement, Marketplace-generated `message` and
`hint` are Chinese and may be displayed directly by the client; machine codes
and `details` keys remain stable. This is a route-specific exception to the
default API convention of English messages. Authentication failures on this
route also use Chinese; existing routes keep their original text.

HTTP **409 preserves Fleet's `error.message` verbatim**, including its language.
Only a missing, invalid, or whitespace-only message uses the Chinese fallback.
Fleet's `DUPLICATE` is retained. For `details.resource = "idempotency_key"`,
Marketplace adds `details.conflict_reason = "idempotency_key_reused"` without
forwarding the raw name/key or arbitrary upstream details. The earlier draft's
`IDEMPOTENCY_KEY_REUSED` mapping remains supported as `CONFLICT`.
Other upstream status codes do not expose Fleet's message or raw response body.
Conflict messages are display text, not trusted HTML, and must not be logged
with submitted content or credentials. The client should render them as text.

Preparation failures such as unreadable/corrupt artifacts or catalog failures
return `500 INTERNAL_ERROR` with `details.phase = "preparation"`. Unknown errors
after invoking the Fleet adapter return `503 UPSTREAM_UNAVAILABLE` with
`details.phase = "fleet"`: the installation may have committed. Known Fleet
errors keep the status-specific mapping (400/401/403/404/409/413/415/429), and
other Fleet statuses map to 503. Valid numeric `Retry-After` is forwarded on 429.
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
- Marketplace re-reads the source on retry; it does **not** store historical
  Definition snapshots. Editing the plugin, dependencies, environment, or
  adapter serialization between attempts can produce a safe 409 rather than a
  replay. Do not claim exact replay across such changes. Retain the original
  client input securely and reconcile before starting a replacement operation.
- Only a confirmed non-replay may increment install metrics. With the current
  Fleet implementation all success responses have unknown replay status, so this
  new route skips Marketplace install counting rather than counting retries twice.
  This does not affect installed resources or legacy metrics; no new deduplication
  store is introduced. Restore counting only with a reliable upstream signal.

Before switching Client, verify single expert and multi-member team installs,
custom naming, MCP, provider environment, complete Skill files, cross-Space and
runtime denials, same-key replay, changed-input conflict, and uncertain-response
recovery against Fleet. Keep the legacy route available throughout migration.
