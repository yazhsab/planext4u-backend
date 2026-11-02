SET ROLE planext4u_fulfillment_owner;
LOCK TABLE fulfillment.privacy_erasure_runs IN SHARE ROW EXCLUSIVE MODE;
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM fulfillment.privacy_erasure_runs) THEN
  RAISE EXCEPTION 'cannot roll back fulfillment privacy fence after an owner scrub';
 END IF;
END $$;
DROP TRIGGER rider_profile_privacy_fence ON fulfillment.rider_profiles;
DROP TRIGGER rider_location_privacy_fence ON fulfillment.rider_locations;
DROP TRIGGER rider_location_history_privacy_fence ON fulfillment.rider_location_history;
DROP TRIGGER rider_submission_privacy_fence ON fulfillment.rider_submissions;
DROP TRIGGER privacy_erasure_run_immutable ON fulfillment.privacy_erasure_runs;
DROP FUNCTION fulfillment.privacy_guard_rider_scoped_row();
DROP FUNCTION fulfillment.privacy_guard_rider_submission();
DROP FUNCTION fulfillment.privacy_guard_erasure_run();
DROP FUNCTION fulfillment.privacy_assert_rider_scope_mutable(uuid,char,uuid);
CREATE OR REPLACE FUNCTION fulfillment.privacy_scrub_onboarding(r text)
RETURNS TABLE(outcome text,evidence_reference text,profile_rows integer,submission_rows integer)
LANGUAGE plpgsql VOLATILE SECURITY DEFINER SET search_path=pg_catalog,fulfillment AS $$
DECLARE tenant text; subject text; policy text; governing_country text; prior fulfillment.privacy_erasure_runs%ROWTYPE;
DECLARE profiles uuid[] := ARRAY[]::uuid[]; countries text[] := ARRAY[]::text[];
DECLARE profile_count integer := 0; sub_count integer := 0; evidence text;
BEGIN
 SELECT scope.tenant_id,scope.subject_id,scope.policy_version,scope.country INTO tenant,subject,policy,governing_country
 FROM identity.privacy_fulfillment_erasure_country_scope(r) scope;
 IF tenant IS NULL OR subject IS NULL OR policy IS NULL OR governing_country IS NULL THEN RAISE EXCEPTION 'fulfillment erasure scope unavailable'; END IF;
 evidence := 'fulfillment-onboarding:'||r;
 SELECT * INTO prior FROM fulfillment.privacy_erasure_runs run WHERE run.request_id=r FOR UPDATE;
 IF FOUND THEN
  IF prior.tenant_id<>tenant OR prior.subject_id<>subject OR prior.policy_version<>policy OR NOT (prior.countries <@ ARRAY[governing_country]::text[]) THEN
   RAISE EXCEPTION 'fulfillment erasure retry scope changed';
  END IF;
  PERFORM identity.privacy_fulfillment_record_retained(r,policy,evidence);
  RETURN QUERY SELECT 'RETAINED'::text,evidence,prior.profile_rows,prior.submission_rows;
  RETURN;
 END IF;
 IF pg_catalog.pg_input_is_valid(tenant,'uuid') AND pg_catalog.pg_input_is_valid(subject,'uuid') THEN
  SELECT coalesce(array_agg(p.id),ARRAY[]::uuid[]) INTO profiles
  FROM (SELECT rp.id FROM fulfillment.rider_profiles rp
        WHERE rp.tenant_id=tenant::uuid AND rp.rider_identity_id=subject::uuid AND rp.country=governing_country::char(2)
        ORDER BY rp.id LIMIT 1001 FOR UPDATE) p;
  profile_count := cardinality(profiles);
  IF profile_count>1000 THEN RAISE EXCEPTION 'fulfillment erasure requires a smaller batch'; END IF;
  SELECT count(*) INTO sub_count FROM fulfillment.rider_submissions s WHERE s.profile_id=ANY(profiles);
  IF sub_count>1000 THEN RAISE EXCEPTION 'fulfillment erasure requires a smaller batch'; END IF;
  SELECT coalesce(array_agg(DISTINCT p.country::text),ARRAY[]::text[]) INTO countries
  FROM fulfillment.rider_profiles p WHERE p.id=ANY(profiles);
  UPDATE fulfillment.rider_submissions s SET documents='[]'::jsonb
  WHERE s.profile_id=ANY(profiles);
  UPDATE fulfillment.rider_profiles p
   SET full_name='',phone_masked='',vehicle_number='',bank_reference='',
       documents='[]'::jsonb,review_reason=''
   WHERE p.id=ANY(profiles);
 END IF;
 INSERT INTO fulfillment.privacy_erasure_runs(request_id,tenant_id,subject_id,policy_version,countries,
  profile_rows,submission_rows,occurred_at)
 VALUES(r,tenant,subject,policy,countries,profile_count,sub_count,clock_timestamp());
 PERFORM identity.privacy_fulfillment_record_retained(r,policy,evidence);
 RETURN QUERY SELECT 'RETAINED'::text,evidence,profile_count,sub_count;
END $$;
ALTER TABLE fulfillment.privacy_erasure_runs DROP COLUMN location_rows;
RESET ROLE;
