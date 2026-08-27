DO $$
BEGIN
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'planext4u_inventory_owner') THEN CREATE ROLE planext4u_inventory_owner NOLOGIN; END IF;
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'planext4u_inventory_runtime') THEN CREATE ROLE planext4u_inventory_runtime NOLOGIN; END IF;
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'planext4u_payment_owner') THEN CREATE ROLE planext4u_payment_owner NOLOGIN; END IF;
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'planext4u_payment_runtime') THEN CREATE ROLE planext4u_payment_runtime NOLOGIN; END IF;
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'planext4u_ordering_owner') THEN CREATE ROLE planext4u_ordering_owner NOLOGIN; END IF;
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'planext4u_ordering_runtime') THEN CREATE ROLE planext4u_ordering_runtime NOLOGIN; END IF;
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'planext4u_wallet_owner') THEN CREATE ROLE planext4u_wallet_owner NOLOGIN; END IF;
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'planext4u_wallet_runtime') THEN CREATE ROLE planext4u_wallet_runtime NOLOGIN; END IF;
END $$;

GRANT planext4u_inventory_owner TO CURRENT_USER;
GRANT planext4u_payment_owner TO CURRENT_USER;
GRANT planext4u_ordering_owner TO CURRENT_USER;
GRANT planext4u_wallet_owner TO CURRENT_USER;
