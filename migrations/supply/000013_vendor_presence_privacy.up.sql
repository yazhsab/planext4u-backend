SET ROLE planext4u_supply_owner;

-- A separate immutable receipt lets an older onboarding scrub acquire this
-- additional scope on retry without rewriting its original audit row.
CREATE TABLE supply.privacy_presence_erasure_runs (
 request_id text PRIMARY KEY,
 tenant_id text NOT NULL,
 subject_id text NOT NULL,
 policy_version text NOT NULL,
 country char(2) NOT NULL CHECK(country ~ '^[A-Z]{2}$'),
 service_zone_rows integer NOT NULL CHECK(service_zone_rows BETWEEN 0 AND 1000),
 catalog_deactivated_rows integer NOT NULL CHECK(catalog_deactivated_rows BETWEEN 0 AND 1000),
 occurred_at timestamptz NOT NULL
);
CREATE FUNCTION supply.guard_privacy_presence_run_immutable() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,supply AS $$
BEGIN
 RAISE EXCEPTION 'supply privacy presence receipt is immutable';
END $$;
CREATE TRIGGER privacy_presence_run_immutable
 BEFORE UPDATE OR DELETE ON supply.privacy_presence_erasure_runs
 FOR EACH ROW EXECUTE FUNCTION supply.guard_privacy_presence_run_immutable();
CREATE TRIGGER privacy_presence_truncate_immutable
 BEFORE TRUNCATE ON supply.privacy_presence_erasure_runs
 FOR EACH STATEMENT EXECUTE FUNCTION supply.guard_privacy_presence_run_immutable();
REVOKE ALL ON supply.privacy_presence_erasure_runs FROM PUBLIC,planext4u_supply_runtime;
REVOKE ALL ON FUNCTION supply.guard_privacy_presence_run_immutable() FROM PUBLIC;

CREATE FUNCTION supply.guard_privacy_presence_reintroduction() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,supply AS $$
BEGIN
 IF EXISTS (
  SELECT 1 FROM supply.privacy_presence_erasure_runs run
  WHERE run.tenant_id=NEW.tenant_id::text AND run.subject_id=NEW.vendor_identity_id::text
    AND run.country=NEW.country
 ) OR (TG_OP='UPDATE' AND EXISTS (
  SELECT 1 FROM supply.privacy_presence_erasure_runs run
  WHERE run.tenant_id=OLD.tenant_id::text AND run.subject_id=OLD.vendor_identity_id::text
    AND run.country=OLD.country
 )) THEN
  RAISE EXCEPTION 'deleted vendor presence cannot be restored';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER service_zone_privacy_reintroduction
 BEFORE INSERT OR UPDATE ON supply.service_zones
 FOR EACH ROW EXECUTE FUNCTION supply.guard_privacy_presence_reintroduction();
CREATE TRIGGER catalog_privacy_reintroduction
 BEFORE INSERT OR UPDATE ON supply.catalog_items
 FOR EACH ROW EXECUTE FUNCTION supply.guard_privacy_presence_reintroduction();
REVOKE ALL ON FUNCTION supply.guard_privacy_presence_reintroduction() FROM PUBLIC;

-- Later administrative or replay writes may retain safe business history, but
-- cannot turn a scrubbed application back into an approved work source or
-- restore private contact/bank evidence.
CREATE FUNCTION supply.guard_privacy_onboarding_reintroduction() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,supply AS $$
BEGIN
 IF EXISTS (
  SELECT 1 FROM supply.privacy_presence_erasure_runs run
  WHERE run.tenant_id=NEW.tenant_id::text AND run.subject_id=NEW.vendor_identity_id::text
    AND run.country=NEW.country
 ) OR (TG_OP='UPDATE' AND EXISTS (
  SELECT 1 FROM supply.privacy_presence_erasure_runs run
  WHERE run.tenant_id=OLD.tenant_id::text AND run.subject_id=OLD.vendor_identity_id::text
    AND run.country=OLD.country
 )) THEN
  IF TG_OP='INSERT' THEN
   RAISE EXCEPTION 'deleted vendor application cannot be recreated';
  END IF;
  IF NEW.verified OR NEW.contact_name<>'' OR NEW.bank_reference IS NOT NULL
     OR NEW.bank_last4 IS NOT NULL OR NEW.bank_status IS NOT NULL
     OR NEW.bank_holder_name IS NOT NULL OR NEW.bank_ifsc IS NOT NULL
     OR (NEW.status='APPROVED' AND OLD.status<>'APPROVED') THEN
   RAISE EXCEPTION 'deleted vendor onboarding evidence cannot be restored';
  END IF;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER vendor_application_privacy_reintroduction
 BEFORE INSERT OR UPDATE ON supply.vendor_applications
 FOR EACH ROW EXECUTE FUNCTION supply.guard_privacy_onboarding_reintroduction();
