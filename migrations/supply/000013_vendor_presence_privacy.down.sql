SET ROLE planext4u_supply_owner;
LOCK TABLE supply.privacy_presence_erasure_runs IN SHARE ROW EXCLUSIVE MODE;
DO $$
BEGIN
 IF EXISTS(SELECT 1 FROM supply.privacy_presence_erasure_runs) THEN
  RAISE EXCEPTION 'cannot roll back supply privacy presence after erasure evidence was recorded';
 END IF;
END $$;
RESET ROLE;
SET ROLE planext4u_supply_owner;
CREATE OR REPLACE FUNCTION supply.privacy_scrub_onboarding(r text)
RETURNS TABLE(outcome text,evidence_reference text,application_rows integer,document_rows integer,submission_rows integer)
LANGUAGE plpgsql VOLATILE SECURITY DEFINER SET search_path=pg_catalog,supply AS $$
DECLARE tenant text; subject text; policy text; governing_country text; prior supply.privacy_erasure_runs%ROWTYPE;
DECLARE applications uuid[] := ARRAY[]::uuid[]; countries text[] := ARRAY[]::text[];
DECLARE app_count integer := 0; doc_count integer := 0; sub_count integer := 0; evidence text;
BEGIN
 SELECT scope.tenant_id,scope.subject_id,scope.policy_version,scope.country INTO tenant,subject,policy,governing_country
 FROM identity.privacy_supply_erasure_country_scope(r) scope;
 IF tenant IS NULL OR subject IS NULL OR policy IS NULL OR governing_country IS NULL THEN RAISE EXCEPTION 'supply erasure scope unavailable'; END IF;
 evidence := 'supply-onboarding:'||r;
 SELECT * INTO prior FROM supply.privacy_erasure_runs run WHERE run.request_id=r FOR UPDATE;
 IF FOUND THEN
  IF prior.tenant_id<>tenant OR prior.subject_id<>subject OR prior.policy_version<>policy OR NOT (prior.countries <@ ARRAY[governing_country]::text[]) THEN
   RAISE EXCEPTION 'supply erasure retry scope changed';
  END IF;
  PERFORM identity.privacy_supply_record_retained(r,policy,evidence);
  RETURN QUERY SELECT 'RETAINED'::text,evidence,prior.application_rows,prior.document_rows,prior.submission_rows;
  RETURN;
 END IF;
 IF pg_catalog.pg_input_is_valid(tenant,'uuid') AND pg_catalog.pg_input_is_valid(subject,'uuid') THEN
  SELECT coalesce(array_agg(a.id),ARRAY[]::uuid[]) INTO applications
  FROM (SELECT v.id FROM supply.vendor_applications v
        WHERE v.tenant_id=tenant::uuid AND v.vendor_identity_id=subject::uuid AND v.country=governing_country::char(2)
        ORDER BY v.id LIMIT 1001 FOR UPDATE) a;
  app_count := cardinality(applications);
  IF app_count>1000 THEN RAISE EXCEPTION 'supply erasure requires a smaller batch'; END IF;
  SELECT count(*) INTO doc_count FROM supply.vendor_documents d WHERE d.application_id=ANY(applications);
  SELECT count(*) INTO sub_count FROM supply.application_submissions s WHERE s.application_id=ANY(applications);
  IF doc_count>1000 OR sub_count>1000 THEN RAISE EXCEPTION 'supply erasure requires a smaller batch'; END IF;
  SELECT coalesce(array_agg(DISTINCT v.country::text),ARRAY[]::text[]) INTO countries
  FROM supply.vendor_applications v WHERE v.id=ANY(applications);
  UPDATE supply.application_submissions s SET documents='[]'::jsonb,contact_name=NULL
  WHERE s.application_id=ANY(applications);
  DELETE FROM supply.vendor_documents d WHERE d.application_id=ANY(applications);
  UPDATE supply.vendor_applications v
   SET contact_name='',bank_reference=NULL,bank_last4=NULL,bank_status=NULL,
       bank_holder_name=NULL,bank_ifsc=NULL
   WHERE v.id=ANY(applications);
 END IF;
 INSERT INTO supply.privacy_erasure_runs(request_id,tenant_id,subject_id,policy_version,countries,
  application_rows,document_rows,submission_rows,occurred_at)
 VALUES(r,tenant,subject,policy,countries,app_count,doc_count,sub_count,clock_timestamp());
 PERFORM identity.privacy_supply_record_retained(r,policy,evidence);
 RETURN QUERY SELECT 'RETAINED'::text,evidence,app_count,doc_count,sub_count;
END $$;
RESET ROLE;

SET ROLE planext4u_supply_owner;
DROP TRIGGER vendor_submission_privacy_reintroduction ON supply.application_submissions;
DROP TRIGGER vendor_document_privacy_reintroduction ON supply.vendor_documents;
DROP FUNCTION supply.guard_privacy_vendor_evidence_reintroduction();
DROP TRIGGER vendor_application_privacy_reintroduction ON supply.vendor_applications;
DROP FUNCTION supply.guard_privacy_onboarding_reintroduction();
DROP TRIGGER catalog_privacy_reintroduction ON supply.catalog_items;
DROP TRIGGER service_zone_privacy_reintroduction ON supply.service_zones;
DROP FUNCTION supply.guard_privacy_presence_reintroduction();
DROP TRIGGER privacy_presence_truncate_immutable ON supply.privacy_presence_erasure_runs;
DROP TRIGGER privacy_presence_run_immutable ON supply.privacy_presence_erasure_runs;
DROP FUNCTION supply.guard_privacy_presence_run_immutable();
DROP FUNCTION supply.privacy_scrub_vendor_presence(text);
DROP TABLE supply.privacy_presence_erasure_runs;
RESET ROLE;
