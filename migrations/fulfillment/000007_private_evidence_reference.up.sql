BEGIN;
SET ROLE planext4u_fulfillment_owner;
CREATE FUNCTION fulfillment.private_identity_evidence_referenced(t uuid,c char(2),o text,a uuid)
RETURNS boolean LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog,fulfillment AS $$
 SELECT EXISTS(SELECT 1 FROM fulfillment.rider_profiles r,jsonb_array_elements(r.documents) d
 WHERE r.tenant_id=t AND r.country=c AND r.rider_identity_id::text=o AND d->>'asset_id'=a::text)
$$;
REVOKE ALL ON FUNCTION fulfillment.private_identity_evidence_referenced(uuid,char,text,uuid) FROM PUBLIC;
GRANT USAGE ON SCHEMA fulfillment TO planext4u_media_runtime;
GRANT EXECUTE ON FUNCTION fulfillment.private_identity_evidence_referenced(uuid,char,text,uuid) TO planext4u_media_runtime;
RESET ROLE;
COMMIT;
