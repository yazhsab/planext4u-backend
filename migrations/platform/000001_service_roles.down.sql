DO $roles$
DECLARE
    service_name text;
BEGIN
    FOREACH service_name IN ARRAY ARRAY['notification', 'messaging', 'audit', 'media', 'catalog', 'configuration', 'identity']
    LOOP
        EXECUTE format('REVOKE %I FROM CURRENT_USER', 'planext4u_' || service_name || '_runtime');
        EXECUTE format('REVOKE %I FROM CURRENT_USER', 'planext4u_' || service_name || '_owner');
        EXECUTE format('DROP ROLE IF EXISTS %I', 'planext4u_' || service_name || '_runtime');
        EXECUTE format('DROP ROLE IF EXISTS %I', 'planext4u_' || service_name || '_owner');
    END LOOP;
END
$roles$;
