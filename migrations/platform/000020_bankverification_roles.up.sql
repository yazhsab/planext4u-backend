DO $$
BEGIN
 IF NOT EXISTS(SELECT 1 FROM pg_roles WHERE rolname='planext4u_bankverification_owner') THEN CREATE ROLE planext4u_bankverification_owner NOLOGIN; END IF;
 IF NOT EXISTS(SELECT 1 FROM pg_roles WHERE rolname='planext4u_bankverification_runtime') THEN CREATE ROLE planext4u_bankverification_runtime NOLOGIN; END IF;
END $$;
GRANT planext4u_bankverification_owner TO CURRENT_USER;
GRANT planext4u_bankverification_runtime TO planext4u_transaction_runtime;
