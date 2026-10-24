# Historical reference index

## Goal
Rebuild only provable reference facts from immutable historical snapshots. Default to a read-only preview.

## Background
The reference delivery adds an index, while old snapshots may contain only live IDs. An ID without a released version must never become a guessed latest-version pin.

## Load-bearing list
- Decode the immutable snapshot; validate source, target kind, independent resource and exact release identity.
- Reject a whole snapshot on any ambiguous edge; do not report partial coverage as complete.
- Reports contain identifiers and fixed reason codes, never package/configuration contents.
- Apply locks and revalidates the selected snapshot and evidence, atomically inserts only missing index facts, and rejects conflicting existing facts.
- A repeated or interrupted run can safely revisit completed snapshots; never modify resources, snapshots, releases, files, scope or listing pointers.

## Out of scope
Embedded resource extraction (covered by the separately authorized stock migration tool), fabricated releases, production execution, feature enabling, external APIs and workspace data.

## Acceptance
- Fixture preview covers valid pins, missing pins, wrong-resource versions, unreleased versions, connectors and malformed snapshots.
- Database preview uses a read-only transaction and always rolls back.
- Apply is per-snapshot atomic, idempotent and fails closed on changed evidence or index conflicts.
- Focused tests, full Go tests and build pass. No real tenant database is used.
