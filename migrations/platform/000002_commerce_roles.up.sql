DO $roles$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'planext4u_commerce_owner') THEN
        CREATE ROLE planext4u_commerce_owner NOLOGIN;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'planext4u_commerce_runtime') THEN
        CREATE ROLE planext4u_commerce_runtime NOLOGIN;
    END IF;
    GRANT planext4u_commerce_owner TO CURRENT_USER;
    GRANT planext4u_commerce_runtime TO CURRENT_USER;
END
$roles$;
