# Author Delete Listed Plugin

## Goal

Allow a plugin owner to delete their own organization-listed plugin directly,
matching the delete action exposed by the Marketplace UI and the product
decision recorded in Mininglamp-OSS/octo-web#1786.

The source of truth is the issue's 2026-10-10 product-decision section: the
uploader retains the right to delete their own organization-listed plugin, and
does not need a Space admin to delist it first.

## Load-Bearing Behavior

- A caller may delete only a plugin they own in their current Space.
- `space` + `published` is deletable by its owner without a prior admin delist.
- Delete remains a soft delete and preserves version history and artifacts.
- Every soft-deleted plugin row, including embedded graph descendants, is reset
  to `listing_state=draft` so an accidental undelete cannot silently relist it.
- Delete cancels any pending review request and appends the existing audit log.
- Live incoming plugin relations continue to block deletion with the actionable
  `CONFLICT` reason `relation_in_use`.
- Expert and expert-team deletion continues to atomically remove embedded
  descendants through `DeleteGraph`.

## Out Of Scope

- Relaxing the review requirement for edits to an organization-listed plugin.
- Allowing cross-Space or non-owner deletion.
- Changing the delete endpoint URL, request, or success response.
- Frontend changes or a separate delist step.
- Physical artifact or version deletion.

## Acceptance Criteria

- The owner can delete a `published` + `space` skill or connector.
- The owner can delete a `published` + `space` expert or expert team through
  the graph-delete path.
- A listed expert-team delete soft-deletes its embedded expert and skill
  descendants and resets every deleted row to `draft`.
- Version rows and artifact objects survive the soft delete.
- An incoming relation returns `CONFLICT` with
  `details.conflict_reason=relation_in_use` and an actionable hint.
- Existing ownership, Space isolation, review cancellation, audit, and
  soft-delete behavior remains covered by tests.
- `make openapi-check`, `make openapi-diff`, and `go test ./...` pass.
