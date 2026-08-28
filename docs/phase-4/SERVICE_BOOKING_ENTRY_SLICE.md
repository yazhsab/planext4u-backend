# Phase 4 service-booking entry slice

## Implemented baseline

The first Phase 4 vertical slice is implemented across the gateway, booking
bounded context, shared Phase 3 payment/wallet services, contract fixtures and
service-owned migrations.

- Geo and category-filtered service discovery returns verified-provider trust,
  rating, completed-booking, live-engagement and payment-policy information.
- Slots carry an absolute instant, IANA timezone, provider revision, buffers,
  capacity and remaining capacity. Atomic holds have a bounded TTL.
- Booking creation consumes a valid owned hold and creates an authoritative
  payment. Wallet advance payment debits the immutable FIFO ledger; provider
  methods remain pending until backend verification confirms capture.
- Every command is idempotent. Booking mutations additionally require the last
  server ETag revision.
- One free reschedule is enforced with a mandatory reason and cutoff. The old
  slot is released only after the replacement hold is consumed.
- Cancellation records a lifecycle trail, cancels or refunds the payment,
  reverses wallet lots with their original expiry and releases capacity.
- Provider actions are ownership-scoped. Start OTP comparison is constant-time,
  the persisted schema stores only a digest, and completion requires a private
  media reference before customer confirmation.
- Customer/provider no-show claims and disputes retain actor and reason evidence.

## Current acceptance evidence

- `BE-P4-001`: OpenAPI 3.1 compatibility baseline and synthetic booking fixture.
- `BE-P4-002`: 24 concurrent last-slot attempts produce exactly one winner;
  hold expiry releases capacity.
- `BE-P4-003`: shared wallet booking, stale revision rejection, one-free-
  reschedule, payment-window expiry, cancellation and wallet reversal tests.
- `BE-P4-004`: provider ownership, invalid/correct OTP, evidence sequencing,
  customer confirmation, no-show and dispute tests.
- Gateway journey: authenticated discovery -> slot hold -> wallet advance ->
  booking -> cancellation -> payment/refund and wallet reconciliation.

This is the Phase 4 entry slice, not the Phase 4 exit. Vendor KYC, food,
fulfilment/rider, settlement, franchise/field operations and their four paired
mobile acceptance journeys remain active backlog items.
