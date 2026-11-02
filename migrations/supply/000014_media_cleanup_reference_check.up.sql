SET ROLE planext4u_supply_owner;
-- The media owner's exact-scope cleanup wrapper needs this read-only result.
-- The separate cleanup role itself receives no direct supply capability.
GRANT USAGE ON SCHEMA supply TO planext4u_media_owner;
GRANT EXECUTE ON FUNCTION supply.private_identity_evidence_referenced(uuid,char,text,uuid)
 TO planext4u_media_owner;

-- Historical submission snapshots also retain the private object. VOLATILE
-- takes a fresh READ COMMITTED snapshot after the cleaner obtains its asset
-- row lock, so a writer that committed while it waited cannot be missed.
CREATE OR REPLACE FUNCTION supply.private_identity_evidence_referenced(t uuid,c char(2),o text,a uuid)
RETURNS boolean LANGUAGE sql VOLATILE SECURITY DEFINER SET search_path=pg_catalog,supply AS $$
 SELECT EXISTS(SELECT 1 FROM supply.vendor_documents d
  JOIN supply.vendor_applications v ON v.id=d.application_id
  WHERE v.tenant_id=t AND v.country=c AND v.vendor_identity_id::text=o
    AND d.private_asset_id=a)
 OR EXISTS(SELECT 1 FROM supply.application_submissions s
  JOIN supply.vendor_applications v ON v.id=s.application_id
  CROSS JOIN LATERAL jsonb_array_elements(s.documents) doc
  WHERE v.tenant_id=t AND v.country=c AND v.vendor_identity_id::text=o
    AND doc->>'asset_id'=a::text)
$$;

-- These triggers are installed before the media guard is created by the
-- later media migration. PL/pgSQL resolves that exact call at execution;
-- service traffic must not start until the whole migration set is applied.
CREATE FUNCTION supply.guard_new_private_media_reference() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,supply AS $$
DECLARE app record; asset_ref text; doc jsonb;
BEGIN
 SELECT v.tenant_id,v.country,v.vendor_identity_id INTO app
 FROM supply.vendor_applications v WHERE v.id=NEW.application_id FOR SHARE;
 IF NOT FOUND THEN RAISE EXCEPTION 'vendor application scope unavailable'; END IF;
 IF TG_TABLE_NAME='vendor_documents' THEN
  asset_ref:=NEW.private_asset_id::text;
 ELSE
  FOR doc IN SELECT value FROM jsonb_array_elements(NEW.documents) LOOP
   asset_ref:=doc->>'asset_id';
   IF asset_ref IS NOT NULL AND pg_catalog.pg_input_is_valid(asset_ref,'uuid') THEN
    IF NOT media.identity_document_reference_allowed(
     app.tenant_id,app.country,app.vendor_identity_id::text,asset_ref::uuid) THEN
     RAISE EXCEPTION 'vendor private media reference is unavailable';
    END IF;
   END IF;
  END LOOP;
  RETURN NEW;
 END IF;
 IF NOT media.identity_document_reference_allowed(
  app.tenant_id,app.country,app.vendor_identity_id::text,NEW.private_asset_id) THEN
  RAISE EXCEPTION 'vendor private media reference is unavailable';
 END IF;
 RETURN NEW;
END $$;
REVOKE ALL ON FUNCTION supply.guard_new_private_media_reference() FROM PUBLIC;
CREATE TRIGGER vendor_document_media_reference_guard
 BEFORE INSERT OR UPDATE OF application_id,private_asset_id ON supply.vendor_documents
 FOR EACH ROW EXECUTE FUNCTION supply.guard_new_private_media_reference();
CREATE TRIGGER vendor_submission_media_reference_guard
 BEFORE INSERT OR UPDATE OF application_id,documents ON supply.application_submissions
 FOR EACH ROW EXECUTE FUNCTION supply.guard_new_private_media_reference();

-- Child references inherit this parent jurisdiction. A direct runtime change
-- to the parent key must not make retained assets disappear from scope checks.
CREATE FUNCTION supply.guard_private_media_owner_scope() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,supply AS $$
BEGIN
 IF NEW.tenant_id IS DISTINCT FROM OLD.tenant_id
    OR NEW.country IS DISTINCT FROM OLD.country
    OR NEW.vendor_identity_id IS DISTINCT FROM OLD.vendor_identity_id THEN
  RAISE EXCEPTION 'vendor private media owner scope is immutable';
 END IF;
 RETURN NEW;
END $$;
REVOKE ALL ON FUNCTION supply.guard_private_media_owner_scope() FROM PUBLIC;
CREATE TRIGGER vendor_private_media_owner_scope_guard
 BEFORE UPDATE OF tenant_id,country,vendor_identity_id ON supply.vendor_applications
 FOR EACH ROW EXECUTE FUNCTION supply.guard_private_media_owner_scope();
RESET ROLE;
