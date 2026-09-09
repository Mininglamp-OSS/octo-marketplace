# Space review auto-approval: decision and rollout

Applies to [PR #77](https://github.com/Mininglamp-OSS/octo-marketplace/pull/77)
and the [feature brief](../../.octospec/tasks/space-review-auto-approve/brief.md).

## Confirmed product decision

On 2026-09-09, after the review identified the effect on existing Spaces, the
requester explicitly reconfirmed: “要的就是这样 默认就是自动通过” — automatic
approval is the intended default. This includes existing Spaces and new Spaces.

Deploying this feature intentionally changes subsequent Space-visible plugin
submissions from manual review to automatic approval wherever no explicit
disabled policy exists. Later version upgrades use the same policy. The migration
creates an empty policy table; no disabled-policy backfill or fleet-level default
override is part of this release. A Space owner/admin may opt into manual review
by saving `is_auto_approve_enabled=false` for that Space.

| Effective policy | Result for a subsequent valid Space-visible submission |
| --- | --- |
| No row, existing or new Space | Automatically approved and published |
| Explicit `true` | Automatically approved and published |
| Explicit `false` | Pending manual review, with the existing notification-card flow |
| Policy lookup error | Error returned before submission or approval |

An absent row is a successfully resolved product default. A database error means
the policy could not be resolved and prevents publishing. These are separate
conditions. Authentication, Space membership, plugin ownership, content validation,
and version checks continue to apply; automatic approval does not grant permission
to publish another user's plugin or into another Space.

Existing published versions and existing pending review requests are not changed
by deploying or toggling the policy. Policy selection happens when a submission
starts; a submission already in progress may finish using the value it read.

## Responsibilities and deployment steps

This document records the product decision and the procedure to execute. It does
not record a completed production deployment or a shipped client settings page.

| Responsible role | Concrete steps |
| --- | --- |
| Marketplace release operator executing the deployment | Include the default change for existing Spaces in the release notice; deploy the migration and service; perform the checks below and record the release version, target environment, and results in the release record. |
| Space owner/admin | Use the authenticated policy API for any explicit manual-review override and verify the effective value with GET. |
| Client maintainer | Apply the response-handling steps below when consuming publish/review APIs; expose the existing policy API if adding a settings control. |

The release operator:

1. Deploys the policy-table migration with the service release. Standard startup
   runs embedded migrations; environments managing migrations externally apply
   `20260903-00-plugin-review-policies.sql` before routing traffic to the new code.
2. Checks `/readyz`, then reads the effective policy in an authenticated test
   Space without a stored override. Expect `is_auto_approve_enabled=true`.
3. Publishes an owned test plugin with Space visibility and a valid version.
   Expect immediate publication and an approved audit request with
   `is_auto_approved=true`, no `reviewer_id`/`reviewer_name`, and legacy
   `decision_source=web`. Read the request via the submit response or the review
   list/detail API; the publish response does not contain a `review_id` for an
   automatic approval.
4. In the test Space, saves `false`, verifies it with GET, and submits a different
   valid test plugin/version. Expect a pending request and the normal review-card
   flow. Saves `true` again and verifies that the pending request stays pending;
   it still needs its normal explicit review/cancel action.

Steps 2–4 use test content and an authenticated account with the required plugin
ownership and Space role. Production Spaces without overrides adopt automatic
approval on deployment; there is no per-Space enablement or frontend-release
prerequisite for that confirmed default.

## Client steps

- Read the effective policy from GET rather than treating a missing stored row
  or a missing `updated_at` as disabled. A failed GET is an error, not a default.
- A Space-visible publish with automatic approval succeeds immediately and omits
  `review_id`; a manual-review publish returns a pending `review_id`. Presence of
  this field identifies pending review, not whether an audit record was created.
- Submit, review-list, and review-detail responses use `is_auto_approved=true`
  for automatic decisions. Show an automatic-approval label for these records.
  Absence means false. Keep support for the existing `web|im` source values and
  optional reviewer fields; `decision_source=web` alone does not imply a human
  approval.
- When providing policy settings, show the server's effective value, allow only
  owners/admins to save it, and re-read after saving. The server enforces the role
  check. The authenticated API described below is the operator path while a
  settings UI is unavailable; this PR does not claim that such a UI has shipped.

## Explicit manual-review override and recovery

Use an OCTO user session with owner/admin rights in the target Space. Direct
service paths start with `/api/v1`; the documented same-origin web gateway adds
`/market`, giving `/market/api/v1`. Every request carries the session in
`Authorization: Bearer <session token>` (or `Token`) and the target `X-Space-Id`.
Space membership is verified by the server; the body never carries routing data.

| Operation | Request | Expected result |
| --- | --- | --- |
| Read effective policy | `GET /api/v1/plugin_review_policies` | `data.is_auto_approve_enabled` is the effective boolean |
| Require manual review | `PATCH /api/v1/plugin_review_policies` with `{"is_auto_approve_enabled":false}` | `data.is_auto_approve_enabled=false`; verify with GET |
| Restore automatic approval | Same PATCH with `{"is_auto_approve_enabled":true}` | `data.is_auto_approve_enabled=true`; verify with GET |

To require manual review for multiple Spaces, their authorized owners/admins
apply and verify the explicit `false` override for each target Space. There is
no global toggle in this release. Spaces are owned by `octo-server`; Marketplace
has no local `spaces` table to backfill from. Do not drop the policy table or
delete override rows to disable automatic approval: absence restores `true`.

Changing to `false` affects subsequent submissions. It does not recall published
versions, cancel pending requests, or stop approvals already using the earlier
policy value. Any content already published requires its normal authorized
delisting/review handling separately. Re-enable with `true` once the Space wants
automatic approval again. This operational override keeps the service release
and audit records in place and requires no schema rollback.
