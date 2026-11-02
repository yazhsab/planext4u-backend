SET ROLE planext4u_fulfillment_owner;

-- This remains partial owner erasure. Delivery, chat, attendance, payout,
-- command and audit records are retained for their separately governed terms.
ALTER TABLE fulfillment.privacy_erasure_runs
 ADD COLUMN location_rows integer NOT NULL DEFAULT 0 CHECK(location_rows BETWEEN 0 AND 2);

-- A retained owner receipt is permanent evidence of the scoped scrub. Runtime
-- writers cannot revive private rows after it, even through a direct DML path.
CREATE FUNCTION fulfillment.privacy_assert_rider_scope_mutable(t uuid,c char(2),s uuid)
RETURNS void LANGUAGE plpgsql VOLATILE SECURITY DEFINER
SET search_path=pg_catalog,fulfillment AS $$
BEGIN
 IF t IS NULL OR c IS NULL OR s IS NULL THEN
  RAISE EXCEPTION 'fulfillment rider scope is unavailable';
 END IF;
 IF EXISTS(SELECT 1 FROM fulfillment.privacy_erasure_runs run
           WHERE run.tenant_id=t::text AND run.subject_id=s::text
             AND (cardinality(run.countries)=0 OR c::text=ANY(run.countries))) THEN
  RAISE EXCEPTION 'fulfillment rider scope was scrubbed';
 END IF;
END $$;
REVOKE ALL ON FUNCTION fulfillment.privacy_assert_rider_scope_mutable(uuid,char,uuid) FROM PUBLIC;

CREATE FUNCTION fulfillment.privacy_guard_rider_scoped_row() RETURNS trigger
LANGUAGE plpgsql VOLATILE SECURITY DEFINER SET search_path=pg_catalog,fulfillment AS $$
BEGIN
 IF TG_OP<>'INSERT' THEN
  PERFORM fulfillment.privacy_assert_rider_scope_mutable(
   OLD.tenant_id,OLD.country,OLD.rider_identity_id);
 END IF;
 IF TG_OP<>'DELETE' THEN
  PERFORM fulfillment.privacy_assert_rider_scope_mutable(
   NEW.tenant_id,NEW.country,NEW.rider_identity_id);
 END IF;
 IF TG_OP='DELETE' THEN RETURN OLD; END IF;
 RETURN NEW;
END $$;
REVOKE ALL ON FUNCTION fulfillment.privacy_guard_rider_scoped_row() FROM PUBLIC;
CREATE TRIGGER rider_profile_privacy_fence BEFORE INSERT OR UPDATE OR DELETE
 ON fulfillment.rider_profiles FOR EACH ROW
 EXECUTE FUNCTION fulfillment.privacy_guard_rider_scoped_row();
CREATE TRIGGER rider_location_privacy_fence BEFORE INSERT OR UPDATE OR DELETE
 ON fulfillment.rider_locations FOR EACH ROW
 EXECUTE FUNCTION fulfillment.privacy_guard_rider_scoped_row();
CREATE TRIGGER rider_location_history_privacy_fence BEFORE INSERT OR UPDATE OR DELETE
 ON fulfillment.rider_location_history FOR EACH ROW
 EXECUTE FUNCTION fulfillment.privacy_guard_rider_scoped_row();

CREATE FUNCTION fulfillment.privacy_guard_rider_submission() RETURNS trigger
LANGUAGE plpgsql VOLATILE SECURITY DEFINER SET search_path=pg_catalog,fulfillment AS $$
DECLARE scope record;
BEGIN
 IF TG_OP<>'INSERT' THEN
  SELECT p.tenant_id,p.country,p.rider_identity_id INTO scope
  FROM fulfillment.rider_profiles p WHERE p.id=OLD.profile_id FOR KEY SHARE;
  IF NOT FOUND THEN RAISE EXCEPTION 'fulfillment rider submission owner unavailable'; END IF;
  PERFORM fulfillment.privacy_assert_rider_scope_mutable(
   scope.tenant_id,scope.country,scope.rider_identity_id);
 END IF;
 IF TG_OP<>'DELETE' THEN
  SELECT p.tenant_id,p.country,p.rider_identity_id INTO scope
  FROM fulfillment.rider_profiles p WHERE p.id=NEW.profile_id FOR KEY SHARE;
  IF NOT FOUND THEN RAISE EXCEPTION 'fulfillment rider submission owner unavailable'; END IF;
  PERFORM fulfillment.privacy_assert_rider_scope_mutable(
   scope.tenant_id,scope.country,scope.rider_identity_id);
 END IF;
 IF TG_OP='DELETE' THEN RETURN OLD; END IF;
 RETURN NEW;
