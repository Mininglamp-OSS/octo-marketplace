# Connector display-name uniqueness

Goal: apply existing Skill/Expert/Expert Team standalone display-name reservations to MCP connectors.

Load-bearing: normalized repository/service write validation; Space/type unique keys and reservation triggers via a new ordered migration; legacy NULL-key and published global name checks; stable ErrNameTaken/409 mapping across create, rename and review approval. Connector version behavior remains unchanged.

Out of scope: automatic historical rename/backfill, embedded connector uniqueness, frontend changes, other plugin lifecycle rules.

Acceptance: connector NFKC/trim/case duplicates reject in the same Space, including delisted rows and legacy NULL keys; foreign private rows do not affect another Space; different plugin types may reuse names; published global connector names block collisions; concurrent writes and approval/rename use existing constraints. Migration applies without multiStatements. MySQL integration and Go package tests cover these paths.
