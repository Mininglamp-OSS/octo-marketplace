# Expert and skill version management in the combined V1+V2 delivery

## Goal
Deliver the V1 immutable version/artifact foundation and V2 publication/history
capabilities together in PR #113. Preserve PR #112 as its original review record.
Separate editable saved snapshots from formal releases and provide authorized
historical reads and file retrieval. User-authorized fixes are pushed to the fork
branch for review; production activation remains separate.

## Load-bearing behavior
- Feature flag PLUGIN_VERSION_MANAGEMENT_ENABLED defaults off until adoption,
  dependency protection and the M1 inventory are ready.
- Saving never advances the published pointer. Approval releases the frozen
  submission without overwriting a newer saved draft. A first listing
  promotes only the frozen container's own embedded children without replacing
  later live draft relations, so the listed release remains installable.
- Confirmed Admin matrix: a listed system resource publishes on save/import in
  the same transaction; a delisted or draft system resource and every tenant
  resource only advance current. Connectors never enter the version model.
- V1-window compatibility: re-publishing a listed resource with a complete
  snapshot but no release adopts that approved snapshot as the baseline. Space
  resources require an approved review over the same version and content; rows
  without that evidence stay blocked for the M1/M3 inventory and migration.
- Release labels are unique per resource and publication metadata is atomic.
- Users can read only current-authorized releases; same-Space owners and gated
  marketplace admins can manage drafts. Every version lookup matches resource ID.
- Historical files are verified against their original size/digest. Missing or
  corrupt files never fall back to current bytes. Object keys stay private.
- Visibility is A': a private resource may be published and is owner-only; the
  owner's private expert/team may reference the owner's authorized private
  resources. Space is shared within the Space; system is global. Space/system
  sources cannot reference private dependencies.
- Connectors remain current-state resources with existing review behavior.

## Out of scope
Web/Admin UI, Fleet changes, version-pinned dependency resolution and indexes
(R1-R4), restore-to-draft, security-disable, old-format ZIP conversion, stock
resource extraction/migration and physical cleanup. The third implementation PR
includes only provable historical dependency index backfill, not full migration.

## Acceptance
1. v1 release survives v2/v3 saves; frozen v2 approval preserves v3 draft.
2. Private and admin publication, manual and automatic review record releases.
3. Historical list/detail/file reads enforce tenant/owner/release
   access, resource-version matching, integrity, and no private object key leaks.
4. Relevant tests, all Go tests and OpenAPI check/diff pass; baseline PR1 failures
   are tracked separately and final parent changes aligned before delivery.

## Review correction 2026-10-24

The combined V1+V2 delivery is #113; #112 remains the prior review record.
First-listing approval preserves live later-draft relations while promoting only
owned embedded descendants of the frozen container. A standalone referenced
expert and all of its bundled private skills keep their independent publication
boundary. Frozen member traversal checks embedded identity, owner and Space
before descending. This correction does not implement R audience admission or
change any HTTP contract.

Acceptance: service-driven MySQL regressions for manual and automatic team
approval cannot expose a standalone private expert's bundled skill to another
Space member; a team's own embedded expert and skill still promote successfully.
Re-run existing first-approval/later-draft and released-install regression cases.
