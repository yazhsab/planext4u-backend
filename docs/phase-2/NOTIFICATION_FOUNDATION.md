# Notification foundation

`BE-P2-009` establishes a provider-neutral notification core for email, push, WhatsApp and in-app delivery.

## Guarantees

- Security messages cannot be disabled. Transactional messages honor an explicit opt-out; marketing messages require both current consent and an enabled channel preference.
- A queue command uses a tenant-scoped idempotency key. The delivery row and `planext4u.notification.delivery.requested.v1` outbox event are inserted through one repository transaction boundary.
- Only immutable, published template versions are accepted. Publishing validates locale, channel, declared variables, unresolved placeholders, body limits and email-header line breaks. Missing locales fall back to the published English version.
- Recipient addresses and device tokens are represented by opaque contact references. They are absent from outbox events and synthetic fixtures.
- Provider adapters classify failures as retryable or permanent. Retryable failures return the delivery to `QUEUED` so the messaging foundation can apply its bounded retry/DLQ policy; permanent failures are consumed as `FAILED`.
- Provider receipts are tenant-scoped and idempotent. Delivered, failed and suppressed states are terminal.

## Production adapters

The `Repository.Queue` implementation must use a single database transaction for the delivery and outbox insert. Provider webhook handlers must verify the provider signature before calling `RecordReceipt`. Provider credentials belong in the deployment secret store and must never be included in commands, events, logs or database rows.

## Evidence

`BE-NOTIFY-001` covers consent and preference suppression, essential security delivery, locale fallback, strict template rendering, command idempotency, transactional outbox data, retryable and permanent provider degradation, optimistic preference updates, immutable publishing and duplicate receipt handling.
