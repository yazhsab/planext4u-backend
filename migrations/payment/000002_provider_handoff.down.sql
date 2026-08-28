SET ROLE planext4u_payment_owner;
DROP INDEX IF EXISTS payment.payment_provider_transaction_reference_idx;
DROP INDEX IF EXISTS payment.payment_provider_refund_reference_idx;
ALTER TABLE payment.payments
    DROP CONSTRAINT IF EXISTS payment_client_handoff_object,
    DROP COLUMN IF EXISTS client_handoff,
    DROP COLUMN IF EXISTS provider_refund_reference,
    DROP COLUMN IF EXISTS provider_transaction_reference;
RESET ROLE;
