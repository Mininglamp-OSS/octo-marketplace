# Independent resource references

The `PLUGIN_REFERENCES_ENABLED` rollout gate defaults to `false` and also requires
`PLUGIN_VERSION_MANAGEMENT_ENABLED`. This document does not authorize enabling either.

## Write contract

Existing User/Bot upsert and Admin create/update DTOs accept optional
`relations[].target_version_id`. With references enabled, expert skills and team
members require the exact formally released target version; connector references
must omit it and use the current authorized connector configuration by ID.
Targets must be independent resources. Team relation data permits only
`member_key`, `role`, `is_leader`; exactly one member leads. Role text is limited
to 500 Unicode characters and the reserved `leader` role cannot identify a nonleader.

Private sources may reference their owner's published private targets in the same
Space; Space sources use same-Space Space or system targets; system sources use
system targets. Placement does not establish eligibility. Admin privilege never
widens a source audience. Frozen member versions are checked recursively.

Container uploads use `container_schema_version=2` with `schema_version:"2"` in
one root `expert.json` or `squad.json`. Skills use `{id,version_id}`, connectors
`{id}`, members `{id,version_id,member_key,role,is_leader}`. Inline child ZIPs,
MCP configurations, member ability overrides and unknown manifest fields fail
closed. Import/update reuse existing Admin publication semantics and never delete
shared targets. With the gate disabled, existing legacy container handling remains.

## Publication and lifecycle

The same transaction rechecks target ownership, audience, release identity and
listing state during saves, review submissions, approval and publication. An
unchanged existing pin may remain in a draft after target delisting; new pins and
new publication require listed targets. Existing pins and release evidence are
retained as history, but installation preparation and frozen-plan retries require
both the root and all dependencies to be currently published and authorized.
A retained pin does not allow a new Fleet dispatch after target delisting. Releasing a source never follows a target's latest
editable version. Saved snapshots and their reverse index are written together.

Previously published/delisted Skills and Experts may be soft-deleted only when
no protected live, current published or Team-pinned indirect incoming reference
survives. Historical releases remain; delisting alone does not release incoming
references. This does not change the existing visibility-narrowing policy.
Deleting a source never deletes an independent target. Impact queries remain
diagnostic, not deletion authorization. See [the current lifecycle and rollout
contract](marketplace-v2-release.md).

## Published graph identity

`GET /plugins/detail_graph` traverses each released target by
`(plugin_id, target_version_id)`, rather than collapsing every release of one
plugin into a single node. `related_plugins` may repeat `plugin_id` for distinct
released versions. Match a pinned relation to the node with that `plugin_id` and
`current_version_id == target_version_id`; the existing `current_version_id`
field describes the selected immutable snapshot, not a later editable version.
Names, manifests and hashes all come from that same selected release. No new
wire field is added, and full packages/storage attachment keys remain omitted.

The same release shared by multiple edges is projected/expanded once; all its
incoming edges remain. Legacy unpinned edges select the current published
release, while Connector edges retain their existing current-resource behavior.
If a selected release is unavailable after root admission, omit only that
version's incoming edges, preserving other readable releases of the same plugin.
Current caller/Space access, root diagnostics and graph-size limits still apply;
this is not an exemption for hidden/deleted dependencies. Installation resolution
and its authenticated frozen plans are unchanged by the graph projection fix.

## Diagnostic reads

- `GET /plugins/{plugin_id}/dependents` and Admin equivalent: authorized historical
  direct and indirect consumers, optional `version_id`, standard offset pagination.
- `GET /plugins/{plugin_id}/reference_candidates?relation_type=...`: compatible
  independently listed targets, published version identity, `already_bound`,
  `is_selectable`, and an actionable unselectable reason. No placement requirement.
  Owners in the source Space see editable bindings. Other public-route readers,
  including non-owner platform admins, see released bindings; this route never
  promotes a caller into repository admin scope.

Names in diagnostics come from released/historical snapshots rather than an
unpublished editable version. Dependents fail with `409 CONFLICT`, reason
`reference_index_incomplete`, when any caller-readable historical snapshot lacks a
verified index marker or its digest/count mismatches. A zero-edge snapshot also
requires a marker. Run verified backfill before relying on historical diagnostics.
The digest is SHA-256 over the exact MySQL-returned `relations_json` UTF-8 bytes.

## Verification

`TestReferenceDelivery*` uses isolated MySQL databases and covers private positive
and cross-Space negatives, wrong/unreleased pins, immutable indexes, delisting
revalidation, frozen review pins, lifecycle, relation ID preservation, connector pin
clearing, transitive historical impact, candidate disabling and draft-name privacy.
Package tests additionally cover container rejection and metadata/audience rules.
No production migration or rollout has been performed.

## Concurrent writes and legacy approval

Internal mutations with a nonempty `ExpectedCurrentVersionID` compare the saved
head under the resource row lock and reject stale writers atomically. This is an
opt-in internal guard, not a new wire field or a claim that all public saves
require an expected version. Explicit publication separately checks its selected
version under the same lock.

With version management disabled, legacy approval first replaces live relations
with the frozen review graph, then promotes embedded children in the same locked
transaction. The live table at that point contains the reviewed graph, not the
later draft. `TestApproveAppliesTheFrozenRelationGraph` covers a draft-only member
added after submission and verifies that member stays private.
