# Card terminal receipts and safe installation messages

## Goal
Persist terminal card outcomes for safe replay and prevent arbitrary Fleet error text exposure.

## Load-bearing behavior
- Every accepted handled callback persists one immutable event outcome before 200.
- Applied approval/rejection and receipt share a transaction; unique event key
  arbitrates refusal/apply races. Storage/role faults stay retryable.
- Preserve old receipts and wire fields. New private actor binding rejects
  event reuse with changed signed inputs; missing-review receipts need no FK.
- Both installation adapters discard Fleet text and select fixed Chinese
  messages using whitelisted status/code/resource only. Preserve 409/429,
  retry deadline and earlier uncertain outcomes.

## Out of scope
Fleet changes, new review workflow, lifecycle/private rule changes, unrelated
refactoring, new dependencies, production migration or notification.

## Acceptance criteria
1. Signed HTTP + MySQL regressions prove refusal replay after role/state/row
   changes, concurrent winner, atomic decision rollback, and transient retry.
2. Both installation adapters/content modes expose safe fixed messages only;
   response and logs contain no synthetic upstream secret.
3. Existing and missing-parent receipts survive migration/repeated Up; Down
   refuses without deleting evidence. API and release docs explain behavior.
4. Full Go/MySQL race suite, build/vet and OpenAPI checks pass before one push.
