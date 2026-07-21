# Metrics Durable Flush

## Goal

Make Redis-to-MySQL metrics flushing recoverable across ordinary process
failure without losing deltas or double-counting acknowledged work.

## Load-Bearing Behavior

- Dirty metric members are not removed until their counters are atomically moved
  into a Redis pending record.
- Pending records contain the drained delta and can be retried after worker
  restart.
- Database persistence is idempotent by flush ID, so a crash after DB commit but
  before Redis acknowledgement does not double-count on retry.
- Successfully acknowledged pending records are removed from Redis.
- Applied flush ID ledger rows are retained only for a bounded window and are
  cleaned by the flush worker while holding the distributed lock.

## Out Of Scope

- Exactly-once delivery when Redis itself loses acknowledged writes.
- Cross-resource metric aggregation beyond current `resource_metrics`.
- Runtime configuration surface for changing ledger retention.

## Acceptance Criteria

- A crash after counter drain but before DB write leaves a pending delta that a
  later worker can persist.
- A crash after DB commit but before Redis ack can replay without incrementing
  counts twice.
- Failed DB writes do not restore counters into the hot path; they keep pending
  state for retry.
- The idempotency ledger has an automatic retention cleanup path.
