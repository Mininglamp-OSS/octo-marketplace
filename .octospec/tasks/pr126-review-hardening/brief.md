# PR126 migration and operational review follow-up

## Goal
Address the review on `94de531`: protect stock migration audit history from
schema downgrade, index actual operational access paths, and verify scan plans.

## Load-bearing list
- Reject stock-audit schema Down explicitly, retaining data and migration receipts.
- Add indexes in a forward migration so existing PR3 schemas receive them.
- Match batch discovery and reverse-application ordering; preserve scoped
  installation retry identities and provide scoped resource/time lookup.
- Verify evidence ORDER BY columns with real MySQL EXPLAIN and index metadata.
- Trace pinned installation authorization, migration transaction/CAS invariants
  and secret handling in all three files called out by the reviewer.

## Out of scope
Production operations, flag activation, PR merge, review dismissal, changing
HTTP contracts, and representing agent review as independent human approval.

## Acceptance
Fresh and upgrade migrations pass; Down refuses without deleting audit rows;
reversible pre-audit migrations retain their existing Up/Down coverage. Focused
MySQL and regression tests, full Go suite, formatting, Vet and build pass.
Record scan index evidence and the remaining human-review requirement.
