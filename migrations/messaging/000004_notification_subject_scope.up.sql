SET ROLE planext4u_messaging_owner;

-- This is lookup metadata for new notification events, not a copy of the
-- recipient or rendered message. Older events remain explicitly unclassified.
ALTER TABLE messaging.outbox
 ADD COLUMN privacy_country char(2),
 ADD COLUMN privacy_subject_id text;
CREATE INDEX messaging_outbox_privacy_subject_idx
 ON messaging.outbox(tenant_id,privacy_country,privacy_subject_id,event_id)
 WHERE privacy_subject_id IS NOT NULL;

CREATE FUNCTION messaging.guard_outbox_privacy_scope() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='UPDATE' THEN
  IF NEW.event_id IS DISTINCT FROM OLD.event_id
     OR NEW.tenant_id IS DISTINCT FROM OLD.tenant_id
     OR NEW.aggregate_type IS DISTINCT FROM OLD.aggregate_type
     OR NEW.aggregate_id IS DISTINCT FROM OLD.aggregate_id
     OR NEW.aggregate_version IS DISTINCT FROM OLD.aggregate_version
     OR NEW.envelope IS DISTINCT FROM OLD.envelope
     OR NEW.privacy_country IS DISTINCT FROM OLD.privacy_country
     OR NEW.privacy_subject_id IS DISTINCT FROM OLD.privacy_subject_id THEN
   RAISE EXCEPTION 'outbox event identity and privacy scope are immutable';
  END IF;
  RETURN NEW;
 END IF;

 IF NEW.envelope->>'producer'='notification-service'
    OR NEW.envelope->>'event_type'=('planext4u.'||'notification'||'.delivery.requested.v1')
    OR NEW.aggregate_type='notification-delivery' THEN
  IF NOT pg_has_role(current_user,'planext4u_notification_runtime','member')
     OR NEW.envelope->>'producer' IS DISTINCT FROM 'notification-service'
     OR NEW.envelope->>'event_type' IS DISTINCT FROM ('planext4u.'||'notification'||'.delivery.requested.v1')
     OR NEW.aggregate_type IS DISTINCT FROM 'notification-delivery'
     OR NEW.privacy_subject_id IS NULL
     OR NEW.privacy_subject_id !~ '^[A-Za-z0-9._:-]{1,128}$'
     OR NEW.privacy_country IS NULL
     OR NEW.privacy_country::text !~ '^[A-Z]{2}$'
     OR NEW.envelope->>'country' IS DISTINCT FROM NEW.privacy_country::text
     OR NEW.envelope->>'tenant_id' IS DISTINCT FROM NEW.tenant_id::text
     OR NEW.envelope->>'event_id' IS DISTINCT FROM NEW.event_id::text
     OR NEW.envelope->>'aggregate_type' IS DISTINCT FROM NEW.aggregate_type
     OR NEW.envelope->>'aggregate_id' IS DISTINCT FROM NEW.aggregate_id::text
     OR NEW.envelope->>'aggregate_version' IS DISTINCT FROM NEW.aggregate_version::text
     OR NEW.envelope->'data'->>'delivery_id' IS DISTINCT FROM NEW.aggregate_id::text THEN
   RAISE EXCEPTION 'notification outbox privacy scope is missing or inconsistent';
  END IF;
 ELSIF NEW.privacy_country IS NOT NULL OR NEW.privacy_subject_id IS NOT NULL THEN
  RAISE EXCEPTION 'only notification delivery events may set this privacy scope';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER outbox_privacy_scope_guard BEFORE INSERT OR UPDATE
 ON messaging.outbox FOR EACH ROW
 EXECUTE FUNCTION messaging.guard_outbox_privacy_scope();
REVOKE ALL ON FUNCTION messaging.guard_outbox_privacy_scope() FROM PUBLIC;
RESET ROLE;
