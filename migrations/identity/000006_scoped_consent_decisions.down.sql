SET ROLE planext4u_identity_owner;
DROP FUNCTION IF EXISTS identity.delivery_consent_allowed(text,char,text,text);
DROP INDEX IF EXISTS identity.consent_evidence_scoped_current_idx;
ALTER TABLE identity.consent_evidence DROP COLUMN country;
GRANT UPDATE,DELETE ON identity.consent_evidence TO planext4u_identity_runtime;
RESET ROLE;
