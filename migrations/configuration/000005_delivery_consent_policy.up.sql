BEGIN;
SET ROLE planext4u_configuration_owner;
-- Only the published policy version is exposed. Drafts and the remainder of
-- the configuration document stay private to the configuration owner.
CREATE FUNCTION configuration.delivery_consent_policy(t uuid,c char(2))
RETURNS text LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog,configuration AS $$
 SELECT CASE WHEN count(*)=1 THEN min(p->>'policy_version') END
 FROM configuration.snapshots s,
 jsonb_array_elements(coalesce(s.document->'consent_policies','[]'::jsonb)) p
 WHERE s.tenant_id=t AND s.country=c AND p->>'purpose'='LOCATION_DELIVERY'
 AND p->>'policy_version' ~ '^[A-Za-z0-9][A-Za-z0-9._:-]{0,63}$'
$$;
REVOKE ALL ON FUNCTION configuration.delivery_consent_policy(uuid,char) FROM PUBLIC;
GRANT USAGE ON SCHEMA configuration TO planext4u_fulfillment_runtime;
GRANT EXECUTE ON FUNCTION configuration.delivery_consent_policy(uuid,char) TO planext4u_fulfillment_runtime;
RESET ROLE;
COMMIT;
