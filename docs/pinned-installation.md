# Pinned installation (local delivery, disabled by default)

Refs #119 and #120. `PLUGIN_PINNED_INSTALLATION_ENABLED=false` preserves the
legacy-content selection through the shared capability executor. The pinned
path requires version management and reference-format released resources.
Activation is a separate operation; this change does not activate a deployment
or migrate data.

## Resolution and access

Both existing Marketplace install entrypoints resolve one common in-memory plan:
root published version → pinned expert versions → pinned skill versions; connector
IDs select the current configuration. Missing/foreign/unreleased pins, embedded
resources, unknown schemas or inaccessible files stop before any Fleet write.
The root AND every dependency (including Connector rows) must currently be
published and pass status/deletion/access checks before Fleet is called. A pin
proves which immutable content was released; it does not grant a delisting
exemption. This applies both to the first dispatch and to same-key retries of a
frozen plan. Retain the pin, release history and any uncertain attempt when
eligibility fails; rejection is not proof that an earlier call did not commit.
Private reads require owner and Space even when the caller is an administrator.
A private target also requires a private source with the same owner and Space;
source audiences are rechecked through each member's frozen references.
Independent-reference writes and publication enforce the source audience for
Space targets too, including Admin and legacy `public` sources: a system/public
source cannot reference a Space target, and Space targets require the same Space.
Only embedded children created/rebuilt within the current graph transaction keep
the existing intra-graph exception.

Frozen-plan recovery validates the schema, root kind, expert/team cardinality,
unique expert/skill names, member/leader wiring and skill references before
constructing bindings. JSON clone failures or malformed persisted definitions
return the existing integrity error without dispatching to Fleet or changing the
attempt. A domain-separated `EligibilityHMAC` in the existing frozen-plan JSON
protects the exact `NodeIDs`/`Edges`, root version and immutable operation context
(caller/Space/workspace/key/adapter/intent/key ID plus input/request fingerprints).
The existing request HMAC authenticates the reconstructed Fleet content/runtime
bindings; the eligibility MAC binds that fingerprint to the governance graph.
Verify eligibility authentication before using the persisted node/edge lists for
current reads, then still verify current eligibility and exact request HMAC.
Neither MAC grants distribution or Workspace access. Unsigned/mismatched plans
fail closed without dispatch, plan rebuild, signing-on-read or attempt mutation.
Exact request HMAC checks and current governance/Workspace authorization
remain mandatory; corruption never causes plan regeneration or a new key.

