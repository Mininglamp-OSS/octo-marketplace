# Pinned installation (local delivery, disabled by default)

Refs #119 and #120. `PLUGIN_PINNED_INSTALLATION_ENABLED=false` preserves the
existing paths. The new path requires version management and reference-format
released resources. Activation is a separate operation; this change does not
activate a deployment or migrate data.

## Resolution and access

Both existing Marketplace install entrypoints resolve one common in-memory plan:
root published version → pinned expert versions → pinned skill versions; connector
IDs select the current configuration. Missing/foreign/unreleased pins, embedded
resources, unknown schemas or inaccessible files stop before any Fleet write.
Existing pins can use delisted dependencies; the root must still be listed.
Private reads require owner and Space even when the caller is an administrator.
Source audiences are rechecked through each member's frozen references.

Repository reads batch each dependency wave and cache only within the request.
Same-name skills receive stable `-mk1-<digest>` names from resource/version IDs;
only temporary SKILL.md frontmatter changes. The market keeps its original name
and bytes. Skill bindings preserve the exact selected version for each expert.
MCP comes from connector resources, never the expert's old embedded mcp.json.
Duplicate MCP server names fail instead of overwriting another connector.

## Retry and rollback

The capability entrypoint records scoped key digests, selected root/version,
adapter and input/request fingerprints. No token, environment value, instruction
or complete request is persisted. Root upgrades do not change an existing key's
selection. Changed input or current connector content returns conflict before
Fleet. Identical retries still reach Fleet so current workspace authorization
and the remote receipt remain authoritative. Records older than Fleet's 24-hour
receipt window fail closed and need explicit reconciliation; they are not deleted
and blindly retried. No automatic fallback follows an uncertain Fleet outcome.

The legacy entrypoint uses the same prepared plan, then its existing create/bind
operations. It is not globally idempotent. A shared skill is created once and
bound to every declaring member. Rollback deletes only IDs created by this
attempt, separately from IDs merely bound to another member.

## Verification boundary

Unit tests exercise fixed versions, same-name skills, visibility failures,
connector updates, both entrypoints, retry drift and partial rollback. Dedicated
synthetic MySQL tests exercise actual scope SQL, release ownership, concurrent
attempt claiming and root upgrades. These do not constitute I4 acceptance:
unmodified real Fleet/Daemon installation, materialization and skill/tool
discovery still need a separately available isolated environment.
