---
type: Task
title: "Task: Space review auto approval policy"
slug: space-review-auto-approve
---

# Space review auto approval policy

## Goal

Let each Space owner or admin choose whether organization-visible plugin
submissions are approved automatically. The effective default is enabled, so
Spaces without a stored override publish immediately while retaining an
approval audit record.

The requester reconfirmed on 2026-09-09 that automatic approval is the intended
default for both existing and new Spaces. Applying that default to existing
Spaces on deployment is part of the requested behavior. See the
[confirmed decision and rollout procedure](../../../docs/releases/space-review-auto-approval.md)
for operator responsibilities, client behavior, and explicit manual-review overrides.

## Load-bearing behavior

- The policy is owned and persisted by marketplace and scoped by authenticated
  `space_id`; request bodies never carry a Space identifier.
- A missing policy row resolves to `is_auto_approve_enabled=true` for both
  existing and new Spaces. Deployment intentionally creates no disabled-policy
  backfill and introduces no fleet-level switch that changes this default.
- Any authenticated Space member may read the effective policy; Space admins
  and owners (`space_member.role>=1`) may update the one shared Space policy.
- When enabled, publishing a Space-visible plugin still freezes a review request
  and then approves it with `decision_source=policy`; no approval card is sent.
- Review submission, list, and detail responses identify policy approvals with
  `is_auto_approved=true` and omit `decision_source` and human
  `reviewer_id`/`reviewer_name` fields. Manual decisions retain the existing
  `decision_source=web|im` enum; persisted policy decisions keep
  `decision_source=policy` and the triggering actor for internal audit. Manual
  and pending responses omit `is_auto_approved` (absence means false).
- When disabled, the existing pending-review and notification-card flow is used.
- Policy lookup failures fail closed and do not publish.
- Changing the policy does not mutate existing pending review requests.

## Out of scope

- Platform/system plugin policy.
- Retroactively deciding pending requests.
- Moving Space membership or role authority out of octo-server.

## Acceptance criteria

- GET/PATCH endpoints use authenticated Space context and standard envelopes.
- The PATCH endpoint accepts owners and admins and rejects ordinary members with
  `FORBIDDEN`.
- Default-enabled, disabled, lookup-failure, and automatic audit-source paths
  have tests.
- Handler tests cover automatic, manual web/IM, and pending review attribution
  on submission, list, and detail responses without mutating persisted records.
- OpenAPI validation and compatibility checks pass.
