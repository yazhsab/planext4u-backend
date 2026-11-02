BEGIN;
SET ROLE planext4u_media_owner;

-- The runtime needs a row lock for current-object deletion, but PostgreSQL
-- does not permit row-locking SELECT with its read-only table grant. Keep the
-- lock inside a fixed-scope owner function; never grant direct purge-state
-- UPDATE or a broad table read to the ordinary media process.
CREATE FUNCTION media.lock_object_purge_state(
    p_asset_id uuid, p_tenant_id uuid, p_country text, p_owner_id text
) RETURNS TABLE(purged_at timestamptz)
LANGUAGE sql SECURITY DEFINER SET search_path=pg_catalog,media AS $$
    SELECT state.purged_at
    FROM media.object_purge_state AS state
    JOIN media.assets AS asset ON asset.id = state.asset_id
    WHERE asset.id = p_asset_id AND asset.tenant_id = p_tenant_id
      AND asset.country = p_country AND asset.owner_id = p_owner_id
    FOR UPDATE OF state
$$;
REVOKE ALL ON FUNCTION media.lock_object_purge_state(uuid,uuid,text,text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION media.lock_object_purge_state(uuid,uuid,text,text)
    TO planext4u_media_runtime;
REVOKE SELECT ON media.object_purge_state FROM planext4u_media_runtime;

COMMENT ON FUNCTION media.lock_object_purge_state(uuid,uuid,text,text) IS
    'Exact-scope row lock held until caller transaction commit; returns no row for missing or foreign asset.';

RESET ROLE;
COMMIT;
