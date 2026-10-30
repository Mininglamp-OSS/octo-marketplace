# Published version baseline
Goal: owner listings expose the formal release separately from the editable draft so display and upgrades use the correct baseline.
Load-bearing: scoped owner-list SQL, public response schema, draft preservation.
Out of scope: migration/backfill, visibility policy, artifact mutation, installation.
Acceptance: draft1.0.0 plus release1.0.3 returns both labels and IDs; unpublished rows have no release; other Spaces remain inaccessible; existing current fields stay backward compatible.
