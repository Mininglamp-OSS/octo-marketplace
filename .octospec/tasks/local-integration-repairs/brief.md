# Local integration repairs
Goal: preserve draft notes, exact pinned reads, and change published tenant audiences independently of immutable content releases.
Load-bearing: owner/Space authorization; append-only snapshots/releases; published dependency audience guards; review state and stale approval checks; same-version visibility requests.
Out of scope: changing artifact bytes, runtime lifecycle, system/public tenant writes, remote push or PR.
Acceptance: draft notes survive save/reopen without copying old release notes; same-version audience change preserves release and editable draft identities; manual review keeps old audience until approval, reject/cancel preserves it; auto review and narrowing validate dependency graph; non-owner/cross-Space fail closed; local browser/API and real MySQL regressions pass.
