BEGIN;
SET ROLE planext4u_identity_owner;
DROP TRIGGER clear_contacts_on_disable ON identity.identities;
DROP TRIGGER clear_contacts_on_withdrawal ON identity.consent_evidence;
DROP FUNCTION identity.clear_contacts_on_disable(),identity.clear_contacts_on_withdrawal(),identity.clear_contact_snapshot(text,char);
DROP TABLE identity.contact_hashes,identity.contact_snapshots;
-- Preserve historical consent evidence on rollback.
RESET ROLE;
COMMIT;
