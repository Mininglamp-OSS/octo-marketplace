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
2. Rehearse M3 on an isolated copy, then separately authorize its bounded apply
   window. All applicable root units must be migrated and historical indexes
   verified. A fresh M3 preview must have zero remaining units and exceptions;
   an interrupted/partial batch is not readiness evidence.
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

The capability entrypoint records scoped key digests, selected root/version,
adapter and input/request fingerprints. No token, environment value, instruction
or complete request is persisted. Root upgrades do not change an existing key's
selection. Changed input or current connector content returns conflict before
Fleet. Identical retries still reach Fleet so current workspace authorization
and the remote receipt remain authoritative. Records older than Fleet's 24-hour
receipt window fail closed and need explicit reconciliation; they are not deleted
and blindly retried. No automatic fallback follows an uncertain Fleet outcome.

### Expired receipt reconciliation

The 24-hour window is a remote replay guarantee, not permission to delete local
receipts. Local records act as durable tombstones for spent/uncertain keys. There
is no automatic TTL deletion, key reset or GC: table growth is an explicit current
limitation, and must be capacity-planned. Deleting a completed row would allow
the same key to create duplicates after Fleet forgets its receipt.

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
