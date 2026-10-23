# Expert/skill publication and historical management (V2 of the 14-PR baseline)

## Goal
Build on the V1 immutable version/artifact foundation (PR #112). Separate editable
saved snapshots from formal releases and provide authorized historical reads, file
retrieval and restoration to a new draft. Develop locally on the user's fork;
create no pull request until the user has tested.

## Load-bearing behavior
- Feature flag PLUGIN_VERSION_MANAGEMENT_ENABLED defaults off until adoption,
  dependency protection and the M1 inventory are ready.
- Saving never advances the published pointer. Approval releases the frozen
  submission without overwriting a newer saved draft.
- Confirmed Admin matrix: a listed system resource publishes on save/import in
  the same transaction; a delisted or draft system resource and every tenant
  resource only advance current. Connectors never enter the version model.
- Release labels are unique per resource and publication metadata is atomic.
- Users can read only current-authorized releases; same-Space owners and gated
  marketplace admins can manage drafts. Every version lookup matches resource ID.
- Historical files are verified against their original size/digest. Missing or
  corrupt files never fall back to current bytes. Object keys stay private.
- Restore copies an old snapshot to a new draft with provenance and a new forward
  version label; it does not publish or change the source snapshot.
- Connectors remain current-state resources with existing review behavior.

## Out of scope
Web/Admin UI, Fleet changes, version-pinned dependency resolution and indexes
(R1-R4), old package conversion, production migration and physical cleanup.

## Acceptance
1. v1 release survives v2/v3 saves; frozen v2 approval preserves v3 draft.
2. Private and admin publication, manual and automatic review record releases.
3. Historical list/detail/file reads and restoration enforce tenant/owner/release
   access, resource-version matching, integrity, and no private object key leaks.
4. Relevant tests, all Go tests and OpenAPI check/diff pass; baseline PR1 failures
   are tracked separately and final parent changes aligned before delivery.
