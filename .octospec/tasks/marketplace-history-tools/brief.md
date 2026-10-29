# Historical marketplace tools follow-up

## Goal
Keep historical name-key inventory/apply and legacy conversion/rollback compatibility separate from the Web runtime delivery.

## Load-bearing behavior
- Preserve same-Space draft/released reservations while comparing only public/system effective releases across Spaces in existing inventories.
- Lock/revalidate name-backfill participation metadata; report applied only after commit.
- Existing conversion writes enforce runtime name rules for newly extracted standalone resources; rollback checks exact post-write name evidence and repeated roots require the recorded digest.
- Hydrated files remain frozen outside inventory locks and included in plan digests.

## Out of scope
Runtime API/editor delivery belongs to the companion PR; PR159 and new conversion architecture are excluded.

## Acceptance
Historical CLI, import and stock migration regressions pass against isolated MySQL. The dependency-only comparison against the runtime branch contains no runtime endpoint or schema changes.
