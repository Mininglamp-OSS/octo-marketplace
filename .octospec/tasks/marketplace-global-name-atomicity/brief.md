# Atomic global marketplace names

## Goal
Resolve PR161 review feedback by enforcing published public/system name uniqueness across Spaces at the database boundary.

## Load-bearing behavior
- Normalized effective published names share one namespace per resource type, independent of Space and transaction isolation.
- Only active, non-deleted, standalone, published public/system resources occupy that global namespace. Private resources, unpublished drafts, delisted rows and embedded resources keep their existing policy.
- Concurrent losers return the existing name-conflict or retryable-deadlock error and roll back publication evidence.
- Stock conversion publishes a new canonical snapshot; its name key matches that snapshot, while distinct unpublished drafts remain blocked without writes.

## Out of scope
New endpoints, changes to tenant authorization, automatic renames, global reservation of private or draft names, and changes to PR159 conversion architecture.

## Acceptance
Use isolated MySQL to reproduce concurrent cross-Space creates at READ COMMITTED, then prove one global commit for concurrent creates and publications across all three named resource types. Verify schema upgrade/down and exclusions, migration draft-name rejection, repository-wide tests, vet/build and unchanged OpenAPI.
