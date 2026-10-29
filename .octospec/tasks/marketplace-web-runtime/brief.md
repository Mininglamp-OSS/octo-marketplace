# Marketplace Web runtime backend

## Goal
Support merged Web #1865/#1867 from current main with runtime name reservations, draft concurrency/notes, and same-release owner visibility changes.

## Load-bearing behavior
- Skill/Expert/Team create/update/release/approval normalize and reserve draft/released names; tenant writes reject existing published public/system market names without exposing foreign private or unreleased global draft names.
- Owner-only draft reads return revision and current unreleased note; writes check revision under lock and preserve omitted notes.
- Audience changes retain immutable release identity/content, owner/Space access checks, pending-review gates, embedded graph boundaries and visible catalog placement.
- Required nullable name columns/reservation triggers and visibility-review evidence are independently deployable; historical NULL-key rows remain guarded during runtime writes.

## Out of scope
Historical name backfill CLI, legacy import/stock migration tooling and rollback, PR159, runtime/agent lifecycle or installation policy redesign.

## Acceptance
Run appropriate runtime regression tests against isolated MySQL, full Go suite, vet/build and OpenAPI check/diff. New PR is based on origin/main 73a1f9f; PR144 follows as a historical-tool dependency.
