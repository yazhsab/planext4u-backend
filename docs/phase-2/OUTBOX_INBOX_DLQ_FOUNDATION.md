# Outbox, inbox and dead-letter foundation

BE-P2-011 implements reliable at-least-once event delivery using the existing
AsyncAPI `EventEnvelope` as its executable message type.

- `EnqueueAtomic` places a domain mutation and outbox insert behind one
  service-owned transaction boundary.
- Dispatchers claim due records with expiring worker leases. A crashed worker's
  record is reclaimable; bounded exponential backoff ends in `DEAD_LETTER`.
- Consumers acquire an inbox lease, reject aggregate-version gaps, retry a
  failed handler, and return duplicates without invoking the handler twice.
- Dead-letter reads and replay are restricted to the principal's tenant and
  selected country. Replay also requires explicit capability, authentication
  within five minutes, a reason and a correlation ID.
- Replay requests are written through the append-only audit service before the
  record becomes dispatchable again.

`BE-OUTBOX-001` covers transactional rollback, dispatcher crash recovery,
duplicate delivery, out-of-order delivery, consumer failure recovery, dead
lettering, tenant denial, fresh-auth denial, audited replay and successful
redispatch under the race detector.

The owning memory and PostgreSQL stores also filter dead-letter reads and
replay by the event's declared country. Unit and disposable PostgreSQL cases
cover a same-tenant request for a differently declared country. Historical
arbitrary envelopes do not yet have independently verified country metadata;
they need producer-specific classification or a fail-closed quarantine before
a production regional administrator endpoint can expose them. The caller must
also supply current operator-directory/session authority. A denied replay is
audited as a request before the store reports that no eligible event exists;
that audit does not record a successful requeue.

The JetStream publisher now reads the live stream policy at startup and before
each publish. It accepts only the owned subject prefix with file-backed limits
retention, a positive maximum age of at most seven days, and no mirror, source,
subject transform, republish or direct-read setting. A broker policy change to
unbounded retention fails readiness and blocks later publishing; a disposable
NATS test changes the live stream policy and verifies that no new event reaches
the broker until bounded retention is restored. This bounds the configured
primary stream's normal retention. It does not remove already published bytes,
prove backup removal, discover external subscribers or copies, or classify
legacy outbox envelopes by account. An external policy change between the
live policy read and the publish acknowledgment remains an operator race and
requires deployment controls. Account erasure remains incomplete.
