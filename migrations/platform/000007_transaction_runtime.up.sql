DO $$
BEGIN
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'planext4u_transaction_runtime') THEN
        CREATE ROLE planext4u_transaction_runtime NOLOGIN;
    END IF;
END $$;

GRANT planext4u_catalog_runtime TO planext4u_transaction_runtime;
GRANT planext4u_commerce_runtime TO planext4u_transaction_runtime;
GRANT planext4u_inventory_runtime TO planext4u_transaction_runtime;
GRANT planext4u_payment_runtime TO planext4u_transaction_runtime;
GRANT planext4u_ordering_runtime TO planext4u_transaction_runtime;
GRANT planext4u_wallet_runtime TO planext4u_transaction_runtime;
