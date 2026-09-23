# Plugin capability installations (draft integration)

This is an additive Marketplace endpoint for Workspace recruitment. The existing
`POST /api/v1/plugins/install` remains unchanged and is not deprecated. Client
migration and enabling the new Fleet integration are separate work.

## Rollout gate / pending Fleet contract

`OCTO_FLEET_CAPABILITY_INSTALL_ENABLED` defaults to `false`. Until explicitly
enabled, the new endpoint returns `503 UPSTREAM_UNAVAILABLE` without calling
Fleet. `OCTO_FLEET_URL` is the direct service base URL used by both installers;
the new adapter appends `/v1/capabilities/install`.

The supplied unified-install proposal does **not** yet define these three parts
needed to preserve existing recruitment behavior:

| Existing behavior | Provisional Fleet wire field |
| --- | --- |
| Per-expert MCP configuration | `definition.experts[].mcp_config` (JSON object) |
| Team summary and collaboration instructions | `definition.expert_team.description` / `instructions` |
| Runtime-specific environment values, including provider selection | `bindings.experts[].custom_env` (string map) |

These shapes are isolated in `internal/fleet/capability_types.go`. They are draft
assumptions, not an assertion that Fleet accepts them today. Before enabling,
confirm the route, auth headers, all three extensions, bare success envelope,
timeout behavior, and replay semantics against the actual Fleet implementation.
No live Fleet integration has been verified in this change.

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
- `resource_name` is optional, trimmed, and limited to 200 characters without
  control characters. It defaults to the catalog name. For a single expert it
  sets that expert's name; for a team it sets only the team name. Member names
  remain their catalog names. No catalog record is renamed.
- `custom_env` is optional and is copied to every expert's binding, never into
  Definition or Marketplace persistence. Limits: 100 keys; key length 128 and
  pattern `[A-Za-z_][A-Za-z0-9_]*`; value length 32 KiB; 256 KiB total key/value
  bytes; UTF-8 values without NUL. Do not log credentials or environment values.
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

The adapter preserves expert AGENTS.md/MCP and team AGENTS.md. Same-name Skills
within one request are shared only if their normalized content, description,
configuration, and complete file tree match. Skill descriptions come from the
Skill manifest rather than the installing expert/team, so shared content has a
stable fingerprint. Conflicting content is rejected, not overwritten.

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
and 1 MiB per text item. The configured archive limit may further reduce the
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

A replay adds `Idempotency-Replayed: true`. Fleet response names/IDs, resource
type, leader, and Skill references are checked before returning success. Errors
use the standard `{ "error": { "code", "message", "details", "hint" } }`
envelope. Upstream messages are not exposed because they may contain submitted
content or credentials. Fleet's `IDEMPOTENCY_KEY_REUSED` maps to HTTP 409
`CONFLICT`, with `details.conflict_reason = "idempotency_key_reused"`.

## Retry / coexistence rules

- Select **one** installation route for an operation; parallel availability
  does not mean dual-writing to both routes.
- Generate one key before starting. Reuse that key and unchanged input on a
  timeout, lost response, or malformed upstream success. Never automatically
  fall back to the legacy installer or create a new key after an uncertain result:
  Fleet may already have committed.
- Arrays and files are sorted and JSON serialization is stable. The proposal
  hashes raw entity bytes and retains replay results for seven days. Retry
  within that window; after expiry, reconcile the Workspace before creating
  another installation. The adapter itself makes no automatic retry.
- Marketplace re-reads the source on retry; it does **not** store historical
  Definition snapshots. Editing the plugin, dependencies, environment, or
  adapter serialization between attempts can produce a safe 409 rather than a
  replay. Do not claim exact replay across such changes. Retain the original
  client input securely and reconcile before starting a replacement operation.
- Confirmed replays do not increment install metrics. Metrics remain best-effort:
  a lost first success can leave a missing count, without affecting Fleet state.

Before switching Client, verify single expert and multi-member team installs,
custom naming, MCP, provider environment, complete Skill files, cross-Space and
runtime denials, same-key replay, changed-input conflict, and uncertain-response
recovery against Fleet. Keep the legacy route available throughout migration.
