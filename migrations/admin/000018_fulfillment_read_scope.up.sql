BEGIN;
SET ROLE planext4u_admin_owner;

-- Owning regional reads need the current source session as well as the live
-- territory assignment. Hold both rows through the read transaction so a
-- concurrent suspension or logout cannot race an acknowledged response.
CREATE FUNCTION admin.fulfillment_read_scope(t uuid,c char(2),s uuid,source_session uuid,expected_revision bigint)
RETURNS text LANGUAGE plpgsql VOLATILE SECURITY DEFINER SET search_path=pg_catalog,admin AS $$
DECLARE account_row record; session_row record; checked_at timestamptz;
BEGIN
 IF t IS NULL OR c IS NULL OR s IS NULL OR source_session IS NULL
    OR expected_revision IS NULL OR expected_revision < 1 THEN RETURN NULL; END IF;
 SELECT active,revision,allowed_countries,roles INTO account_row
 FROM admin.operator_accounts WHERE tenant_id=t AND subject_id=s FOR SHARE;
 IF NOT FOUND OR NOT account_row.active OR account_row.revision<>expected_revision
    OR NOT(c::text=ANY(account_row.allowed_countries))
    OR NOT(account_row.roles && ARRAY['SUPER_ADMIN','COUNTRY_ADMIN','REGIONAL_STAFF']::text[])
    THEN RETURN NULL; END IF;
 SELECT tenant_id,subject_id,selected_country,operator_revision,expires_at,
        authenticated_at,roles,auth_methods INTO session_row
 FROM admin.sessions WHERE session_id=source_session FOR SHARE;
 checked_at:=clock_timestamp();
 IF NOT FOUND OR session_row.tenant_id<>t OR session_row.subject_id<>s
    OR session_row.selected_country<>c OR session_row.operator_revision<>expected_revision
    OR session_row.expires_at<=checked_at OR session_row.authenticated_at>checked_at+interval '1 minute'
    OR NOT(session_row.roles <@ account_row.roles)
    OR NOT(session_row.roles && ARRAY['SUPER_ADMIN','COUNTRY_ADMIN','REGIONAL_STAFF']::text[])
    OR NOT EXISTS(SELECT 1 FROM unnest(session_row.auth_methods) AS method(value)
                  WHERE lower(method.value) IN ('mfa','otp','totp','webauthn'))
    THEN RETURN NULL; END IF;
 IF session_row.roles && ARRAY['SUPER_ADMIN','COUNTRY_ADMIN']::text[] THEN RETURN 'COUNTRY'; END IF;
 RETURN 'SCOPED';
END $$;
REVOKE ALL ON FUNCTION admin.fulfillment_read_scope(uuid,char,uuid,uuid,bigint) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION admin.fulfillment_read_scope(uuid,char,uuid,uuid,bigint) TO planext4u_fulfillment_runtime;
RESET ROLE;
COMMIT;
