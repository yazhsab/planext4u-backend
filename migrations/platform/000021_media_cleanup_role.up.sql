DO $roles$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'planext4u_media_cleanup') THEN
        CREATE ROLE planext4u_media_cleanup NOLOGIN;
    END IF;
    GRANT planext4u_media_cleanup TO CURRENT_USER;
END
$roles$;
