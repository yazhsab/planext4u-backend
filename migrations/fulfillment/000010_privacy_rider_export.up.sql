SET ROLE planext4u_fulfillment_owner;
-- Explicit safe fields only. Bank, phone, review, private evidence, live
-- location and other parties' delivery data are outside this version.
CREATE FUNCTION fulfillment.privacy_export_rider_profiles(t text,s text,sid text,c char(2))
RETURNS TABLE(country char(2),revision bigint,status text,full_name text,vehicle_type text,
              vehicle_number text,zones text[],created_at timestamptz,updated_at timestamptz)
LANGUAGE plpgsql VOLATILE SECURITY DEFINER SET search_path=pg_catalog,fulfillment AS $$
BEGIN
 IF NOT identity.privacy_export_session(t,c,s,sid) THEN RETURN; END IF;
 IF NOT pg_catalog.pg_input_is_valid(t,'uuid') OR NOT pg_catalog.pg_input_is_valid(s,'uuid') THEN RETURN; END IF;
 RETURN QUERY SELECT r.country,r.revision,r.status,r.full_name,r.vehicle_type,
                     r.vehicle_number,r.zones,r.created_at,r.updated_at
 FROM fulfillment.rider_profiles r
 WHERE r.tenant_id=t::uuid AND r.rider_identity_id=s::uuid
 ORDER BY r.country,r.created_at,r.id;
END $$;
REVOKE ALL ON FUNCTION fulfillment.privacy_export_rider_profiles(text,text,text,char) FROM PUBLIC;
GRANT USAGE ON SCHEMA fulfillment TO planext4u_identity_runtime;
GRANT EXECUTE ON FUNCTION fulfillment.privacy_export_rider_profiles(text,text,text,char)
 TO planext4u_identity_runtime;
RESET ROLE;
