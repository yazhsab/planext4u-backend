SET ROLE planext4u_supply_owner;
DROP TRIGGER vendor_private_media_owner_scope_guard ON supply.vendor_applications;
DROP FUNCTION supply.guard_private_media_owner_scope();
DROP TRIGGER vendor_submission_media_reference_guard ON supply.application_submissions;
DROP TRIGGER vendor_document_media_reference_guard ON supply.vendor_documents;
DROP FUNCTION supply.guard_new_private_media_reference();
CREATE OR REPLACE FUNCTION supply.private_identity_evidence_referenced(t uuid,c char(2),o text,a uuid)
RETURNS boolean LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog,supply AS $$
 SELECT EXISTS(SELECT 1 FROM supply.vendor_documents d JOIN supply.vendor_applications v ON v.id=d.application_id
 WHERE v.tenant_id=t AND v.country=c AND v.vendor_identity_id::text=o AND d.private_asset_id=a)
$$;
REVOKE EXECUTE ON FUNCTION supply.private_identity_evidence_referenced(uuid,char,text,uuid)
 FROM planext4u_media_owner;
REVOKE USAGE ON SCHEMA supply FROM planext4u_media_owner;
RESET ROLE;
