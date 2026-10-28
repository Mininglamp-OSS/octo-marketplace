# Reviewed Skill publication must have a formal release

## Goal

Every successful Skill review decision records the exact approved snapshot as a
formal release in the same transaction as the published listing and approved
review state, including approvals handled by the compatibility path.

## Load-bearing behavior

- Share snapshot-and-release persistence between managed and compatibility review
  approval; preserve the managed path's separate editable head.
- A release failure rolls back snapshot, publication, review decision and audit.
- Formal approval snapshots carry a complete reverse index before a reference
  rollout; creating release evidence must not introduce missing-index blockers.
- Exercise the actual public router, ZIP upload/parse/import, manual and automatic
  review, and exact released-version references with real isolated MySQL.
- Preserve tenant isolation, frozen reviewed content, Connector behavior and
  rollout-specific editing rules.

## Out of scope

- No direct repair of shared test/production data, blanket release backfill, or
  environment configuration changes.
- No claim that the reported all-enabled deployment has been reproduced unless
  the HTTP regression demonstrates it; retain that distinction in the PR.
- No new API, error enum, or LLM runtime configuration.

## Acceptance criteria

- Manual/automatic Skill approvals have a matching published pointer and release
  under both version-management modes; switching to managed reads exposes them.
- An expert can pin the approved Skill snapshot with independent references on.
- Release persistence failure leaves a pending review and unpublished resource.
- Cross-Space reads and references fail closed.
- Relevant integration tests, full Go tests, vet and OpenAPI check/diff pass.