REVOKE ALL ON FUNCTION supply.guard_privacy_onboarding_reintroduction() FROM PUBLIC;

CREATE FUNCTION supply.guard_privacy_vendor_evidence_reintroduction() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,supply AS $$
BEGIN
 -- The scrub locks the parent application before deleting child evidence.
 -- Acquire a conflicting parent lock first, then read the receipt in a new
 -- READ COMMITTED statement. A BEFORE trigger that checks the receipt first
 -- could otherwise wait on the FK after its stale check and insert evidence
 -- immediately after the scrub commits.
 IF TG_OP='UPDATE' THEN
  PERFORM 1 FROM supply.vendor_applications app
  WHERE app.id IN (NEW.application_id,OLD.application_id)
  ORDER BY app.id FOR SHARE;
 ELSE
  PERFORM 1 FROM supply.vendor_applications app
  WHERE app.id=NEW.application_id FOR SHARE;
 END IF;
 IF EXISTS (
  SELECT 1 FROM supply.vendor_applications app
  JOIN supply.privacy_presence_erasure_runs run
    ON run.tenant_id=app.tenant_id::text AND run.subject_id=app.vendor_identity_id::text
   AND run.country=app.country
  WHERE app.id=NEW.application_id
 ) OR (TG_OP='UPDATE' AND EXISTS (
  SELECT 1 FROM supply.vendor_applications app
  JOIN supply.privacy_presence_erasure_runs run
    ON run.tenant_id=app.tenant_id::text AND run.subject_id=app.vendor_identity_id::text
   AND run.country=app.country
  WHERE app.id=OLD.application_id
 )) THEN
  RAISE EXCEPTION 'deleted vendor private evidence cannot be restored';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER vendor_document_privacy_reintroduction
 BEFORE INSERT OR UPDATE ON supply.vendor_documents
 FOR EACH ROW EXECUTE FUNCTION supply.guard_privacy_vendor_evidence_reintroduction();
CREATE TRIGGER vendor_submission_privacy_reintroduction
 BEFORE INSERT OR UPDATE ON supply.application_submissions
 FOR EACH ROW EXECUTE FUNCTION supply.guard_privacy_vendor_evidence_reintroduction();
REVOKE ALL ON FUNCTION supply.guard_privacy_vendor_evidence_reintroduction() FROM PUBLIC;