Delisted legacy data stays historical. Conversion is an explicit operator action
for a specific republish/install need and preserves delisted state; restore the
required source/dependencies through normal publication before installing again.
There is no request-time conversion, automatic re-listing, alternate-version
fallback or permission bypass. See the current
[rollout and incident contract](marketplace-v2-release.md#3-下架历史与按需转换).
This replaces the older released-pin delisting exemption.

Repository reads batch each dependency wave and cache only within the request.
Skills retain their original market names, SKILL.md bytes and supporting files;
there are no generated aliases or frontmatter rewrites. Same-name identical
content can be shared; conflicting content is rejected. Skill bindings preserve
the exact selected version for each expert.
MCP comes from connector resources, never the expert's old embedded mcp.json.
Duplicate MCP server names fail instead of overwriting another connector.
An expert/team snapshot containing inline MCP, skill/member bundles or inline
capability fields is rejected before either installer runs, even when it also
declares connector references. A hidden/missing root returns 404; hidden
dependencies retain their separate permission error.

## Activation checklist

Keep pinned installation off throughout this sequence. Use a separately
authorized SELECT-only account and freeze concurrent publication during the
final checks so the evidence remains current until the authorized cutover.

1. Run M1 (`marketplace-inventory --all-spaces`) and resolve unknown publication,
   missing-release and source-integrity findings. Review the full distribution.
2. Rehearse the needed M3 single-root conversion on an isolated copy, then
   separately authorize its bounded apply window. Verify the resources intended
   for the rollout and their historical indexes. Delisted unused history is not
   automatically converted or re-listed; an incomplete selected graph or
   unresolved exception is not readiness evidence.
3. Check current released graphs with the committed read-only SQL. It checks
   released roots and their reachable pins, schema/index coverage and inline
   capability shapes. Retained old releases not reachable from a root do not
   block the gate. A successful SQL execution alone is insufficient: the blocker
   count must be zero.

   ```bash
   mysql --defaults-extra-file=/secure/read-only.cnf --database=octo_marketplace \
     --batch --skip-column-names < scripts/pinned-release-readiness.sql \
     > /tmp/pinned-readiness.tsv
   python3 - <<'PY'
   from pathlib import Path
   fields = Path('/tmp/pinned-readiness.tsv').read_text().split()
   if fields != ['pinned_readiness_blockers', '0']:
       raise SystemExit('Pinned activation blocked: unresolved released graphs')
   PY
   ```

4. Verify the exact planned skill names, member bindings, MCP discovery and
   execution in isolated real Fleet/Daemon/provider acceptance. The SQL gate
   does not prove artifact availability, runtime discovery or remote permissions.
5. Obtain deployment approval, then enable the prerequisite flags in order and
   monitor failed preparation. Recheck if publication changes after the checks.
   Startup validates flag dependencies; it does not execute this operator gate.

## Retry and rollback

The capability entrypoint records scoped key digests and HMAC fingerprints,
plus the first secret-free frozen definition, selected versions, names, file
bytes, Connector configuration and eligibility edges. Tokens and custom
runtime environment values are not persisted; retries supply the same input.
Normal root/dependency publications and Connector edits do not rebuild an
existing plan; changed explicit input conflicts. Before every resend, current
caller, graph (including listing state) and Fleet workspace permissions remain
mandatory. A delisted/deleted/inaccessible node blocks dispatch without deleting
the original plan, extending its deadline or clearing earlier uncertainty.

Identical eligible retries still reach Fleet, including completed attempts, so
current workspace authorization and the remote receipt remain authoritative.
Records outside the original retry deadline (bounded by Fleet's 24-hour receipt
window) fail closed and need explicit reconciliation; they are not deleted and
blindly retried. No automatic fallback follows an uncertain Fleet outcome.

### Unsigned or damaged eligibility metadata

This update adds no database column or wire field: `EligibilityHMAC` is stored
inside the existing JSON document. Pre-update attempts lack it and are not safe
to resend automatically, including completed receipts. New installations through
either adapter receive authenticated plans normally. Retain the configured old
HMAC keys during rotation so authentic existing plans remain verifiable.

Before rollout, inventory unsigned attempts with a read-only query (select only
operation IDs/state, not full payloads/secrets); rehearse on a database copy. An
unsigned, mismatched or damaged plan returns the existing safe integrity failure.
Pause that operation and reconcile its original key/workspace with Fleet and its
receipt. Preserve the plan, result IDs, deadline and unknown-result evidence.
Do not bulk-sign stored lists, synthesize a signature from unverified metadata,
re-resolve latest content or delete/reset the attempt. Restore only an independently
verified authentic backup when available; a legacy unsigned backup cannot acquire
trusted metadata merely by restoration. If no authenticated record exists, use
explicit incident reconciliation/forward repair, not automatic resend/new-key
fallback. A rollback image must preserve this verification; otherwise keep
installation writes paused. See the rollout runbook for operator steps.

### Expired receipt reconciliation

The 24-hour window is a remote replay guarantee, not permission to delete local
receipts. Local records act as durable tombstones for spent/uncertain keys. There
is no automatic TTL deletion, key reset or GC: table growth is an explicit current
limitation, and must be capacity-planned. Deleting a completed row would allow
the same key to create duplicates after Fleet forgets its receipt.

An HTTP expiry conflict does not say that a prior successful installation failed.
Its completed receipt/IDs are retained, while current workspace access must still
be verified before returning or using that result. The client is told to use the
existing resource if completed, or stop and reconcile if unclear; it is not told
to automatically replace the key.

Use this operator procedure after a key expires (never retry with a new key to
discover whether the previous call committed):

1. Pause the affected client's retries. Obtain authoritative Space, caller,
   workspace and resource IDs from its authenticated context. With a SELECT-only
   account, bind those IDs in this scoped query; it outputs only receipt metadata.

   ```sql
   SELECT key_digest,root_version_id,completed,agent_id,team_id,created_at
   FROM plugin_install_attempts
   WHERE space_id='<verified-space>' AND plugin_id='<verified-resource>'
     AND caller_uid='<verified-user>' AND workspace_id='<verified-workspace>'
     AND created_at < UTC_TIMESTAMP(3) - INTERVAL 24 HOUR
   ORDER BY created_at LIMIT 100;
   ```

2. For `completed=1`, verify the recorded agent/team ID in the same authorized
   Fleet workspace and use that existing result. Do not recreate it or delete
   the receipt. If the instance was intentionally removed, a new installation
   is a new user-authorized operation with a new key, not a replay.
3. For `completed=0`, reconcile Fleet's authoritative workspace instance/audit
   evidence using the original operation key retained by the client. Marketplace
   stores only its hash and cannot reconstruct it or the request environment.
   Record the matching instance IDs if committed; return those existing IDs.
   If the outcome cannot be proved, keep the key blocked and escalate to the
   Fleet operator. A name match or absent 24-hour receipt is not proof of no commit.
4. Only after authoritative evidence proves no instance was created (or an
   explicitly approved cleanup removed it), and the user authorizes another
   installation, submit the normal authenticated request with a fresh key. Keep
   the old tombstone and reconciliation evidence; never SQL-reset its timestamp.

This resolves the client operation without unlocking a spent key. Bulk archival
or receipt compaction requires a separate design that retains key tombstones;
the service does not claim to provide an automated reconciliation endpoint.

Both installation URLs use one capability executor with the same configured
content mode. The legacy URL preserves its old request/response contract; it
has no separate create/bind/compensation provisioner or delisted/private
exemption. Without a client key, an uncertain result requires workspace
inspection before a new operation, not a same-key retry the caller cannot make.

## Verification boundary

Unit tests exercise fixed versions, same-name skills, visibility failures,
frozen Connector configuration, both entrypoints, retry uncertainty and delisted
root/dependency refusal. Dedicated synthetic MySQL tests exercise actual scope SQL, release ownership, concurrent
attempt claiming and root upgrades. These do not constitute I4 acceptance:
unmodified real Fleet/Daemon installation, materialization and skill/tool
discovery still need a separately available isolated environment.
