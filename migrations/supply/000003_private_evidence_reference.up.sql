BEGIN;
SET ROLE planext4u_supply_owner;
CREATE FUNCTION supply.private_identity_evidence_referenced(t uuid,c char(2),o text,a uuid)
RETURNS boolean LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog,supply AS $$
 SELECT EXISTS(SELECT 1 FROM supply.vendor_documents d JOIN supply.vendor_applications v ON v.id=d.application_id
 WHERE v.tenant_id=t AND v.country=c AND v.vendor_identity_id::text=o AND d.private_asset_id=a)
$$;
REVOKE ALL ON FUNCTION supply.private_identity_evidence_referenced(uuid,char,text,uuid) FROM PUBLIC;
GRANT USAGE ON SCHEMA supply TO planext4u_media_runtime;
GRANT EXECUTE ON FUNCTION supply.private_identity_evidence_referenced(uuid,char,text,uuid) TO planext4u_media_runtime;
RESET ROLE;
COMMIT;
