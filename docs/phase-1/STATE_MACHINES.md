# Critical state machines v1.0

Approval: accepted on 2026-08-26 as the server-authoritative lifecycle baseline.

All transitions are server-authorised, idempotent and audited. Clients receive allowed next actions from APIs; they do not infer them from a status string.

## Product order

```text
DRAFT
  -> PENDING_PAYMENT
  -> PLACED
  -> ACCEPTED | REJECTED
  -> PACKING
  -> READY_FOR_HANDOVER
  -> ASSIGNED
  -> PICKED_UP
  -> OUT_FOR_DELIVERY
  -> DELIVERED_PENDING_CONFIRMATION
  -> COMPLETED

Cancellation: DRAFT/PENDING_PAYMENT/PLACED/ACCEPTED/PACKING -> CANCEL_REQUESTED -> CANCELLED
Return: DELIVERED_PENDING_CONFIRMATION/COMPLETED -> RETURN_REQUESTED -> RETURN_APPROVED|RETURN_REJECTED -> RETURNED -> REFUNDED
Terminal: REJECTED, CANCELLED, COMPLETED, REFUNDED
```

Invariants: item/price/tax/vendor/address snapshots become immutable at `PLACED`; inventory reservation precedes placement; delivery requires configured POD; refund and wallet reversal reference immutable order/payment lines.

## Food order

```text
PENDING_PAYMENT -> PLACED -> RESTAURANT_ACCEPTED -> PREPARING -> READY
-> ASSIGNED -> PICKED_UP -> OUT_FOR_DELIVERY -> DELIVERED_PENDING_CONFIRMATION -> COMPLETED
```

Restaurant rejection/offline timeout enters `AUTO_CANCEL_PENDING` then `CANCELLED_REFUND_PENDING` and finally `CANCELLED_REFUNDED`. Chat closes one hour after delivery through a separate timed policy, not a client action.

## Service booking

```text
SLOT_HELD -> PENDING_PAYMENT -> REQUESTED -> ACCEPTED
-> PROVIDER_EN_ROUTE -> ARRIVED -> START_OTP_REQUIRED -> IN_PROGRESS
-> COMPLETION_EVIDENCE_REQUIRED -> COMPLETED_PENDING_CONFIRMATION -> COMPLETED
```

Branches: `RESCHEDULE_REQUESTED`, `CANCEL_REQUESTED`, `DECLINED`, `CUSTOMER_NO_SHOW`, `PROVIDER_NO_SHOW`, `DISPUTED`. A slot hold has TTL and a unique provider/time overlap constraint. Start OTP and completion evidence are one-time, time-bound and linked to the booking.

## Payment and refund

```text
CREATED -> PROVIDER_ORDER_CREATED -> AUTHORISATION_PENDING
-> AUTHORISED -> CAPTURED -> RECONCILED
```

Failure branches: `FAILED_RETRYABLE`, `FAILED_FINAL`, `EXPIRED`, `SIGNATURE_INVALID`, `RECONCILIATION_EXCEPTION`. Refund: `REFUND_REQUESTED -> REFUND_SUBMITTED -> REFUNDED|REFUND_FAILED`. Provider webhooks are signature-verified, stored once by provider event ID and processed idempotently.

## Inventory reservation

```text
REQUESTED -> RESERVED -> COMMITTED
                    \-> RELEASED
          \-> REJECTED_INSUFFICIENT
```

Reservations have explicit quantity, SKU/variant, order/idempotency key and expiry. Only inventory service owns stock arithmetic.

## Vendor KYC and field verification

```text
DRAFT -> DOCUMENTS_SUBMITTED -> UNDER_REVIEW
-> FIELD_VISIT_REQUIRED -> FIELD_VISIT_SCHEDULED -> FIELD_VISIT_COMPLETED
-> APPROVED
```

Review branches: `MORE_INFORMATION_REQUIRED`, `RESUBMITTED`, `REJECTED`, `SUSPENDED`, `EXPIRED_DOCUMENTS`. Approval requires verified bank before payout eligibility, not necessarily before catalog drafting.

## Rider assignment

```text
OFFERED -> ACCEPTED -> EN_ROUTE_PICKUP -> ARRIVED_PICKUP -> PICKED_UP
-> EN_ROUTE_DROP -> ARRIVED_DROP -> POD_REQUIRED -> COMPLETED
```

Branches: `DECLINED`, `OFFER_EXPIRED`, `CANCELLED`, `FAILED`, `REASSIGNMENT_REQUIRED`. Offer acceptance is compare-and-set. Location loss raises a timed warning before reassignment; it does not silently complete/cancel a task.

## Settlement

```text
ACCRUING -> COOLING -> CALCULATED -> REVIEW_REQUIRED
-> APPROVED -> PAYOUT_SUBMITTED -> PAID -> CLOSED
```

Branches: `DISPUTED`, `ADJUSTMENT_REQUIRED`, `PAYOUT_FAILED`, `REVERSED`. Calculation records immutable source entries and rule versions. Approval/payout requires fresh MFA and four-eyes policy where configured.

## Emergency request

```text
DRAFT -> CONSENT_CONFIRMED -> REQUESTED -> TRIAGED -> RESPONDER_OFFERED
-> RESPONDER_ASSIGNED -> EN_ROUTE -> ARRIVED -> ASSISTANCE_IN_PROGRESS -> CLOSED
```

Branches: `ESCALATED`, `NO_RESPONDER_AVAILABLE`, `CANCELLED_BY_REQUESTER`, `CLOSED_REFERRED_TO_PUBLIC_EMERGENCY_SERVICE`. The app must never imply guaranteed clinical care; legal/operational policy controls launch.

## Accepted state decisions

- Customer confirmation is requested after delivery but auto-completes after a configurable timeout. An unconfirmed prior order never creates a global purchase block; risk policy may require confirmation for selected high-risk transactions.
- POD policy is configured by country, module and value tier. The API returns required evidence; clients do not infer it.
- Commerce owns return eligibility/case state, Payment owns provider refund execution/chargebacks, and Wallet/Settlement owns ledger consequences. A durable process manager coordinates them.
- Service reschedule/no-show fees and cut-offs are versioned country/tenant policies evaluated in the booking service with explicit timezone.
- Settlement approval always requires fresh MFA; four-eyes applies to every manual payout/adjustment in Phase 2 and may later use approved value thresholds.
