SET ROLE planext4u_payment_owner;
DROP TABLE IF EXISTS payment.command_records;
ALTER TABLE payment.provider_events
    DROP CONSTRAINT IF EXISTS payment_provider_event_response_object,
    DROP COLUMN IF EXISTS response_payload;
ALTER TABLE payment.payments
    DROP COLUMN IF EXISTS operation_state,
    DROP COLUMN IF EXISTS payer;
RESET ROLE;
