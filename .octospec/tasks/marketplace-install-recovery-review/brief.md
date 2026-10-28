# Marketplace installation audience and retry review

## Goal
Carry PR #142's lifecycle and installation compatibility delivery into a
replacement PR, fix confirmed installation authorization gaps, and resolve the
latest review's retry-policy questions against the existing requirements.
Refs #141 and #131. Baseline: PR #142 at `feac1b888753f3b07f42e9f47d008e6d499d9f42`.

## Load-bearing behavior
- Both installation adapters must check every independent Expert/Skill target
  against its source audience in the default-content builder, including Space
  targets retained from older data. Caller visibility does not grant a source
  permission to distribute a dependency. Check before reusing cached Skills.
- Preserve the existing embedded-child exception, private owner/Space isolation,
  current listing/status/deletion checks, and Fleet workspace authorization.
- Disabling new independent-reference writes must not skip recursive publication
  checks for already-prepared pinned drafts. Reuse the exact released-tree
  validator/locks so a Team cannot publish a historical member whose Skill has
  since become unavailable; legacy unpinned snapshots retain their existing path.
- Frozen retry uses the authenticated original released graph and content, not
  later editable relations. Normal saves, pin changes and publications must not
  rebuild or invalidate that plan merely because the draft/current release has
  different edges. Current node visibility and listing still govern dispatch.
- Root audience checks remain intentional. Under the supported audience policy,
  legal Team -> Expert -> private Skill chains require private sources with the
  same owner and Space throughout; a Space source is never a private exemption.
- Keep the request body, response, status/error enum and required headers of
  both endpoints compatible. Correct the stale OpenAPI conflict-text description.

## Out of scope
Changing PR #142's three core requirements; permitting delisted dependencies;
using live draft edges as retry authorization; automatic conversion, re-signing
or rebuilding old plans; new migrations or HMAC formats; Fleet changes;
production operations; closing or modifying PR #142.

## Acceptance criteria
1. Reproduce the default-content system/public -> Space Skill/Expert bypass
   and shared-Skill cache bypass before fixing them. Both adapters reject the
   graphs before any Fleet call after the fix.
2. Real HTTP -> Service -> MySQL tests cover managed/legacy data, both URLs,
   allowed same-Space/system targets, cross-Space refusals and safe errors.
3. Real HTTP/SQL tests preserve original bytes/key across draft relation removal,
   a later root publication and a later selected pin, for UNKNOWN and successful
   attempts. Delisting and a foreign Space block retry without changing evidence.
4. Existing private, embedded, frozen-plan integrity, expiry and callback tests
   remain green. Full Go/MySQL/race, build, vet and OpenAPI checks pass.
5. After reference writes are disabled, a retained pinned Team draft publishes
   only when its historical member's dependencies remain eligible. A removed
   Skill refuses publication without changing the draft/release/audit evidence;
   a healthy graph still publishes and protects its indirect Skill from deletion.

## COMPREHENSION
The latest review's proposed private-chain example cannot be admitted by the
existing source-audience rule, and its delisting exemption contradicts the
pinned-installation brief. Comparing a frozen plan to mutable `plugin_relations`
would also contradict immutable version semantics. Those policies are retained
and tested, rather than relaxed to satisfy the example. The confirmed defect
is the default builder's private-only audience gate: it allowed retained global
sources to distribute Space targets. This change applies the already-established
predicate to all independent targets; no new authorization rule is introduced.
An additional ordinary lifecycle reproduction confirmed that flag rollback
previously checked only the immediate Expert when publishing an existing pinned
Team draft. The recursive validator must remain active for that immutable graph
even while the flag prevents creating new pinned references.
