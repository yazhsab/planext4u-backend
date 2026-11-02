SET ROLE planext4u_supply_owner;
-- Explicit safe fields only. Bank, OCR, review, private evidence and field
-- visit data are outside this version's coverage.
CREATE FUNCTION supply.privacy_export_vendor_applications(t text,s text,sid text,c char(2))
RETURNS TABLE(country char(2),revision bigint,status text,business_name text,business_type text,
              contact_name text,created_at timestamptz,updated_at timestamptz)
LANGUAGE plpgsql VOLATILE SECURITY DEFINER SET search_path=pg_catalog,supply AS $$
BEGIN
 IF NOT identity.privacy_export_session(t,c,s,sid) THEN RETURN; END IF;
 -- The identity store accepts opaque text IDs. Supply uses UUIDs; a non-UUID
 -- account cannot have a supply application.
 IF NOT pg_catalog.pg_input_is_valid(t,'uuid') OR NOT pg_catalog.pg_input_is_valid(s,'uuid') THEN RETURN; END IF;
 RETURN QUERY SELECT a.country,a.revision,a.status,a.business_name,a.business_type,
                     a.contact_name,a.created_at,a.updated_at
 FROM supply.vendor_applications a
 WHERE a.tenant_id=t::uuid AND a.vendor_identity_id=s::uuid
 ORDER BY a.country,a.created_at,a.id;
END $$;
REVOKE ALL ON FUNCTION supply.privacy_export_vendor_applications(text,text,text,char) FROM PUBLIC;
GRANT USAGE ON SCHEMA supply TO planext4u_identity_runtime;
GRANT EXECUTE ON FUNCTION supply.privacy_export_vendor_applications(text,text,text,char)
 TO planext4u_identity_runtime;
RESET ROLE;