END $$;
REVOKE ALL ON FUNCTION fulfillment.privacy_guard_rider_submission() FROM PUBLIC;
CREATE TRIGGER rider_submission_privacy_fence BEFORE INSERT OR UPDATE OR DELETE
 ON fulfillment.rider_submissions FOR EACH ROW
 EXECUTE FUNCTION fulfillment.privacy_guard_rider_submission();

CREATE FUNCTION fulfillment.privacy_guard_erasure_run() RETURNS trigger
LANGUAGE plpgsql VOLATILE SECURITY DEFINER SET search_path=pg_catalog,fulfillment AS $$
BEGIN
 RAISE EXCEPTION 'fulfillment erasure evidence is immutable';
END $$;
REVOKE ALL ON FUNCTION fulfillment.privacy_guard_erasure_run() FROM PUBLIC;
CREATE TRIGGER privacy_erasure_run_immutable BEFORE UPDATE OR DELETE OR TRUNCATE
 ON fulfillment.privacy_erasure_runs FOR EACH STATEMENT
 EXECUTE FUNCTION fulfillment.privacy_guard_erasure_run();

CREATE OR REPLACE FUNCTION fulfillment.privacy_scrub_onboarding(r text)
RETURNS TABLE(outcome text,evidence_reference text,profile_rows integer,submission_rows integer)
LANGUAGE plpgsql VOLATILE SECURITY DEFINER SET search_path=pg_catalog,fulfillment AS $$
DECLARE tenant text; subject text; policy text; governing_country text; prior fulfillment.privacy_erasure_runs%ROWTYPE;
DECLARE profiles uuid[] := ARRAY[]::uuid[]; countries text[] := ARRAY[]::text[];
DECLARE profile_count integer := 0; sub_count integer := 0; location_count integer := 0;
DECLARE removed integer; evidence text;
BEGIN
 SELECT scope.tenant_id,scope.subject_id,scope.policy_version,scope.country
 INTO tenant,subject,policy,governing_country
 FROM identity.privacy_fulfillment_erasure_country_scope(r) scope;
 IF tenant IS NULL OR subject IS NULL OR policy IS NULL OR governing_country IS NULL
    OR NOT pg_catalog.pg_input_is_valid(tenant,'uuid')
    OR NOT pg_catalog.pg_input_is_valid(subject,'uuid') THEN
  RAISE EXCEPTION 'fulfillment erasure scope unavailable';
 END IF;
 evidence := 'fulfillment-onboarding:'||r;

 -- These transaction-level locks serialize the absence check and receipt
 -- insert with direct profile, submission and location writes. The row
 -- triggers then reject every later write to the scrubbed country scope.
 LOCK TABLE fulfillment.rider_profiles,fulfillment.rider_submissions,
            fulfillment.rider_locations,fulfillment.rider_location_history
 IN SHARE ROW EXCLUSIVE MODE;

 -- Duty start takes this same profile lock before inserting its duty row.
 -- Locking profiles first makes the active-duty check observe a concurrent
 -- committed start, or makes the starter observe the scrubbed SUSPENDED row.
 SELECT coalesce(array_agg(p.id),ARRAY[]::uuid[]) INTO profiles
 FROM (SELECT rp.id FROM fulfillment.rider_profiles rp
       WHERE rp.tenant_id=tenant::uuid AND rp.rider_identity_id=subject::uuid
         AND rp.country=governing_country::char(2)
       ORDER BY rp.id LIMIT 1001 FOR UPDATE) p;
 profile_count := cardinality(profiles);
 IF profile_count>1000 THEN RAISE EXCEPTION 'fulfillment erasure requires a smaller batch'; END IF;
 SELECT count(*) INTO sub_count FROM fulfillment.rider_submissions s
 WHERE s.profile_id=ANY(profiles);
 IF sub_count>1000 THEN RAISE EXCEPTION 'fulfillment erasure requires a smaller batch'; END IF;

 -- An active duty or unfinished assigned delivery needs operational handling
 -- before any private onboarding or location data is removed.
 PERFORM 1 FROM fulfillment.duty_sessions d
 WHERE d.tenant_id=tenant::uuid AND d.country=governing_country::char(2)
   AND d.rider_identity_id=subject::uuid AND d.status='ACTIVE'
 LIMIT 1 FOR UPDATE;
 IF FOUND THEN RAISE EXCEPTION 'fulfillment rider has active duty'; END IF;
 PERFORM 1 FROM fulfillment.delivery_tasks task
 WHERE task.tenant_id=tenant::uuid AND task.country=governing_country::char(2)
   AND task.assigned_rider_identity_id=subject::uuid
   AND task.status NOT IN ('DELIVERED','CANCELLED')
 LIMIT 1 FOR UPDATE;
 IF FOUND THEN RAISE EXCEPTION 'fulfillment rider has active delivery'; END IF;

 SELECT * INTO prior FROM fulfillment.privacy_erasure_runs run
 WHERE run.request_id=r FOR UPDATE;
 IF FOUND THEN
  IF prior.tenant_id<>tenant OR prior.subject_id<>subject
     OR prior.policy_version<>policy
     OR prior.profile_rows<>profile_count
     OR prior.submission_rows<>sub_count
     OR NOT (prior.countries <@ ARRAY[governing_country]::text[])
     OR EXISTS(SELECT 1 FROM fulfillment.rider_profiles p
               WHERE p.id=ANY(profiles) AND
                (p.status<>'SUSPENDED' OR p.full_name<>'' OR p.phone_masked<>''
                 OR p.vehicle_number<>'' OR p.bank_reference<>'' OR p.bank_status<>''
                 OR cardinality(p.zones)<>0 OR p.documents<>'[]'::jsonb
                 OR p.review_reason<>'' OR p.rework_allowed))
     OR EXISTS(SELECT 1 FROM fulfillment.rider_submissions s
               WHERE s.profile_id=ANY(profiles) AND s.documents<>'[]'::jsonb)
     OR EXISTS(SELECT 1 FROM fulfillment.rider_locations l
               WHERE l.tenant_id=tenant::uuid AND l.country=governing_country::char(2)
                 AND l.rider_identity_id=subject::uuid)
     OR EXISTS(SELECT 1 FROM fulfillment.rider_location_history l
               WHERE l.tenant_id=tenant::uuid AND l.country=governing_country::char(2)
                 AND l.rider_identity_id=subject::uuid) THEN
   RAISE EXCEPTION 'fulfillment erasure retry scope or state changed';
  END IF;
  PERFORM identity.privacy_fulfillment_record_retained(r,policy,evidence);
  RETURN QUERY SELECT 'RETAINED'::text,evidence,prior.profile_rows,prior.submission_rows;
  RETURN;
 END IF;

 DELETE FROM fulfillment.rider_locations l
 WHERE l.tenant_id=tenant::uuid AND l.country=governing_country::char(2)
   AND l.rider_identity_id=subject::uuid;
 GET DIAGNOSTICS removed=ROW_COUNT;
 location_count := location_count+removed;
 DELETE FROM fulfillment.rider_location_history l
 WHERE l.tenant_id=tenant::uuid AND l.country=governing_country::char(2)
   AND l.rider_identity_id=subject::uuid;
 GET DIAGNOSTICS removed=ROW_COUNT;
 location_count := location_count+removed;

 UPDATE fulfillment.rider_submissions s SET documents='[]'::jsonb
 WHERE s.profile_id=ANY(profiles);
 UPDATE fulfillment.rider_profiles p
  SET status='SUSPENDED',revision=p.revision+1,full_name='',phone_masked='',
      vehicle_number='',bank_reference='',bank_status='',zones=ARRAY[]::text[],
      documents='[]'::jsonb,review_reason='',rework_allowed=false,
      updated_at=clock_timestamp()
  WHERE p.id=ANY(profiles);

 countries := ARRAY[governing_country]::text[];
 INSERT INTO fulfillment.privacy_erasure_runs
  (request_id,tenant_id,subject_id,policy_version,countries,
   profile_rows,submission_rows,location_rows,occurred_at)
 VALUES(r,tenant,subject,policy,countries,profile_count,sub_count,location_count,
        clock_timestamp());
 PERFORM identity.privacy_fulfillment_record_retained(r,policy,evidence);
 RETURN QUERY SELECT 'RETAINED'::text,evidence,profile_count,sub_count;
END $$;
RESET ROLE;
