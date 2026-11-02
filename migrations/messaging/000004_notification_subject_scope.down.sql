SET ROLE planext4u_messaging_owner;
DROP TRIGGER outbox_privacy_scope_guard ON messaging.outbox;
DROP FUNCTION messaging.guard_outbox_privacy_scope();
DROP INDEX messaging.messaging_outbox_privacy_subject_idx;
ALTER TABLE messaging.outbox DROP COLUMN privacy_subject_id, DROP COLUMN privacy_country;
RESET ROLE;
