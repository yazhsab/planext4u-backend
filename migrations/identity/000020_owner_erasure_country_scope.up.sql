SET ROLE planext4u_identity_owner;

-- Keep the immutable request country in the same capability result as the
-- locked, due owner scope. Owner scrubs must not infer jurisdiction from the
-- subject's other profiles or from a caller-supplied country.
CREATE FUNCTION identity.privacy_supply_erasure_country_scope(r text)
RETURNS TABLE(tenant_id text,subject_id text,policy_version text,country text)
LANGUAGE sql VOLATILE SECURITY DEFINER SET search_path=pg_catalog,identity AS $$
 SELECT scope.tenant_id,scope.subject_id,scope.policy_version,d.country::text
 FROM identity.privacy_supply_erasure_scope(r) scope
 JOIN identity.account_deletion_requests d ON d.id=r
 WHERE d.country IS NOT NULL
$$;
CREATE FUNCTION identity.privacy_fulfillment_erasure_country_scope(r text)
RETURNS TABLE(tenant_id text,subject_id text,policy_version text,country text)
LANGUAGE sql VOLATILE SECURITY DEFINER SET search_path=pg_catalog,identity AS $$
 SELECT scope.tenant_id,scope.subject_id,scope.policy_version,d.country::text
 FROM identity.privacy_fulfillment_erasure_scope(r) scope
 JOIN identity.account_deletion_requests d ON d.id=r
 WHERE d.country IS NOT NULL
$$;
REVOKE ALL ON FUNCTION identity.privacy_supply_erasure_country_scope(text),
 identity.privacy_fulfillment_erasure_country_scope(text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION identity.privacy_supply_erasure_country_scope(text)
 TO planext4u_supply_owner;
GRANT EXECUTE ON FUNCTION identity.privacy_fulfillment_erasure_country_scope(text)
 TO planext4u_fulfillment_owner;
RESET ROLE;
