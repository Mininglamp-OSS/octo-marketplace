# Marketplace lifecycle and installation compatibility

## Goal
Narrow PR #142 to the user's three requirements: delete previously published
Skills/Experts only when unreferenced, preserve private reference isolation, and
keep both installation URLs usable with equivalent content for existing clients.
Delisted legacy data remains historical until a specific republish/install need.

## Load-bearing behavior
- Deletion checks live incoming relations, current published references and Team
  member-version indirect references under the existing mutation transaction.
  With the independent-reference flag off, read immutable release snapshots.
- The existing owner/Space/audience rule applies to both reference types,
  including legacy writes/publication and installation expansion before cache reuse.
- A31 preserves its body, opaque workspace/runtime IDs and typed result IDs.
  A32 keeps its required headers and runtime UUID validation.
- Both adapters use the same capability executor. Existing feature flags select
  a common legacy-content builder or released-pin builder, never a URL-specific
  executor. No fallback to create/bind/compensate provisioning exists.
- The versioned installation path freezes the first plan and protects same-key
  retry, current authorization, immutable bytes and secret-free persistence.
  Both content modes classify timeout/5xx/empty outcomes as unknown; keyless old
  clients must inspect their workspace rather than retry an unavailable key.

## Out of scope
Fleet changes; bulk migration/run-ledger platform; public distribution endpoint;
Admin import receipts/projection changes; editing-revision CAS; new review system;
Connector effective-revision storage; automatic conversion or re-listing.

## Acceptance criteria
1. Unreferenced published/delisted Skill/Expert deletion preserves release history;
   current/live/Team-indirect references prevent deletion, with tenant isolation.
2. Private Expert→Skill and Team→Expert are legal only for the same owner/Space;
   broader audiences and Admin writes do not bypass the existing rule.
3. Both adapters generate byte-equivalent Fleet requests for Expert/Team under
   each supported content mode; an unconfigured capability service fails closed.
4. Old clients need no new fields or headers; supplied keys can replay across
   adapters; no-key uncertain calls instruct users to inspect the workspace.
5. Local Go/MySQL/race/build/vet/OpenAPI checks pass before one remote update;
   an operator runbook explains deferred conversion and incident recovery.
