# Reference release readiness

Local implementation is not deployment approval. Default configuration keeps version management, reference writes and pinned installation disabled. No production migration, real-tenant inventory or feature activation is performed by the tools or tests.

## Delivery order

1. Land version/release foundations (PR124), then R1–R4. Preserve append-only release identities and historical files.
2. Land the installation/M1/M3 delivery. Inspect the read-only inventory and migration exceptions on a separately authorized environment before any data operation.
3. Rehearse migration on an isolated copy: verify inherited owner/Space/visibility/listing, dependency pins, repeated runs, interruption and pointer rollback. Unresolved exceptions block apply; do not guess latest or clear secrets.
4. Verify installed Fleet rows/bindings and Daemon files against the exact prepared plan. Actual provider discovery, online dispatch and MCP execution need separate acceptance; a successful HTTP response alone is insufficient.
5. Before activation, finish M1/M3 for all applicable roots with no unresolved
   exceptions or remaining units and verify historical indexes. Run
   `scripts/pinned-release-readiness.sql` with a SELECT-only account; require
   `pinned_readiness_blockers=0`, then complete the runtime acceptance above.
   Keep pinned off during partial migration. Follow the executable checklist in
   `docs/pinned-installation.md`; startup only checks flag dependencies.
6. Only after acceptance, authorize an explicit deployment change. `PLUGIN_REFERENCES_ENABLED` requires version management; pinned installation requires both. Server startup rejects an inconsistent combination.

## Coverage and compatibility

Historical index preview proves exact edges and explicit empty graphs. A coverage marker is written in the same transaction as verified facts. Unknown legacy pins remain exceptions; a migrated container's new version does not prove the old version's dependencies. Dependents queries return an index-incomplete conflict rather than claiming no consumers when authorized history lacks verified coverage. This diagnostic limitation does not decide deletion permissions.

New container uploads use reference format. An old-format archive is rejected when reference writes are enabled. Existing legacy rows require the dedicated migration tool; there is no automatic import/conversion fallback. Connector IDs resolve current configuration and have no versions.

Existing pins may remain in historical/editing records after a dependency is
delisted; this does not authorize installation. Both the root and every dependency
must be published and authorized before the first installation dispatch or a
frozen-plan retry. New references also require listed, authorized resources.
Delisted unused history is retained; follow the explicit per-root conversion and
publication procedure in `docs/marketplace-v2-release.md` only when needed.

## Rollback boundaries

Stop new reference writes and new installation attempts first. Do not blindly switch all reads back to editable current rows: saved unpublished changes could become visible, and the old embedded-only installer cannot reconstruct independent references. Prefer a compatible forward fix or restore the reviewed application build while retaining the published-version read path.

Migration rollback restores recorded pointers/listing and preserves all new rows, releases and files. Do not run destructive Down migrations or delete shared dependencies. Previously installed Fleet instances keep their own IDs and content; rolling back Marketplace does not uninstall them.

Persisted uncertain installation attempts must be reconciled with Fleet under the same key and payload. After the remote receipt window, do not issue an automatic fresh key or fall back to the non-idempotent installer.
