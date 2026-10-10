# Author Delete Listed Plugin

## Goal

Allow a plugin owner to delete their own organization-listed plugin directly,
matching the delete action exposed by the Marketplace UI and the product
decision recorded in Mininglamp-OSS/octo-web#1786.

## Load-Bearing Behavior

- A caller may delete only a plugin they own in their current Space.
- `space` + `published` is deletable by its owner without a prior admin delist.
- Delete remains a soft delete and preserves version history and artifacts.
- Delete cancels any pending review request and appends the existing audit log.
- Live incoming plugin relations continue to block deletion.
- Expert and expert-team deletion continues to atomically remove embedded
  descendants through `DeleteGraph`.

## Out Of Scope

- Relaxing the review requirement for edits to an organization-listed plugin.
- Allowing cross-Space or non-owner deletion.
- Changing the delete endpoint URL, request, response, or error envelope.
- Frontend changes or a separate delist step.
- Physical artifact or version deletion.

## Acceptance Criteria

- The owner can delete a `published` + `space` skill or connector.
- The owner can delete a `published` + `space` expert or expert team through
  the graph-delete path.
- Existing ownership, Space isolation, incoming-relation, audit, and soft-delete
  behavior remains covered by tests.
- `make openapi-check`, `make openapi-diff`, and `go test ./...` pass.
