BEGIN;
SET ROLE planext4u_media_owner;

DO $$
BEGIN
 IF EXISTS(SELECT 1 FROM media.object_purge_evidence e
           JOIN media.assets a ON a.id=e.asset_id WHERE a.purpose='AVATAR') THEN
  RAISE EXCEPTION 'avatar version purge evidence prevents rollback' USING ERRCODE='23514';
 END IF;
END $$;

CREATE OR REPLACE FUNCTION media.prepare_due_version_purge(r text,a uuid)
RETURNS TABLE(object_key text,policy_version text,already_purged boolean)
LANGUAGE plpgsql VOLATILE SECURITY DEFINER SET search_path=pg_catalog,media AS $$
DECLARE scope record; state media.object_purge_state%ROWTYPE;
asset media.assets%ROWTYPE; evidence media.object_purge_evidence%ROWTYPE;
BEGIN
 IF r IS NULL OR length(r) NOT BETWEEN 1 AND 128 OR a IS NULL THEN
  RAISE EXCEPTION 'media version purge scope is invalid' USING ERRCODE='23514';
 END IF;
 SELECT * INTO scope FROM identity.privacy_media_erasure_country_scope(r);
 IF scope.tenant_id IS NULL OR scope.subject_id IS NULL OR scope.policy_version IS NULL
    OR scope.country IS NULL OR NOT pg_catalog.pg_input_is_valid(scope.tenant_id,'uuid') THEN
  RAISE EXCEPTION 'media due owner scope unavailable' USING ERRCODE='23514';
 END IF;
 SELECT * INTO state FROM media.object_purge_state s WHERE s.asset_id=a FOR UPDATE;
 IF NOT FOUND THEN RAISE EXCEPTION 'media purge state unavailable' USING ERRCODE='23514'; END IF;
 SELECT * INTO asset FROM media.assets m WHERE m.id=a FOR UPDATE;
 IF NOT FOUND OR asset.tenant_id<>scope.tenant_id::uuid OR asset.country<>scope.country::char(2)
    OR asset.owner_id<>scope.subject_id OR asset.lifecycle_state<>'DELETED'
    OR asset.purpose<>'IDENTITY_DOCUMENT'
    OR asset.object_key<>('tenants/'||scope.tenant_id||'/owners/'||scope.subject_id||'/media/'||a::text) THEN
  RAISE EXCEPTION 'media version purge asset scope or purpose denied' USING ERRCODE='23514';
 END IF;
 IF EXISTS(SELECT 1 FROM media.identity_evidence_claims c WHERE c.asset_id=a)
    OR supply.private_identity_evidence_referenced(asset.tenant_id,asset.country,asset.owner_id,a)
    OR fulfillment.private_identity_evidence_referenced(asset.tenant_id,asset.country,asset.owner_id,a) THEN
  RAISE EXCEPTION 'media evidence is retained by an application' USING ERRCODE='23514';
 END IF;
 IF NOT EXISTS(SELECT 1 FROM media.upload_sessions s WHERE s.asset_id=a)
    OR EXISTS(SELECT 1 FROM media.upload_sessions s WHERE s.asset_id=a
      AND (s.grant_provenance<>'AWS_SIGV4_IF_NONE_MATCH' OR s.grant_valid_until IS NULL
           OR s.grant_valid_until>clock_timestamp()-interval '5 minutes')) THEN
  RAISE EXCEPTION 'media upload grant is not closed for version purge' USING ERRCODE='23514';
 END IF;
 IF state.purged_at IS NOT NULL THEN
  SELECT * INTO evidence FROM media.object_purge_evidence e WHERE e.asset_id=a;
  IF NOT FOUND OR evidence.request_id<>r OR evidence.policy_version<>scope.policy_version
     OR evidence.recorded_at<>state.purged_at THEN
   RAISE EXCEPTION 'media completed purge evidence is unavailable or changed' USING ERRCODE='23514';
  END IF;
 END IF;
 RETURN QUERY SELECT asset.object_key,scope.policy_version,state.purged_at IS NOT NULL;
END $$;

COMMENT ON FUNCTION media.prepare_due_version_purge(text,uuid) IS NULL;

RESET ROLE;
COMMIT;
