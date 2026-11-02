BEGIN;
SET ROLE planext4u_media_owner;

-- A dedicated NOLOGIN cleanup role may execute only these exact-scope owner
-- functions. The ordinary media runtime keeps no version-list/delete power.
GRANT USAGE ON SCHEMA media TO planext4u_media_cleanup;

CREATE TABLE media.object_purge_evidence (
 asset_id uuid PRIMARY KEY REFERENCES media.assets(id) ON DELETE RESTRICT,
 request_id text NOT NULL,
 policy_version text NOT NULL CHECK(length(policy_version) BETWEEN 1 AND 128),
 deleted_versions integer NOT NULL CHECK(deleted_versions BETWEEN 0 AND 100000),
 deleted_markers integer NOT NULL CHECK(deleted_markers BETWEEN 0 AND 100000),
 recorded_at timestamptz NOT NULL
);
REVOKE ALL ON media.object_purge_evidence FROM PUBLIC,planext4u_media_runtime,planext4u_media_cleanup;

CREATE FUNCTION media.reject_object_purge_evidence_mutation() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,media AS $$
BEGIN
 RAISE EXCEPTION 'media version purge evidence is immutable' USING ERRCODE='23514';
END $$;
REVOKE ALL ON FUNCTION media.reject_object_purge_evidence_mutation() FROM PUBLIC;
CREATE TRIGGER object_purge_evidence_immutable BEFORE UPDATE OR DELETE OR TRUNCATE
 ON media.object_purge_evidence FOR EACH STATEMENT
 EXECUTE FUNCTION media.reject_object_purge_evidence_mutation();

-- Owning supply/fulfillment triggers acquire this exact asset row lock before
-- introducing a document reference. A matching tombstone is denied even if
-- the reference write was queued behind the cleaner's S3 transaction. An ID
-- with no media asset stays legacy-compatible and cannot name bytes in this
-- bucket. This capability exposes neither the object key nor asset contents.
CREATE FUNCTION media.identity_document_reference_allowed(t uuid,c char(2),o text,a uuid)
RETURNS boolean LANGUAGE plpgsql VOLATILE SECURITY DEFINER SET search_path=pg_catalog,media AS $$
DECLARE asset media.assets%ROWTYPE;
BEGIN
 IF t IS NULL OR c IS NULL OR o IS NULL OR a IS NULL THEN RETURN false; END IF;
 SELECT * INTO asset FROM media.assets m WHERE m.id=a FOR SHARE;
 IF NOT FOUND THEN RETURN true; END IF;
 RETURN asset.tenant_id=t AND asset.country=c AND asset.owner_id=o
    AND asset.purpose='IDENTITY_DOCUMENT' AND asset.lifecycle_state='READY';
END $$;
REVOKE ALL ON FUNCTION media.identity_document_reference_allowed(uuid,char,text,uuid) FROM PUBLIC;
GRANT USAGE ON SCHEMA media TO planext4u_supply_owner,planext4u_fulfillment_owner;
GRANT EXECUTE ON FUNCTION media.identity_document_reference_allowed(uuid,char,text,uuid)
 TO planext4u_supply_owner,planext4u_fulfillment_owner;

-- The due request and current country policy/hold/task are locked first by
-- identity owner first. Then every physical-object path takes purge-state followed by
-- asset. Domain reference checks happen while those locks are held.
CREATE FUNCTION media.prepare_due_version_purge(r text,a uuid)
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
REVOKE ALL ON FUNCTION media.prepare_due_version_purge(text,uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION media.prepare_due_version_purge(text,uuid)
 TO planext4u_media_cleanup;

-- This function is callable only by the separate cleanup role. Its Go
-- caller invokes it after exact-key S3 zero-version verification in the same
-- database transaction. The database cannot independently attest S3 state;
-- no terminal account ERASED or NO_DATA receipt is issued here.
CREATE FUNCTION media.finish_due_version_purge(
 r text,a uuid,expected_key text,expected_policy text,versions integer,markers integer
) RETURNS boolean
LANGUAGE plpgsql VOLATILE SECURITY DEFINER SET search_path=pg_catalog,media AS $$
DECLARE prepared record; recorded_at timestamptz;
BEGIN
 SELECT * INTO prepared FROM media.prepare_due_version_purge(r,a);
 IF prepared.object_key IS DISTINCT FROM expected_key
    OR prepared.policy_version IS DISTINCT FROM expected_policy
    OR versions IS NULL OR versions NOT BETWEEN 0 AND 100000
    OR markers IS NULL OR markers NOT BETWEEN 0 AND 100000 THEN
  RAISE EXCEPTION 'media version purge proof context changed' USING ERRCODE='23514';
 END IF;
 IF prepared.already_purged THEN RETURN true; END IF;
 recorded_at:=clock_timestamp();
 INSERT INTO media.object_purge_evidence
  (asset_id,request_id,policy_version,deleted_versions,deleted_markers,recorded_at)
 VALUES(a,r,expected_policy,versions,markers,recorded_at);
 UPDATE media.object_purge_state s SET purged_at=recorded_at
 WHERE s.asset_id=a AND s.purged_at IS NULL;
 IF NOT FOUND THEN RAISE EXCEPTION 'media purge state changed' USING ERRCODE='23514'; END IF;
 RETURN true;
END $$;
REVOKE ALL ON FUNCTION media.finish_due_version_purge(text,uuid,text,text,integer,integer)
 FROM PUBLIC;
GRANT EXECUTE ON FUNCTION media.finish_due_version_purge(text,uuid,text,text,integer,integer)
 TO planext4u_media_cleanup;

COMMENT ON TABLE media.object_purge_evidence IS
 'Immutable per-asset version cleanup record. This is not a terminal account-erasure receipt or backup proof.';

RESET ROLE;
COMMIT;
