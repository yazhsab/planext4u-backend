SET ROLE planext4u_fulfillment_owner;
-- Preserve legacy rider-document references without exposing fulfillment rows.
GRANT USAGE ON SCHEMA fulfillment TO planext4u_media_owner;
GRANT EXECUTE ON FUNCTION fulfillment.private_identity_evidence_referenced(uuid,char,text,uuid)
 TO planext4u_media_owner;

-- Include immutable submitted snapshots as well as the current profile.
-- VOLATILE obtains a fresh READ COMMITTED view after the cleaner's asset lock.
CREATE OR REPLACE FUNCTION fulfillment.private_identity_evidence_referenced(t uuid,c char(2),o text,a uuid)
RETURNS boolean LANGUAGE sql VOLATILE SECURITY DEFINER SET search_path=pg_catalog,fulfillment AS $$
 SELECT EXISTS(SELECT 1 FROM fulfillment.rider_profiles p,
  jsonb_array_elements(p.documents) doc
  WHERE p.tenant_id=t AND p.country=c AND p.rider_identity_id::text=o
    AND doc->>'asset_id'=a::text)
 OR EXISTS(SELECT 1 FROM fulfillment.rider_submissions s
  JOIN fulfillment.rider_profiles p ON p.id=s.profile_id
  CROSS JOIN LATERAL jsonb_array_elements(s.documents) doc
  WHERE p.tenant_id=t AND p.country=c AND p.rider_identity_id::text=o
    AND doc->>'asset_id'=a::text)
$$;

-- The exact media guard is defined in the later media migration. This
-- late-bound call fails closed until that migration is complete.
CREATE FUNCTION fulfillment.guard_new_private_media_reference() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,fulfillment AS $$
DECLARE t uuid; c char(2); o text; doc jsonb; asset_ref text;
BEGIN
 IF TG_TABLE_NAME='rider_profiles' THEN
  t:=NEW.tenant_id; c:=NEW.country; o:=NEW.rider_identity_id::text;
 ELSE
  SELECT p.tenant_id,p.country,p.rider_identity_id::text INTO t,c,o
  FROM fulfillment.rider_profiles p WHERE p.id=NEW.profile_id FOR SHARE;
  IF NOT FOUND THEN RAISE EXCEPTION 'rider profile scope unavailable'; END IF;
 END IF;
 FOR doc IN SELECT value FROM jsonb_array_elements(NEW.documents) LOOP
  asset_ref:=doc->>'asset_id';
  IF asset_ref IS NOT NULL AND pg_catalog.pg_input_is_valid(asset_ref,'uuid') THEN
   IF NOT media.identity_document_reference_allowed(t,c,o,asset_ref::uuid) THEN
    RAISE EXCEPTION 'rider private media reference is unavailable';
   END IF;
  END IF;
 END LOOP;
 RETURN NEW;
END $$;
REVOKE ALL ON FUNCTION fulfillment.guard_new_private_media_reference() FROM PUBLIC;
CREATE TRIGGER rider_profile_media_reference_guard
 BEFORE INSERT OR UPDATE OF tenant_id,country,rider_identity_id,documents
 ON fulfillment.rider_profiles FOR EACH ROW
 EXECUTE FUNCTION fulfillment.guard_new_private_media_reference();
CREATE TRIGGER rider_submission_media_reference_guard
 BEFORE INSERT OR UPDATE OF profile_id,documents
 ON fulfillment.rider_submissions FOR EACH ROW
 EXECUTE FUNCTION fulfillment.guard_new_private_media_reference();

-- Submitted snapshots inherit the live profile's jurisdiction. Keep it fixed
-- so a direct runtime mutation cannot hide a retained document reference.
CREATE FUNCTION fulfillment.guard_private_media_owner_scope() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,fulfillment AS $$
BEGIN
 IF NEW.tenant_id IS DISTINCT FROM OLD.tenant_id
    OR NEW.country IS DISTINCT FROM OLD.country
    OR NEW.rider_identity_id IS DISTINCT FROM OLD.rider_identity_id THEN
  RAISE EXCEPTION 'rider private media owner scope is immutable';
 END IF;
 RETURN NEW;
END $$;
REVOKE ALL ON FUNCTION fulfillment.guard_private_media_owner_scope() FROM PUBLIC;
CREATE TRIGGER rider_private_media_owner_scope_guard
 BEFORE UPDATE OF tenant_id,country,rider_identity_id ON fulfillment.rider_profiles
 FOR EACH ROW EXECUTE FUNCTION fulfillment.guard_private_media_owner_scope();
RESET ROLE;