-- Country comes only from the due identity deletion request. This operation
-- removes vendor service geography and withdraws active catalog listings.
-- It leaves order, payment, work, historical catalog and audit records intact.
CREATE FUNCTION supply.privacy_scrub_vendor_presence(r text)
RETURNS TABLE(service_zone_rows integer,catalog_deactivated_rows integer)
LANGUAGE plpgsql VOLATILE SECURITY DEFINER SET search_path=pg_catalog,supply AS $$
DECLARE tenant text; subject text; policy text; governing_country text;
DECLARE prior supply.privacy_presence_erasure_runs%ROWTYPE;
DECLARE zone_count integer; catalog_count integer; evidence text;
BEGIN
 SELECT scope.tenant_id,scope.subject_id,scope.policy_version,scope.country
 INTO tenant,subject,policy,governing_country
 FROM identity.privacy_supply_erasure_country_scope(r) scope;
 IF tenant IS NULL OR subject IS NULL OR policy IS NULL OR governing_country IS NULL
    OR NOT pg_catalog.pg_input_is_valid(tenant,'uuid')
    OR NOT pg_catalog.pg_input_is_valid(subject,'uuid') THEN
  RAISE EXCEPTION 'supply presence erasure scope unavailable';
 END IF;
 evidence := 'supply-onboarding:'||r;
 PERFORM 1 FROM supply.privacy_erasure_runs onboarding
 WHERE onboarding.request_id=r AND onboarding.tenant_id=tenant
   AND onboarding.subject_id=subject AND onboarding.policy_version=policy
   AND onboarding.countries <@ ARRAY[governing_country]::text[]
 FOR SHARE;
 IF NOT FOUND THEN RAISE EXCEPTION 'supply onboarding scrub must precede presence erasure'; END IF;
 SELECT * INTO prior FROM supply.privacy_presence_erasure_runs run WHERE run.request_id=r FOR UPDATE;
 IF FOUND THEN
  IF prior.tenant_id<>tenant OR prior.subject_id<>subject
     OR prior.policy_version<>policy OR prior.country<>governing_country THEN
   RAISE EXCEPTION 'supply presence erasure retry scope changed';
  END IF;
  PERFORM identity.privacy_supply_record_retained(r,policy,evidence);
  RETURN QUERY SELECT prior.service_zone_rows,prior.catalog_deactivated_rows;
  RETURN;
 END IF;

 -- Existing profile mutations lock the application first. Follow that order
 -- before taking table locks, which close insert/update races on tables without
 -- an application FK. The post-scrub guards reject later reintroduction.
 PERFORM 1 FROM supply.vendor_applications a
 WHERE a.tenant_id=tenant::uuid AND a.country=governing_country::char(2)
   AND a.vendor_identity_id=subject::uuid FOR UPDATE;
 LOCK TABLE supply.service_zones,supply.catalog_items IN SHARE ROW EXCLUSIVE MODE;
 SELECT count(*) INTO zone_count FROM supply.service_zones z
 WHERE z.tenant_id=tenant::uuid AND z.country=governing_country::char(2)
   AND z.vendor_identity_id=subject::uuid;
 SELECT count(*) INTO catalog_count FROM supply.catalog_items c
 WHERE c.tenant_id=tenant::uuid AND c.country=governing_country::char(2)
   AND c.vendor_identity_id=subject::uuid AND c.active;
 IF zone_count>1000 OR catalog_count>1000 THEN
  RAISE EXCEPTION 'supply presence erasure requires a smaller batch';
 END IF;
 DELETE FROM supply.service_zones z
 WHERE z.tenant_id=tenant::uuid AND z.country=governing_country::char(2)
   AND z.vendor_identity_id=subject::uuid;
 UPDATE supply.catalog_items c
 SET active=false,revision=revision+1,updated_at=clock_timestamp()
 WHERE c.tenant_id=tenant::uuid AND c.country=governing_country::char(2)
   AND c.vendor_identity_id=subject::uuid AND c.active;
 INSERT INTO supply.privacy_presence_erasure_runs
  (request_id,tenant_id,subject_id,policy_version,country,
   service_zone_rows,catalog_deactivated_rows,occurred_at)
 VALUES(r,tenant,subject,policy,governing_country,zone_count,catalog_count,clock_timestamp());
 PERFORM identity.privacy_supply_record_retained(r,policy,evidence);
 RETURN QUERY SELECT zone_count,catalog_count;
END $$;
REVOKE ALL ON FUNCTION supply.privacy_scrub_vendor_presence(text) FROM PUBLIC;

