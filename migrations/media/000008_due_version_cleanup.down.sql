BEGIN;
SET ROLE planext4u_media_owner;
DO $$
BEGIN
 IF EXISTS(SELECT 1 FROM media.object_purge_evidence) THEN
  RAISE EXCEPTION 'media version purge evidence prevents rollback' USING ERRCODE='23514';
 END IF;
END $$;
REVOKE EXECUTE ON FUNCTION media.finish_due_version_purge(text,uuid,text,text,integer,integer)
 FROM planext4u_media_cleanup;
REVOKE EXECUTE ON FUNCTION media.prepare_due_version_purge(text,uuid)
 FROM planext4u_media_cleanup;
DROP FUNCTION media.finish_due_version_purge(text,uuid,text,text,integer,integer);
DROP FUNCTION media.prepare_due_version_purge(text,uuid);
DROP TRIGGER object_purge_evidence_immutable ON media.object_purge_evidence;
DROP FUNCTION media.reject_object_purge_evidence_mutation();
DROP TABLE media.object_purge_evidence;
REVOKE EXECUTE ON FUNCTION media.identity_document_reference_allowed(uuid,char,text,uuid)
 FROM planext4u_supply_owner,planext4u_fulfillment_owner;
DROP FUNCTION media.identity_document_reference_allowed(uuid,char,text,uuid);
REVOKE USAGE ON SCHEMA media FROM planext4u_supply_owner,planext4u_fulfillment_owner;
REVOKE USAGE ON SCHEMA media FROM planext4u_media_cleanup;
RESET ROLE;
COMMIT;
