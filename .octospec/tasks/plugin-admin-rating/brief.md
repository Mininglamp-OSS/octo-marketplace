# Plugin administrator rating

## Goal

Add one nullable administrator-assigned 1–5 rating to every unified `plugins` row and expose a dedicated market-admin update endpoint.

## Load-bearing behavior

- `plugins.rating` is a nullable unsigned tinyint. The API/service contract constrains administrator-assigned values to the inclusive range 1–5; the database intentionally has no enforced range `CHECK` so adding the column remains an explicit `ALGORITHM=INSTANT` metadata-only migration.
- `PATCH /api/v1/admin/plugins/{plugin_id}/rating` accepts a required JSON `rating` property whose value is `null` or an integer from 1 through 5.
- The endpoint uses only the existing `RoleMarketAdmin` route gate and the existing cross-Space admin repository scope.
- Rating updates append a plugin audit record in the same transaction using action `rate`; because rating is outside snapshots and hashes, the remark records the explicit `rating:<before>-><after>` transition.
- Rating updates modify only `plugins.rating` and `plugins.updated_at`; they never create a plugin version or modify manifest, package, attachment keys, version pointers, or hashes.
- Plugin list and detail responses expose `rating` as nullable inside the plugin object; the rating PATCH returns the standard data envelope containing `{plugin_id,rating}`.

## Out of scope

- User ratings, rating aggregation, reviews, ranking changes, and metric counter changes.
- Changes to plugin content, versions, manifests, packages, attachment keys, or hashes.
- New authorization rules beyond the existing market-admin gate.

## Acceptance criteria

- Migration adds the nullable unsigned tinyint with an explicit `ALGORITHM=INSTANT`, no enforced database range `CHECK`, and a replay guard, and removes the column using the next repository migration ID.
- Model, repository, service, handler, route, DTOs, and generated OpenAPI are aligned.
- Repository, service, handler/router, DTO/OpenAPI, and migration behavior have focused tests.
- Relevant Go tests and OpenAPI checks pass.
