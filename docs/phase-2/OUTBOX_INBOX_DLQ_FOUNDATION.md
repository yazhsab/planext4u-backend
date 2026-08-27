# Outbox, inbox and dead-letter foundation

BE-P2-011 implements reliable at-least-once event delivery using the existing
AsyncAPI `EventEnvelope` as its executable message type.

- `EnqueueAtomic` places a domain mutation and outbox insert behind one
  service-owned transaction boundary.
- Dispatchers claim due records with expiring worker leases. A crashed worker's
  record is reclaimable; bounded exponential backoff ends in `DEAD_LETTER`.
- Consumers acquire an inbox lease, reject aggregate-version gaps, retry a
  failed handler, and return duplicates without invoking the handler twice.
- Dead-letter reads are tenant scoped. Replay requires explicit capability,
  authentication within five minutes, a reason and a correlation ID.
- Replay requests are written through the append-only audit service before the
  record becomes dispatchable again.

`BE-OUTBOX-001` covers transactional rollback, dispatcher crash recovery,
duplicate delivery, out-of-order delivery, consumer failure recovery, dead
lettering, tenant denial, fresh-auth denial, audited replay and successful
redispatch under the race detector.