-- The existing identity-owned opt-in call now performs both partial scrubs in
-- one transaction. Existing onboarding receipts are backfilled on retry.
CREATE OR REPLACE FUNCTION supply.privacy_scrub_onboarding(r text)
RETURNS TABLE(outcome text,evidence_reference text,application_rows integer,document_rows integer,submission_rows integer)
LANGUAGE plpgsql VOLATILE SECURITY DEFINER SET search_path=pg_catalog,supply AS $$
DECLARE tenant text; subject text; policy text; governing_country text; prior supply.privacy_erasure_runs%ROWTYPE;
DECLARE applications uuid[] := ARRAY[]::uuid[]; countries text[] := ARRAY[]::text[];
DECLARE app_count integer := 0; doc_count integer := 0; sub_count integer := 0; evidence text;
BEGIN
 SELECT scope.tenant_id,scope.subject_id,scope.policy_version,scope.country INTO tenant,subject,policy,governing_country
 FROM identity.privacy_supply_erasure_country_scope(r) scope;
 IF tenant IS NULL OR subject IS NULL OR policy IS NULL OR governing_country IS NULL
    OR NOT pg_catalog.pg_input_is_valid(tenant,'uuid')
    OR NOT pg_catalog.pg_input_is_valid(subject,'uuid') THEN
  RAISE EXCEPTION 'supply erasure scope unavailable';
 END IF;
 evidence := 'supply-onboarding:'||r;
 -- There is no FK from a new vendor application to the identity account. Hold a table
 -- lock through the audit insert so a zero-row scrub cannot miss a concurrent
 -- registration, and post-scrub application triggers see the committed run.
 LOCK TABLE supply.vendor_applications IN SHARE ROW EXCLUSIVE MODE;
 SELECT * INTO prior FROM supply.privacy_erasure_runs run WHERE run.request_id=r FOR UPDATE;
 IF FOUND THEN
  IF prior.tenant_id<>tenant OR prior.subject_id<>subject OR prior.policy_version<>policy
     OR NOT (prior.countries <@ ARRAY[governing_country]::text[]) THEN
   RAISE EXCEPTION 'supply erasure retry scope changed';
  END IF;
  SELECT coalesce(array_agg(a.id),ARRAY[]::uuid[]) INTO applications
  FROM (SELECT v.id FROM supply.vendor_applications v
        WHERE v.tenant_id=tenant::uuid AND v.vendor_identity_id=subject::uuid
          AND v.country=governing_country::char(2)
        ORDER BY v.id LIMIT 1001 FOR UPDATE) a;
  IF cardinality(applications)>1000
     OR EXISTS(SELECT 1 FROM supply.vendor_documents d WHERE d.application_id=ANY(applications))
     OR EXISTS(SELECT 1 FROM supply.application_submissions s
               WHERE s.application_id=ANY(applications)
                 AND (s.documents<>'[]'::jsonb OR s.contact_name IS NOT NULL))
     OR EXISTS(SELECT 1 FROM supply.vendor_applications v
               WHERE v.id=ANY(applications)
                 AND (v.contact_name<>'' OR v.bank_reference IS NOT NULL
                      OR v.bank_last4 IS NOT NULL OR v.bank_status IS NOT NULL
                      OR v.bank_holder_name IS NOT NULL OR v.bank_ifsc IS NOT NULL)) THEN
   RAISE EXCEPTION 'previous supply onboarding scrub evidence is stale';
  END IF;
  UPDATE supply.vendor_applications v
  SET verified=false,revision=revision+1,updated_at=clock_timestamp()
  WHERE v.id=ANY(applications) AND v.verified;
  PERFORM 1 FROM supply.privacy_scrub_vendor_presence(r);
  RETURN QUERY SELECT 'RETAINED'::text,evidence,prior.application_rows,prior.document_rows,prior.submission_rows;
  RETURN;
 END IF;
 SELECT coalesce(array_agg(a.id),ARRAY[]::uuid[]) INTO applications
 FROM (SELECT v.id FROM supply.vendor_applications v
       WHERE v.tenant_id=tenant::uuid AND v.vendor_identity_id=subject::uuid
         AND v.country=governing_country::char(2)
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
      bank_holder_name=NULL,bank_ifsc=NULL,verified=false,
      revision=revision+1,updated_at=clock_timestamp()
  WHERE v.id=ANY(applications);
 INSERT INTO supply.privacy_erasure_runs(request_id,tenant_id,subject_id,policy_version,countries,
  application_rows,document_rows,submission_rows,occurred_at)
 VALUES(r,tenant,subject,policy,countries,app_count,doc_count,sub_count,clock_timestamp());
 PERFORM 1 FROM supply.privacy_scrub_vendor_presence(r);
 RETURN QUERY SELECT 'RETAINED'::text,evidence,app_count,doc_count,sub_count;
END $$;
RESET ROLE;
