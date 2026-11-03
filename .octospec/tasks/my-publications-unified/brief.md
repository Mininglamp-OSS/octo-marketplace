# My Publications: unified editing, audience groups and dependency reminders

## Goal

Implement Marketplace's backend support for the user-confirmed Web My Publications PRD. The approved PRD is Web My Publications Update. User-confirmed exceptions: connectors remain non-versioned; first unpublished resources remain drafts; Bot selection and forwarding remain unchanged.

The paired Web change is in Mininglamp-OSS/octo-web, branch `feat/my-publications-unified`.

## Baseline and ownership

Backend GitHub main `c720e3cbee2ae3b1557140c53b8ba055e5b2b312`; branch `feat/my-publications-unified`. Marketplace owns permission, immutable releases, group decisions, dependency availability and persistent reminder skips. Web owns editor and confirmation presentation. Existing CLI contracts remain compatible.

## Load-bearing behavior

- Preview audience impact before mutation. Group promotion contains only this owner's private, already-published dependencies in the authoritative Space. Traverse immutable relations across all formal versions whose visibility is expanded, including pinned historical team members.
- All participants preserve original scope until approval. Auto/Web/IM decisions apply the group in one transaction; cancellation/rejection preserve scopes, pins, drafts and release counts.
- Persistently occupy the group's participant resources and enforce occupancy in every mutation path, including Connector editing/deletion/admin delisting. Use stable resource-lock order and compare expected baselines.
- Connector audience checks use effective config hash/scope/state, not a synthetic formal version. Do not mint Connector releases or accept pinned Connector edges.
- Confirmed hiding may proceed despite references and atomically suspends affected published consumers. Recovering the dependency must not automatically re-list consumers or update installed copies.
- Update reminders describe available formal direct Skill/member releases, excluding Connectors, drafts and pending releases. Never expose foreign/private/cross-Space content.
- Skip persistence is authenticated user + Space + root resource + dependency + target release. A newer release reminds again. Viewing/canceling/refreshing must not clear reminders.
- Adopting dependencies and publishing has conditional source/draft/target checks and no partial mutation on failure. Team updates do not rewrite member internals.

## Out of scope

Connector formal versioning/history/update reminders; Bot selection or forwarding changes; runtime/install reconciliation; production data changes; auto approval of another owner's dependencies; modifying historical content; reverse cascading root narrowing into dependencies; production deployment.

## Acceptance

Focused repository/service/handler tests and real disposable-MySQL transaction/migration tests. Include cross-Space negatives, concurrent plans/publications/decisions, all-or-nothing group operations, historical pins, retained metadata/drafts, multiple blockers, durable skips and re-alerts. Run `go test ./...`, `go vet ./...`, `go build ./...`, OpenAPI generation/check/diff against GitHub main, and complete target-branch diff review. Verify Web + backend locally; report mock, build, real DB and browser evidence separately.

## Current state

Implementation and independent review complete. Real MySQL transaction/race regression and API-backed browser checks passed; see verification.md. Production-snapshot full apply/verify remains incomplete because the restored trigger definer account is missing from the isolated local instance. Submit the paired changes for review with this limitation explicit.
