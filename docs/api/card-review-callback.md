# Review card callback idempotency

`POST /v1/card-actions/decide` retains its existing signature verification and
four-field response (`disposition`, `state`, `requester_uid`, `display`).

After verification, every handled HTTP 200 result is immutable for its event ID:
`applied`, `forbidden`, `not_found`, and `conflict`, including stale-version and
occupied-label refusals. Redelivery returns the saved result even if the
operator later gains a role, the review state changes, or a missing row appears.
A deliberate fresh action requires a new event ID. A signed attempt to reuse a
new receipt's event ID with another operator, decision, or review is rejected.
Existing receipts without the private operator binding remain replayable and
never execute a new decision. The private binding is never returned on the wire.

Successful approval/rejection and its receipt commit in one transaction. The
existing event-ID unique key arbitrates concurrent success/refusal attempts;
a losing attempt replays the durable winner without changing the review.
Receipt-write failures, database faults/deadlocks and role-lookup failures are
retryable errors (HTTP 503), not saved refusals. Invalid signatures remain HTTP
401 and produce no receipt. The response disposition is replayed unchanged.

The terminal-receipt migration removes only `fk_receipt_review`; no live review
parent is needed to store `not_found`. Event bindings, indexes and old receipts
are retained. See [release operations](../marketplace-v2-release.md) for deployment
order and forward repair. There is no new endpoint or response schema.
