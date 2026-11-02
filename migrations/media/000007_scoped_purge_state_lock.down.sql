BEGIN;
SET ROLE planext4u_media_owner;

-- Do not remove the only runtime-safe lock primitive after any tombstone has
-- entered purge coordination. Fresh empty-schema rollback remains reversible.
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM media.object_purge_state) THEN
        RAISE EXCEPTION 'media object purge state prevents lock rollback' USING ERRCODE='23514';
    END IF;
END $$;

GRANT SELECT ON media.object_purge_state TO planext4u_media_runtime;
DROP FUNCTION media.lock_object_purge_state(uuid,uuid,text,text);

RESET ROLE;
COMMIT;
