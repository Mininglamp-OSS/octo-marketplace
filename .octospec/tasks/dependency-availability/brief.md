# Hide catalog experts with unavailable dependencies

## Goal
Keep an expert or team published while hiding it from the ordinary catalog when a released dependency is delisted, deleted, or has no matching release. Show the reason to its owner and marketplace admins, and restore catalog visibility automatically when the dependency becomes usable again.

## Load-bearing behavior
- Judge the currently published version's frozen references; only pre-release legacy roots use live relations. A later draft cannot change catalog visibility.
- A team is blocked when any member expert or the member's skill/connector is blocked. Names in diagnostics come from released snapshots, not editable skill drafts.
- Catalog list, detail, graph entry, tags, and category counts agree. Mine and admin lists retain blocked roots and return `dependency_issues`; ordinary readers get 404 for blocked detail.
- Do not mutate the root's `listing_state`, create a new version, or enable rollout flags.
- Re-listing the same dependency automatically restores visibility. A change to
  the frozen target version still follows the existing new-release contract.

## Out of scope
Fleet protocol changes, retroactive alteration of immutable releases, production migration, UI implementation, and optional dependents `blocked` enrichment.

## Acceptance
- Delist and restore a skill without republishing its expert/team: public list and detail hide then return, while mine and admin show the reason throughout.
- Deleted and missing-release dependencies, connector delisting, team `via` path, and draft-relation isolation are covered with isolated MySQL tests.
- OpenAPI check/diff, relevant package tests, `go build ./...`, and `go test ./...` pass.
