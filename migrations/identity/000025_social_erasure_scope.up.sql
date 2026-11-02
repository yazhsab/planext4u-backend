SET ROLE planext4u_identity_owner;

-- The social owner may act only for its own READY task and the immutable
-- country of a due request. It receives no general identity table access.
CREATE FUNCTION identity.privacy_social_erasure_country_scope(r text)
RETURNS TABLE(tenant_id text,subject_id text,policy_version text,country text)
LANGUAGE sql VOLATILE SECURITY DEFINER SET search_path=pg_catalog,identity AS $$
 SELECT scope.tenant_id,scope.subject_id,scope.policy_version,d.country::text
 FROM identity.privacy_owner_erasure_scope(r,'social') scope
 JOIN identity.account_deletion_requests d ON d.id=r
 WHERE d.country IS NOT NULL
$$;
CREATE FUNCTION identity.privacy_social_record_retained(r text,pv text,reference text)
RETURNS void LANGUAGE sql VOLATILE SECURITY DEFINER SET search_path=pg_catalog,identity AS $$
 SELECT identity.privacy_record_retained(r,'social',pv,reference)
$$;
REVOKE ALL ON FUNCTION identity.privacy_social_erasure_country_scope(text),
 identity.privacy_social_record_retained(text,text,text) FROM PUBLIC;
GRANT USAGE ON SCHEMA identity TO planext4u_social_owner;
GRANT EXECUTE ON FUNCTION identity.privacy_social_erasure_country_scope(text),
 identity.privacy_social_record_retained(text,text,text) TO planext4u_social_owner;
RESET ROLE;
