BEGIN;
SET ROLE planext4u_identity_owner;
-- Old evidence has no provable country. Preserve it for audit, but require a
-- new explicit scoped decision before authorizing location processing.
ALTER TABLE identity.consent_evidence ADD COLUMN country char(2) CHECK(country ~ '^[A-Z]{2}$');
CREATE INDEX consent_evidence_scoped_current_idx ON identity.consent_evidence(identity_id,country,purpose,version DESC);
REVOKE UPDATE,DELETE,TRUNCATE ON identity.consent_evidence FROM planext4u_identity_runtime;
CREATE FUNCTION identity.delivery_consent_allowed(t text,c char(2),s text,p text)
RETURNS boolean LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog,identity AS $$
 SELECT p IS NOT NULL AND p<>'' AND EXISTS(
  SELECT 1 FROM identity.identities i
  WHERE i.id=s AND i.tenant_id=t AND i.disabled_at IS NULL
  AND NOT EXISTS(SELECT 1 FROM identity.account_restrictions r WHERE r.identity_id=i.id AND r.source<>'ACTIVE')
  AND coalesce((SELECT e.granted AND e.policy_version=p FROM identity.consent_evidence e
   WHERE e.identity_id=i.id AND e.country=c AND e.purpose='LOCATION_DELIVERY'
   ORDER BY e.version DESC LIMIT 1),false))
$$;
REVOKE ALL ON FUNCTION identity.delivery_consent_allowed(text,char,text,text) FROM PUBLIC;
GRANT USAGE ON SCHEMA identity TO planext4u_fulfillment_runtime;
GRANT EXECUTE ON FUNCTION identity.delivery_consent_allowed(text,char,text,text) TO planext4u_fulfillment_runtime;
RESET ROLE;
COMMIT;
