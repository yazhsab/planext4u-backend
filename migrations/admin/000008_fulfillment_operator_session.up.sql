BEGIN;
SET ROLE planext4u_admin_owner;
CREATE FUNCTION admin.lock_fulfillment_operator(t uuid,c char(2),s uuid,source_session uuid,expected_revision bigint)
RETURNS boolean LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,admin AS $$
DECLARE account_row record; session_row record; checked_at timestamptz;
BEGIN
 IF t IS NULL OR c IS NULL OR s IS NULL OR expected_revision IS NULL OR expected_revision < 1 OR source_session IS NULL THEN RETURN false; END IF;
 -- Directory changes take account then session locks in the same order.
 -- A successful caller holds both shared locks until its owning commit.
 SELECT active,revision,allowed_countries,roles INTO account_row FROM admin.operator_accounts WHERE tenant_id=t AND subject_id=s FOR SHARE;
 IF NOT FOUND OR NOT account_row.active OR account_row.revision<>expected_revision
    OR NOT(c::text=ANY(account_row.allowed_countries))
    OR NOT(account_row.roles && ARRAY['SUPER_ADMIN','COUNTRY_ADMIN','REGIONAL_STAFF']::text[]) THEN RETURN false; END IF;
 SELECT tenant_id,subject_id,selected_country,operator_revision,expires_at,authenticated_at,roles,auth_methods INTO session_row FROM admin.sessions WHERE session_id=source_session FOR SHARE;
 checked_at:=clock_timestamp();
 IF NOT FOUND OR session_row.tenant_id<>t OR session_row.subject_id<>s OR session_row.selected_country<>c
    OR session_row.operator_revision<>expected_revision OR session_row.expires_at<=checked_at
    OR session_row.authenticated_at<checked_at-interval '5 minutes' OR session_row.authenticated_at>checked_at+interval '1 minute'
    OR NOT(session_row.roles <@ account_row.roles)
    OR NOT(session_row.roles && ARRAY['SUPER_ADMIN','COUNTRY_ADMIN','REGIONAL_STAFF']::text[])
    OR NOT EXISTS(SELECT 1 FROM unnest(session_row.auth_methods) AS method(value) WHERE lower(method.value) IN ('mfa','otp','totp','webauthn')) THEN RETURN false; END IF;
 RETURN true;
END $$;
REVOKE ALL ON FUNCTION admin.lock_fulfillment_operator(uuid,char,uuid,uuid,bigint) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION admin.lock_fulfillment_operator(uuid,char,uuid,uuid,bigint) TO planext4u_fulfillment_runtime;
RESET ROLE;
COMMIT;
