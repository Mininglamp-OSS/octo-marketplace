# Marketplace single-database migration
Goal: convert the current existing database in place, using one MYSQL_DSN. Copying production data to test is separate environment preparation.
Load-bearing: restore missing Skill descriptions only from intact original frontmatter and block unavailable metadata; platform/Space/owner isolation, agreed duplicate policy, full file bytes, valid releases and fixed references, stable IDs, transactional receipts and same-run resume, immutable history, safe error reporting.
Scope: Go command/core/tests/docs plus the required platform attachment ownership support in Marketplace. No database export/restore, schema upgrade, deployment, feature-gate removal or cross-database copying.
Acceptance: planner negative isolation cases; database binding; private checkpoint integrity; object digest and ownership; conversion and retry tests; full Go test/vet/build. Real MySQL and test environment runs must be reported separately when actually executed.
Operator guide: docs/production-marketplace-migration.md.
