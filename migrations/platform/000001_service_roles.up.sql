DO $roles$
DECLARE
    service_name text;
BEGIN
    FOREACH service_name IN ARRAY ARRAY['identity', 'configuration', 'catalog', 'media', 'audit', 'messaging', 'notification']
    LOOP
        IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'planext4u_' || service_name || '_owner') THEN
            EXECUTE format('CREATE ROLE %I NOLOGIN', 'planext4u_' || service_name || '_owner');
        END IF;
        IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'planext4u_' || service_name || '_runtime') THEN
            EXECUTE format('CREATE ROLE %I NOLOGIN', 'planext4u_' || service_name || '_runtime');
        END IF;
        EXECUTE format('GRANT %I TO CURRENT_USER', 'planext4u_' || service_name || '_owner');
        EXECUTE format('GRANT %I TO CURRENT_USER', 'planext4u_' || service_name || '_runtime');
    END LOOP;
END
$roles$;
