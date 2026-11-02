BEGIN;
SET ROLE planext4u_media_owner;
CREATE TABLE media.identity_evidence_claims (
 tenant_id uuid NOT NULL,country char(2) NOT NULL,asset_id uuid NOT NULL REFERENCES media.assets(id),
 consumer text NOT NULL CHECK(consumer IN ('VENDOR_APPLICATION','RIDER_APPLICATION')),
 resource_id text NOT NULL CHECK(length(resource_id) BETWEEN 1 AND 128),
 owner_id text NOT NULL,claimed_at timestamptz NOT NULL DEFAULT statement_timestamp(),
 PRIMARY KEY(asset_id,consumer,resource_id)
);
GRANT SELECT ON media.identity_evidence_claims TO planext4u_media_runtime;
-- Claims live as long as their owning application/history. Erasure must run
-- through the domain's configured retention/legal-hold process, not a client
-- asset-delete command. Runtime identities cannot rewrite claim evidence.
CREATE FUNCTION media.claim_identity_evidence(t uuid,c char(2),o text,k text,r text,ids uuid[])
RETURNS boolean LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,media AS $$
DECLARE a media.assets%ROWTYPE; n integer:=0;
BEGIN
 IF t IS NULL OR c IS NULL OR o IS NULL OR k IS NULL OR r IS NULL OR ids IS NULL
 OR k NOT IN ('VENDOR_APPLICATION','RIDER_APPLICATION') OR length(r) NOT BETWEEN 1 AND 128
 OR cardinality(ids) NOT BETWEEN 1 AND 10 OR cardinality(ids)<>(SELECT count(DISTINCT id) FROM unnest(ids) id) THEN RETURN false; END IF;
 -- Lock every asset before inserting any claim; sorting prevents lock cycles.
 FOR a IN SELECT * FROM media.assets WHERE id=ANY(ids) ORDER BY id FOR UPDATE LOOP
  IF a.tenant_id<>t OR a.country<>c OR a.owner_id<>o OR a.purpose<>'IDENTITY_DOCUMENT' OR a.lifecycle_state<>'READY' THEN RETURN false; END IF;
  n:=n+1;
 END LOOP;
 IF n<>cardinality(ids) THEN RETURN false; END IF;
 INSERT INTO media.identity_evidence_claims(tenant_id,country,asset_id,consumer,resource_id,owner_id)
 SELECT t,c,id,k,r,o FROM unnest(ids) id ON CONFLICT DO NOTHING;
 RETURN true;
END $$;
REVOKE ALL ON FUNCTION media.claim_identity_evidence(uuid,char,text,text,text,uuid[]) FROM PUBLIC;
CREATE FUNCTION media.claim_vendor_identity_evidence(t uuid,c char(2),o text,r text,ids uuid[])
RETURNS boolean LANGUAGE sql SECURITY DEFINER SET search_path=pg_catalog,media AS $$
 SELECT media.claim_identity_evidence(t,c,o,'VENDOR_APPLICATION',r,ids)
$$;
CREATE FUNCTION media.claim_rider_identity_evidence(t uuid,c char(2),o text,r text,ids uuid[])
RETURNS boolean LANGUAGE sql SECURITY DEFINER SET search_path=pg_catalog,media AS $$
 SELECT media.claim_identity_evidence(t,c,o,'RIDER_APPLICATION',r,ids)
$$;
REVOKE ALL ON FUNCTION media.claim_vendor_identity_evidence(uuid,char,text,text,uuid[]) FROM PUBLIC;
REVOKE ALL ON FUNCTION media.claim_rider_identity_evidence(uuid,char,text,text,uuid[]) FROM PUBLIC;
GRANT USAGE ON SCHEMA media TO planext4u_supply_runtime,planext4u_fulfillment_runtime;
GRANT EXECUTE ON FUNCTION media.claim_vendor_identity_evidence(uuid,char,text,text,uuid[]) TO planext4u_supply_runtime;
GRANT EXECUTE ON FUNCTION media.claim_rider_identity_evidence(uuid,char,text,text,uuid[]) TO planext4u_fulfillment_runtime;
CREATE FUNCTION media.protect_claimed_identity_evidence() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,media AS $$
BEGIN
 IF TG_OP='DELETE' OR NEW.lifecycle_state IN ('DELETED','EXPIRED') THEN
  IF EXISTS(SELECT 1 FROM media.identity_evidence_claims WHERE asset_id=OLD.id) THEN
   RAISE EXCEPTION 'identity evidence retained by owning application' USING ERRCODE='23514';
  END IF;
 END IF;
 IF TG_OP='DELETE' THEN RETURN OLD; END IF;
 RETURN NEW;
END $$;
REVOKE ALL ON FUNCTION media.protect_claimed_identity_evidence() FROM PUBLIC;
CREATE TRIGGER protect_claimed_identity_evidence BEFORE DELETE OR UPDATE ON media.assets
FOR EACH ROW EXECUTE FUNCTION media.protect_claimed_identity_evidence();
RESET ROLE;
COMMIT;
