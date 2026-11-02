BEGIN;
SET ROLE planext4u_identity_owner;
ALTER TABLE identity.consent_evidence DROP CONSTRAINT consent_evidence_purpose_allowed;
ALTER TABLE identity.consent_evidence ADD CONSTRAINT consent_evidence_purpose_allowed CHECK
 (purpose IN ('ANALYTICS','MARKETING','LOCATION_SERVICEABILITY','LOCATION_DELIVERY','CONTACT_MATCHING'));
-- Revision tombstones survive withdrawal/expiry. A delayed upload must never
-- replace a newer decision, even after a later re-grant of the same policy.
CREATE TABLE identity.contact_snapshots (
 identity_id text NOT NULL REFERENCES identity.identities(id),
 country char(2) NOT NULL CHECK(country ~ '^[A-Z]{2}$'),
 revision bigint NOT NULL CHECK(revision>0),
 key_id text, phone_hash text CHECK(phone_hash ~ '^[a-f0-9]{64}$'),
 policy_version text, request_hash text CHECK(request_hash ~ '^[a-f0-9]{64}$'),
 contact_count integer NOT NULL CHECK(contact_count BETWEEN 0 AND 10000),
 expires_at timestamptz,
 PRIMARY KEY(identity_id,country),
 CHECK((phone_hash IS NULL AND key_id IS NULL AND policy_version IS NULL AND request_hash IS NULL AND expires_at IS NULL AND contact_count=0)
 OR (phone_hash IS NOT NULL AND key_id ~ '^[A-Za-z0-9_-]{1,32}$' AND policy_version IS NOT NULL AND request_hash IS NOT NULL AND expires_at IS NOT NULL))
);
CREATE TABLE identity.contact_hashes (
 identity_id text NOT NULL, country char(2) NOT NULL,
 phone_hash text NOT NULL CHECK(phone_hash ~ '^[a-f0-9]{64}$'),
 PRIMARY KEY(identity_id,country,phone_hash),
 FOREIGN KEY(identity_id,country) REFERENCES identity.contact_snapshots(identity_id,country) ON DELETE CASCADE
);
CREATE INDEX contact_snapshots_expiry_idx ON identity.contact_snapshots(expires_at) WHERE expires_at IS NOT NULL;
CREATE INDEX contact_hashes_lookup_idx ON identity.contact_hashes(country,phone_hash,identity_id);
GRANT SELECT,INSERT,UPDATE,DELETE ON identity.contact_snapshots,identity.contact_hashes TO planext4u_identity_runtime;
CREATE FUNCTION identity.clear_contact_snapshot(s text,c char(2)) RETURNS void
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,identity AS $$
BEGIN
 DELETE FROM identity.contact_hashes WHERE identity_id=s AND (c IS NULL OR country=c);
 UPDATE identity.contact_snapshots SET revision=revision+1,key_id=NULL,phone_hash=NULL,
 policy_version=NULL,request_hash=NULL,contact_count=0,expires_at=NULL
 WHERE identity_id=s AND (c IS NULL OR country=c) AND phone_hash IS NOT NULL;
END $$;
REVOKE ALL ON FUNCTION identity.clear_contact_snapshot(text,char) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION identity.clear_contact_snapshot(text,char) TO planext4u_identity_runtime;
CREATE FUNCTION identity.clear_contacts_on_withdrawal() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,identity AS $$
BEGIN
 IF NEW.purpose='CONTACT_MATCHING' AND NOT NEW.granted THEN
  PERFORM identity.clear_contact_snapshot(NEW.identity_id,NEW.country);
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER clear_contacts_on_withdrawal AFTER INSERT ON identity.consent_evidence
 FOR EACH ROW EXECUTE FUNCTION identity.clear_contacts_on_withdrawal();
CREATE FUNCTION identity.clear_contacts_on_disable() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,identity AS $$
BEGIN
 IF NEW.disabled_at IS NOT NULL THEN PERFORM identity.clear_contact_snapshot(NEW.id,NULL); END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER clear_contacts_on_disable AFTER UPDATE OF disabled_at ON identity.identities
 FOR EACH ROW EXECUTE FUNCTION identity.clear_contacts_on_disable();
REVOKE ALL ON FUNCTION identity.clear_contacts_on_withdrawal(),identity.clear_contacts_on_disable() FROM PUBLIC;
RESET ROLE;
COMMIT;
