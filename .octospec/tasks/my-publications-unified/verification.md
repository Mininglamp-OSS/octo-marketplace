# Verification — 2026-11-03

Local implementation is submitted for review. No production deployment or mutation. Snapshot migration acceptance remains pending; see below.

## Real database checks

Docker API and old TCP MySQL instances do not respond. Tests instead use an isolated MySQL 8.4.11 instance, Unix socket only, networking disabled. No existing Docker containers or databases were restarted/deleted.

- Full `go test -p 2 -race -shuffle=1793684154209985000 -count=1 ./... -timeout=12m` passed: 43 packages; internal/db 334.486s, internal/migration 157.033s. TEST_MYSQL_DSN and MIGRATION_TEST_MYSQL_DSN set; CI=1 and MIGRATION_TEST_MYSQL_REQUIRED=1 prohibit silent DB skips. Evidence: `publications-real-full-race-fixed.log` (local evidence).
- Subsequent affected-package race run after historical-member cascade fix passed (db 192.941s): `publications-final-affected-race.log` (local evidence).
- New fail-first legacy test reproduced empty impact for published resources without formal release registration. Fixed live fallback for unpinned legacy published roots, preserving exact historical pins. Focused real DB race passed: `publications-legacy-cascade-green.log` (local evidence).
- Final affected-package race run including that compatibility fix PASSED (db 163.829s; repository 1.528s; service 14.249s; handler 1.490s): `publications-final-legacy-affected-race.log` (local evidence).
- Covered linked visibility auto/manual approve/reject/cancel, same-plan concurrent confirmation, pending participant occupancy, injected storage rollback, cross-owner private impact/redaction, exact opaque draft-reference retention and strict publication, stale/duplicate/cross-Space adoption, durable reminder skips, manual-review frozen dependency adoption, hide/recovery requiring explicit republication, older pinned teams paused through globally paused members, and legacy Expert/Team compatibility.
- Existing tenant pending-edit tests now enforce rejection; admin repair still verifies review snapshot isolation. Existing migrated incomplete-Team draft test exposed an over-strict retained-reference helper; empty-ref draft save repaired, formal publication remains strict.

## Web and actual API/browser

- MCP 41 files / 549 tests; Skillmarket 28 files / 392 tests; dmworkbase 583 files / 6247 tests: 7188 total passed. Base suite used maxWorkers=2 to fit host memory. Logs: `publications-{mcp-retested,skill-retested,base-full-limited}.log` (local evidence).
- Direct Web production build and i18n check passed. Backend build passed before final graph-only changes; affected-package vet passed after the final compatibility fix.
- Native Chrome ran actual PublicationEditor, DependencyUpdateFlow and VisibilityModal against current local API routes and real synthetic MySQL records, with a minimal host harness. Save draft persisted across reopening while published version stayed fixed; skip persisted; manual adoption included saved draft and published atomically; scope confirmation promoted own private Skill with Expert; organization editor published new version. Unadopted newer dependency remained a reminder.
- Direct API readback verified hide pauses consumer, restore does not relist, explicit publication clears suspension. Connector scope/edit flow produced zero formal plugin_versions.
- Browser integration found and fixed actual GET/PATCH APIClient baseURL routing bugs (two fail-first adapter regressions), historical version response `data.version` contract, and opaque dependency fallback. No business API mocking. Local auth disabled for synthetic user; no real IM/Bot send, installation, connector execution, or production auth claim.
- Browser/API evidence: `publications-{api-regression,connector-regression,browser-api-readback}.json` (local evidence). Temporary tracked entrypoints/helper sources removed, browser and HTTP servers stopped.

## Production snapshot limitation

Current main includes earlier migration hardening and publication baseline fixes. A local copy of an existing production backup was restored, all 4511 object files (512720733 bytes) checked against size/hash records, and all three new schema migrations applied. Current migration preview passed for 702 target nodes / 2135 source mappings with no plan issues.

Full snapshot apply, independent verify, repeat apply and complete converted-content audit have NOT passed: the first node is blocked by MySQL 1449 because the restored triggers reference an absent source definer account. No node committed. This is a separate local migration-environment limitation, not a passing full rehearsal. No production connection or mutation occurred.

## Review and limitations

