SET ROLE planext4u_identity_owner;

-- The media owner receives only the due media task's immutable jurisdiction.
-- The underlying function locks the request/policy/task and rejects active holds.
CREATE FUNCTION identity.privacy_media_erasure_country_scope(r text)
RETURNS TABLE(tenant_id text,subject_id text,policy_version text,country text)
LANGUAGE sql VOLATILE SECURITY DEFINER SET search_path=pg_catalog,identity AS $$
 SELECT scope.tenant_id,scope.subject_id,scope.policy_version,d.country::text
 FROM identity.privacy_owner_erasure_scope(r,'media') scope
 JOIN identity.account_deletion_requests d ON d.id=r
 WHERE d.country IS NOT NULL
$$;
REVOKE ALL ON FUNCTION identity.privacy_media_erasure_country_scope(text) FROM PUBLIC;
GRANT USAGE ON SCHEMA identity TO planext4u_media_owner;
GRANT EXECUTE ON FUNCTION identity.privacy_media_erasure_country_scope(text)
 TO planext4u_media_owner;
RESET ROLE;
