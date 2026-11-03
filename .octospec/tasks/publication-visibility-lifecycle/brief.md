# F3/F6 publication audience and dependency lifecycle

Goal: implement PRD F3 and the related F6 state/transaction boundaries as the first independently reviewable backend PR. F4/F5 update reminders/dismissals follow in a separate PR. User confirmed Connectors stay current-state resources; later reminders identify configuration revisions, not historical Connector releases.

Load-bearing: owner/Space scoped impact preview and stale-plan confirmation; all four resource types; traverse immutable/pinned released graphs; deduplicate owned private dependencies and batch audience reviews with atomic approve/reject/cancel; referenced narrowing allowed only after explicit impact confirmation; persist dependency suspension so recovery never automatically relists; pending review blocks tenant edits; no mutation of draft/release bytes/labels/pins or workspace copies; bounded graphs and safe errors.

Compatibility: retain existing single-resource audience requests when no additional dependency/impact confirmation is required. New Connector audience writes use expected_revision because they have no immutable published pointer. Existing legacy publication/review semantics remain feature-gated. Administrative content operations remain unchanged apart from maintaining dependency safety.

Acceptance: real MySQL tests for private/Space + auto/manual grouped decisions, stale/replayed plans and changes during review, transitive/pinned dependencies, explicit referenced narrowing, recovery/republication, Connector audience, cross-Space/non-owner failures and rollback. Full Go tests, OpenAPI check/diff and generated specification required. No deployment or production data changes.