- Independent read-only reviews identified and verified fixes for callback request generation, private consumer omission, opaque dependency loading, historical member suspension propagation, and legacy consumer fallback. Latest incremental review found no new definite blocker.
- OpenAPI regeneration, coverage, consumer contract and lint passed (0 errors, 14 warnings); semantic diff vs github-upstream/main has no breaking changes. Committed-baseline verification is run after creating the reviewed commit, before push.
- Whole-app TypeScript checking still reports existing React/jsx-runtime/class resolution errors. Root Turbo lint/build fails macOS keychain/TLS setup before task scheduling; no full lint/typecheck success claimed. Direct Web build succeeds.
- Reverse impact scans same-Space published Expert/Team history; large-Space performance is not load-tested.

## Consolidated backend and Web follow-up

Regression checks cover imported bundled Skills without standalone release pointers, persistent suspension after administrator delist, both review policies for unpinned Connector recovery, direct configuration reminder dismissal/adoption, rejection of stale Connector approvals, and initialization/replay of stock-conversion baselines. Real HTTP integration used the paired Web publication service and API client against a Go handler and isolated MySQL. It exercised preview, dismissal, adoption of unsaved edits and readback with the Connector relation still unpinned.

Deploy the unified migrations (including `20261103-03-plugin-connector-baselines.sql`) before the paired Web change. If a database has previously applied the separate PR #180 schema, inspect `SHOW CREATE TABLE plugin_dependency_suspensions` and the migration receipts first. The unified schema records causes by `(plugin_id, source_plugin_id)`; the alternative single-marker schema requires an explicit reconciliation before deployment.

## Private Connector publication RC follow-up

The optional draft revision is now rechecked immediately after the owner-scoped row lock in `PublishPlugin`, before any write. A stale request returns `ErrConflict`; callers omitting the condition retain their existing behavior. Added a fail-first repository rollback test and a real-service/MySQL regression for an interleaved save, an unchanged draft, and an omitted condition.

Repository race tests, focused publication/draft service and handler race tests, `go vet ./...`, `go build ./...`, and the OpenAPI five-gate check passed. The MySQL fixture canonicalization also passed. The three new MySQL transaction cases remain unexecuted locally because Docker access approval timed out and then failed due to approval-service capacity. The full race run in the restricted sandbox failed when existing HTTP tests tried to bind loopback ports; it is not reported as passing. Independent review of the final increment found no remaining definite issue. Remote CI is a separate check after push.

## Embedded-child suspension recovery RC follow-up

Octo-Q's request-changes finding on `a06459e`: after a dependency delist suspended a container and demoted its embedded children, an explicit republish could never recover. `recordRelease` deleted the suspension marker only when the candidate release passed the catalog availability check, but that check ran before the container's embedded subtree was promoted, so the still-demoted children returned `target_not_published` on every attempt and the rollback made the state permanent. At that point the child cannot be republished on its own.

`clearRecoveredSuspension` now reads the candidate release's immutable topology and restores the container-owned embedded subtree it declares (`promoteFrozenEmbeddedChildren`) before evaluating the availability check. The promotion shares the publication transaction: an independent dependency that is still unavailable fails the check and rolls the promotion back, while standalone (non-embedded) targets are never touched.

`TestSuspendedContainerRepublishRestoresEmbeddedChildren` exercises a real MySQL database under both review policies: published Space Expert with a bundled Skill and an unpinned Connector, delist the Connector (cascade suspends the Expert and demotes the Skill), a republish while the dependency is down (fails; pointer, marker and child unchanged), restore the Connector, then republish. It asserts the Expert and the bundled Skill return to `published`, the marker is gone and the published pointer advances. The test fails on the previous implementation (`relations: target_not_published`) and passes with the fix.

Focused `-race` runs for the affected DB scenarios (suspension, delist, managed publication) and the repository package passed. The full `internal/db` MySQL suite passed every test touching this change; eight pre-existing `plugin_data_migration`/`plugin_reminder_migration` cases fail identically on the unmodified head in this environment with `persistence_conflict` from the local migration fixture, so they are not attributed to this commit. `go build ./...` and `go vet ./...` passed. `golangci-lint` is not installed locally, so `make lint` was not run.
