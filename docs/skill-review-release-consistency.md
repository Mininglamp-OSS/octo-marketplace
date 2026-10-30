# Reviewed Skill publication consistency

Approving a Skill creates an immutable approval snapshot, a
`plugin_version_releases` record and `plugins.published_version_id` in the same
transaction as `listing_state=published`, the approved review and its audit row.
Both compatibility and managed review paths now use the same publication helper,
which also writes the exact Skill snapshot's complete reverse index regardless
of the read rollout flag. This keeps a subsequent reference rollout from blocking
deletion on newly published Skill snapshots with missing index evidence. Release
insertion failure rolls back the entire decision and permits a
retry. Legacy unpinned Expert/Team graphs retain their compatibility semantics;
Connector reviews keep their non-versioned behavior. Rollout flags still
control editing, history projections and independent-reference enforcement.

The released snapshot is the reviewed content, not an arbitrary latest save.
Managed approval preserves the separate editable head. `is_published` means the
version equals the current published pointer; `release != null` is the evidence
that a snapshot was formally released, including historical releases.

## Reproduction and verification

`TestSkillReviewHTTPAlwaysCreatesReleasedSnapshot` uses the production public
router and real isolated MySQL. It initializes an upload, uploads a real ZIP,
parses it, imports the Skill, publishes it and performs a manual or automatic
review. It checks the published pointer against the release table, reads history
with managed mode enabled, and creates an Expert relation pinned to that exact
released snapshot. Cross-Space history and reference writes must return 404.

Before the fix, both compatibility-mode cases fail with an approved review and
published listing but no release/published pointer. The all-enabled cases pass
on the current source. The fix does not establish why a remote deployment
reported the same inconsistency after all flags were enabled; tracing that
specific request still requires the deployed revision/replica and decision logs.

`TestSkillReviewReleaseFailureRollsBackApproval` forces release insertion to fail
in MySQL. Approval must return 500, leave the review pending and the resource a
draft, and commit no new snapshot, release or approval audit. Retrying after the
failure is removed must create a release. The card callback is covered by
`TestSkillReviewCardApprovalCreatesReleasedSnapshot` in both modes.

## Why earlier local checks missed this gap

The recorded full local integration configured all three rollout flags as true.
Its fixtures created and normally published new Skills before pinning them from
Experts. Those checks exercise the managed approval path, which already wrote
formal releases. The missing-pin/foreign-pin/draft-pin negative tests verify
reference validation, not release completeness across publication modes.

Those passing results did not prove that compatibility approvals could later be
read or referenced in managed mode, or that a release persistence failure could
never commit a successful listing. The new regression matrix checks those
database invariants and real HTTP transitions explicitly. It does not replace
checking the deployed revision, effective configuration and request path.

The nullable-legacy deletion regression previously used current compatibility
approval to generate historical rows with no formal publication pointer. That
was a fixture for deletion protection, not an assertion that publication was
complete. It now explicitly reconstructs the historical missing-release state so
that protection stays covered while new approvals create releases and indexes.

## Existing inconsistent resources

This change prevents new compatibility approvals from omitting release evidence;
it does not manufacture approval evidence for existing resources. Preserve the
resource/review/version records for diagnosis. Use the existing owner publish
operation's verified-baseline adoption only when the exact current snapshot and
files match an approved review. Evidence mismatch must stay blocked. Do not
blanket-insert releases or select a snapshot just because it has the highest
sequence number. Apply any data repair separately with the normal authorization
and audit trail.
