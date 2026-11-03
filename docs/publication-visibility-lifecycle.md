# Publication audience and dependency lifecycle

Enabled with `PLUGIN_VERSION_MANAGEMENT_ENABLED`. This implements PRD F3 and the related F6 lifecycle boundaries. Dependency update suggestions and per-user dismissals (F4/F5) are separate work.

## Owner audience changes

1. `GET /api/v1/plugins/{plugin_id}/visibility_preview?visibility=private|space` returns the root, owned private dependencies to widen, readable affected published resources to suspend, and `plan_revision`. Names of other users' private resources are omitted. The preview changes no resource data.
2. Show the complete plan and obtain confirmation. `PATCH /api/v1/plugins/{plugin_id}/visibility` accepts `visibility`, `plan_revision`, and `is_confirmed`. Versioned resources also require `expected_published_version_id`; Connectors require the preview root's `revision` as `expected_revision`.
3. A plan that adds dependencies or suspends incoming resources requires explicit confirmation. A changed plan returns a conflict; refresh the preview before retrying. The original resource and every dependency remain unchanged on failure.

Single-resource requests with no extra dependencies/impacts retain the existing published-version precondition contract. All four resource types support private/Space audience changes; Connectors keep their current configuration model and gain no release history.

## Atomic audience review

Widening follows the Space review policy. Each batch member has a pending visibility review, so existing owner-list and pending-write guards apply to every member. The change response includes `review_group_id`; review list/detail responses include that ID and `audience_members` (review ID, resource ID, frozen name). The approval notification explicitly describes a batch decision.

Approving, rejecting, or withdrawing any batch member settles the whole batch atomically. A stale resource, changed release pointer, inaccessible dependency or invalid original file prevents approval of the entire batch. Before approval each resource retains its original effective visibility. Audience approval does not replace drafts, release bytes, version labels or pinned relations. Existing pre-batch pending visibility requests remain decidable.

## Dependency suspension

Confirmed narrowing and administrator delisting suspend the affected published experts and upper teams. Suspensions are stored in `plugin_dependency_suspensions`, and affected resources become delisted. Embedded children follow the container's listing state. Workspace copies are untouched.

Owner list/editing reads expose `dependency_status`:

- `unavailable`: a suspended resource still has an unavailable dependency.
- `restored`: its dependencies are available again, but it remains delisted.
- Omitted: no persisted dependency suspension.

Restore dependencies from the leaves upward, then explicitly publish the affected resource using the normal version/review rules. Successful publication clears its suspension in the same transaction. Rejection, cancellation and failed publication retain the prior release, draft and suspension. Recovery alone never relists a suspended resource.

Version-managed tenant edits are refused while a review is pending. System administrative repair remains available; stale batch baselines prevent such edits from silently changing an audience decision.

## Schema and verification

The embedded migration `20261103-00-plugin-audience-lifecycle.sql` adds audience review group/baseline metadata and suspension storage. Application startup applies it through the existing migration runner. No legacy content or file conversion is required by this migration.

Regression coverage uses real MySQL for atomic manual/automatic decisions, cancellation/rejection, stale baselines, owner/Space isolation, transitive team suspension, recovery/republish and Connector configuration concurrency. Frozen approval and installation retries retain their existing content/pin guarantees across the lifecycle changes.
