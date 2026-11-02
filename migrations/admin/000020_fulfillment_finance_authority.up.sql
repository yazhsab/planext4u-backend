BEGIN;
SET ROLE planext4u_admin_owner;

-- Finance reads and commands must follow the current operator directory and
-- source session. The owning fulfillment transaction holds these row locks
-- until its read or command commits, so suspension/logout cannot overtake it.
CREATE FUNCTION admin.lock_fulfillment_finance_operator(
 t uuid,c char(2),s uuid,source_session uuid,expected_revision bigint,capability text,claimed_role text
) RETURNS boolean LANGUAGE plpgsql VOLATILE SECURITY DEFINER SET search_path=pg_catalog,admin AS $$
DECLARE account_row record; session_row record; checked_at timestamptz;
BEGIN
 IF t IS NULL OR c IS NULL OR s IS NULL OR source_session IS NULL
    OR expected_revision IS NULL OR expected_revision < 1
    OR capability IS NULL OR claimed_role IS NULL
    OR capability NOT IN ('READ','RECONCILE','APPROVE','EXECUTE','SEED')
    OR claimed_role NOT IN ('FINANCE','OPS_ADMIN','SUPER_ADMIN') THEN RETURN false; END IF;
 SELECT active,revision,roles,allowed_countries INTO account_row
 FROM admin.operator_accounts WHERE tenant_id=t AND subject_id=s FOR SHARE;
 IF NOT FOUND OR NOT account_row.active OR account_row.revision<>expected_revision
    OR NOT(c::text=ANY(account_row.allowed_countries)) THEN RETURN false; END IF;
 SELECT tenant_id,subject_id,selected_country,operator_revision,roles,auth_methods,
        authenticated_at,expires_at INTO session_row
 FROM admin.sessions WHERE session_id=source_session FOR SHARE;
 checked_at:=clock_timestamp();
 IF NOT FOUND OR session_row.tenant_id<>t OR session_row.subject_id<>s
    OR session_row.selected_country<>c OR session_row.operator_revision<>expected_revision
    OR session_row.expires_at<=checked_at OR session_row.authenticated_at>checked_at+interval '1 minute'
    OR NOT(session_row.roles <@ account_row.roles)
    OR NOT EXISTS(SELECT 1 FROM unnest(session_row.auth_methods) AS method(value)
                  WHERE lower(method.value) IN ('mfa','otp','totp','webauthn'))
    THEN RETURN false; END IF;
 IF claimed_role='FINANCE' AND NOT('FINANCE'=ANY(session_row.roles))
    OR claimed_role='OPS_ADMIN' AND NOT(session_row.roles && ARRAY['COUNTRY_ADMIN','SUPER_ADMIN']::text[])
    OR claimed_role='SUPER_ADMIN' AND NOT('SUPER_ADMIN'=ANY(session_row.roles)) THEN RETURN false; END IF;
 IF capability IN ('APPROVE','EXECUTE') THEN
   IF NOT('FINANCE'=ANY(session_row.roles)) THEN RETURN false; END IF;
 ELSIF capability='SEED' THEN
   IF NOT(session_row.roles && ARRAY['FINANCE','SUPER_ADMIN']::text[]) THEN RETURN false; END IF;
 ELSE
   IF NOT(session_row.roles && ARRAY['FINANCE','COUNTRY_ADMIN','SUPER_ADMIN']::text[]) THEN RETURN false; END IF;
 END IF;
 IF capability IN ('APPROVE','EXECUTE','SEED','RECONCILE')
    AND session_row.authenticated_at<checked_at-interval '5 minutes' THEN RETURN false; END IF;
 RETURN true;
END $$;
REVOKE ALL ON FUNCTION admin.lock_fulfillment_finance_operator(uuid,char,uuid,uuid,bigint,text,text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION admin.lock_fulfillment_finance_operator(uuid,char,uuid,uuid,bigint,text,text) TO planext4u_fulfillment_runtime;
RESET ROLE;
COMMIT;
