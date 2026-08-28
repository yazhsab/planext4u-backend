SET ROLE planext4u_payment_owner;

ALTER TABLE payment.payments
    ADD COLUMN provider_transaction_reference text,
    ADD COLUMN provider_refund_reference text,
    ADD COLUMN client_handoff jsonb;

ALTER TABLE payment.payments
    ADD CONSTRAINT payment_client_handoff_object CHECK (
        client_handoff IS NULL OR jsonb_typeof(client_handoff) = 'object'
    );

CREATE UNIQUE INDEX payment_provider_transaction_reference_idx
    ON payment.payments (method, provider_transaction_reference)
    WHERE provider_transaction_reference IS NOT NULL;

CREATE UNIQUE INDEX payment_provider_refund_reference_idx
    ON payment.payments (method, provider_refund_reference)
    WHERE provider_refund_reference IS NOT NULL;

COMMENT ON COLUMN payment.payments.client_handoff IS
    'Public, short-lived native checkout values only. Merchant secrets are prohibited.';

GRANT SELECT, INSERT, UPDATE, DELETE ON payment.payments TO planext4u_payment_runtime;
RESET ROLE;
