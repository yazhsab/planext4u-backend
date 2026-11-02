DO $roles$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'planext4u_media_cleanup') THEN
        EXECUTE format('REVOKE %I FROM CURRENT_USER', 'planext4u_media_cleanup');
        DROP ROLE planext4u_media_cleanup;
    END IF;
END
$roles$;
