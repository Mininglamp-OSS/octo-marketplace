# Preserve exact dependency pins in review submissions

## Goal
Forward target_version_id from the review HTTP request into the frozen submission so valid Expert and Expert Team upgrades can pass released-reference validation.

## Load-bearing behavior
- Review HTTP-to-service translation preserves exact submitted pins.
- Review detail exposes the exact frozen target_version_id for reviewer inspection.
- Missing and unreleased pins remain rejected; Space isolation remains enforced.
- Review approval creates a new immutable released snapshot without rewriting historical snapshots.

## Out of scope
Skill compatibility release creation (PR #152), historical backfills, rollout policy changes, and remote resource mutation.

## Acceptance criteria
- Handler regression fails before the fix and passes afterward.
- Production-router MySQL test recovers an unpinned listed Expert with no release through both manual and automatic new-version review.
- Frozen release and reverse reference index retain the requested exact pin.
- Cross-Space and missing/unreleased pin requests remain rejected.
- OpenAPI validation/diff and full Go test suite pass.
