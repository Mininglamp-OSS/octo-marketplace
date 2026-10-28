# Production snapshot Marketplace migration
Goal: one-entry isolated test rehearsal from immutable production-shaped snapshots.
Load-bearing: read-only source export, Space/owner scope, explicit canonical policy, complete file bytes, stable IDs and versions, transactional receipts, immutable history, accurate partial/error reporting.
Scope: Go tooling/tests/docs plus Marketplace platform attachment read/write/version ownership compatibility and immutable storage guards; no new endpoints, deployment, production write, or test overwrite. Submission targets the upstream GitHub main branch.
Acceptance: planner negative isolation cases; snapshot tamper detection; real MySQL restored graph/version/ref/index validation; idempotent rerun and interrupted resume; binaries build; no raw secret logging.
Operator guide: docs/production-marketplace-migration.md.
