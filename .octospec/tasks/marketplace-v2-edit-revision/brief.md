# Marketplace v2 backend implementation

Goal: implement the approved Marketplace v2 B-model contracts in one combined code PR against octo-marketplace, tracked by issues #131–134 and the backend portion of #138.

Load-bearing behavior:
- Editable revisions use transactional CAS independently of immutable versions. Connector editing and effective payloads are distinct; Admin changes take effect without review.
- Admin saves of already-listed versioned assets, including tenant-owned assets, append a snapshot and advance the formal release atomically. They preserve owner, Space, visibility and placements, require an explicit unused forward label for changed published content, and never add a review/publish step. Draft/delisted saves do not re-list; Tenant-user saves retain their review policy.
- Expert→Skill and Team→Expert resolve latest formal releases; Connector references resolve complete effective rows. Graph reads share a consistent snapshot and never fall back to older releases.
- Lifecycle checks protect live, published and indirect references while allowing unreferenced previously published assets to be deleted. Review observations remain frozen across conflicts.
- A34 returns complete verified original content with a JCS digest and bounded response. Installation preserves market names/files, freezes the initial plan for retries, and never treats uncertainty as failure.
- Migration uses explicit frozen plans and offline authorization; publication and review policies are retained.

Out of scope: changing octo-web, octo-cli, octo-server, Buddy or Fleet; enabling production traffic; executing production migrations; rewinding published version pointers. Cross-repository integration is an external dependency, not a backend completion claim.

Acceptance: concurrent CAS and cross-Space negative tests, latest dependency and no-fallback tests, Connector effective/draft isolation, complete-content integrity/budget tests, lifecycle and review races, retry/UNKNOWN state-machine and migration recovery tests, full Go suite and generated OpenAPI validation. Partial implementation commits do not close the tracking issues.
