SET ROLE planext4u_fulfillment_owner;
CREATE OR REPLACE FUNCTION fulfillment.privacy_scrub_onboarding(r text)
RETURNS TABLE(outcome text,evidence_reference text,profile_rows integer,submission_rows integer)
LANGUAGE plpgsql VOLATILE SECURITY DEFINER SET search_path=pg_catalog,fulfillment AS $$
DECLARE tenant text; subject text; policy text; prior fulfillment.privacy_erasure_runs%ROWTYPE;
DECLARE profiles uuid[] := ARRAY[]::uuid[]; countries text[] := ARRAY[]::text[];
DECLARE profile_count integer := 0; sub_count integer := 0; evidence text;
BEGIN
 SELECT scope.tenant_id,scope.subject_id,scope.policy_version INTO tenant,subject,policy
 FROM identity.privacy_fulfillment_erasure_scope(r) scope;
 IF tenant IS NULL OR subject IS NULL OR policy IS NULL THEN RAISE EXCEPTION 'fulfillment erasure scope unavailable'; END IF;
 evidence := 'fulfillment-onboarding:'||r;
 SELECT * INTO prior FROM fulfillment.privacy_erasure_runs run WHERE run.request_id=r FOR UPDATE;
 IF FOUND THEN
  IF prior.tenant_id<>tenant OR prior.subject_id<>subject OR prior.policy_version<>policy THEN
   RAISE EXCEPTION 'fulfillment erasure retry scope changed';
  END IF;
  PERFORM identity.privacy_fulfillment_record_retained(r,policy,evidence);
  RETURN QUERY SELECT 'RETAINED'::text,evidence,prior.profile_rows,prior.submission_rows;
  RETURN;
 END IF;
 IF pg_catalog.pg_input_is_valid(tenant,'uuid') AND pg_catalog.pg_input_is_valid(subject,'uuid') THEN
  SELECT coalesce(array_agg(p.id),ARRAY[]::uuid[]) INTO profiles
  FROM (SELECT rp.id FROM fulfillment.rider_profiles rp
        WHERE rp.tenant_id=tenant::uuid AND rp.rider_identity_id=subject::uuid
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
RESET ROLE;
