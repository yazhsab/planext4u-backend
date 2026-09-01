SET ROLE planext4u_ordering_owner;
DROP TABLE IF EXISTS ordering.notification_intents;
ALTER TABLE ordering.idempotency_records
    DROP CONSTRAINT IF EXISTS ordering_idempotency_response_object,
    DROP CONSTRAINT IF EXISTS ordering_idempotency_key_length;
ALTER TABLE ordering.orders
    DROP CONSTRAINT IF EXISTS ordering_state_snapshot_object,
    DROP COLUMN IF EXISTS state_snapshot;
RESET ROLE;
