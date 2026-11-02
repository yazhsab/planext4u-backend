SET ROLE planext4u_fulfillment_owner;
DROP TRIGGER rider_private_media_owner_scope_guard ON fulfillment.rider_profiles;
DROP FUNCTION fulfillment.guard_private_media_owner_scope();
DROP TRIGGER rider_submission_media_reference_guard ON fulfillment.rider_submissions;
DROP TRIGGER rider_profile_media_reference_guard ON fulfillment.rider_profiles;
DROP FUNCTION fulfillment.guard_new_private_media_reference();
CREATE OR REPLACE FUNCTION fulfillment.private_identity_evidence_referenced(t uuid,c char(2),o text,a uuid)
RETURNS boolean LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog,fulfillment AS $$
 SELECT EXISTS(SELECT 1 FROM fulfillment.rider_profiles r,jsonb_array_elements(r.documents) d
 WHERE r.tenant_id=t AND r.country=c AND r.rider_identity_id::text=o AND d->>'asset_id'=a::text)
$$;
REVOKE EXECUTE ON FUNCTION fulfillment.private_identity_evidence_referenced(uuid,char,text,uuid)
 FROM planext4u_media_owner;
REVOKE USAGE ON SCHEMA fulfillment FROM planext4u_media_owner;
RESET ROLE;
