# Stock resource migration

## Goal
Refs #123. Convert existing embedded skill/expert graphs and expert MCP attachments
into independent references through an explicitly operated, resumable batch tool.

## Background
Legacy container imports create embedded rows and only snapshot the root. The new
writer rejects inline bundles. Historical rows must remain immutable and readable.

## Load-bearing list
- Preview is read-only, deterministic and redacts package/configuration contents.
- Inherit root owner, Space, visibility and listing; never widen beyond that root.
- Order skill extraction, current connector extraction, member expert releases,
  root expert/team snapshot; connectors have no versions.
- New release identities never mark an old snapshot released. Keep old snapshots,
  files and rows. New container label advances deterministically.
- Generated labels obey the ordinary nine-digit-per-component write contract,
  exceed historical numeric labels, and leave room for a later publication.
  Unresolvable numeric bounds quarantine the root before any write.
- Rebuild source evidence under transaction locks before apply. Atomic root units
  and immutable batch mappings support interruption and repeat without duplicates.
- Any unresolved exception blocks apply; operator must affirm G4 policy externally.
- Rollback restores pointers, content and listing only if migration output is still
  current; keeps new rows/files/history and retires the batch against further apply.

## Out of scope
Production execution, feature enablement, old ZIP conversion/upload, physical GC,
remote object retrieval or unverified storage materialization. External/legacy
file pointers are explicit exceptions, requiring a separately verified input path.

## Acceptance
Dry run reconciles resources and reports exceptions. Isolated fixtures cover all
visibility/listing combinations, skills/MCP/member extraction, role/order/leader,
unchanged history, missing evidence, idempotent resume and pointer rollback.
Package tests, full Go tests and build pass; no HTTP endpoint is introduced.
Real SQL migration followed by the public edit and publish service paths passes
for unorderable legacy labels and bounded patch rollover.
