SET ROLE planext4u_identity_owner;
CREATE UNIQUE INDEX deletion_policy_version_unique
 ON identity.deletion_policy_proposals(tenant_id,country,owner,policy_version);

CREATE FUNCTION identity.require_deletion_governor(t uuid,c char(2),a uuid,s uuid,rev bigint)
RETURNS void LANGUAGE plpgsql VOLATILE SECURITY DEFINER SET search_path=pg_catalog,identity AS $$
BEGIN
 IF admin.lock_deletion_governor(t,c,a,s,rev) IS DISTINCT FROM true THEN
  RAISE EXCEPTION 'fresh country administrator authority is required';
 END IF;
END $$;
REVOKE ALL ON FUNCTION identity.require_deletion_governor(uuid,char,uuid,uuid,bigint) FROM PUBLIC;

CREATE FUNCTION identity.propose_deletion_policy(pid uuid,t uuid,c char(2),o text,pv text,
 retention interval,expected bigint,reason text,a uuid,s uuid,rev bigint)
RETURNS uuid LANGUAGE plpgsql VOLATILE SECURITY DEFINER SET search_path=pg_catalog,identity AS $$
DECLARE existing identity.deletion_policy_proposals%ROWTYPE; current_revision bigint;
BEGIN
 PERFORM identity.require_deletion_governor(t,c,a,s,rev);
 IF pid IS NULL OR o IS NULL OR pv IS NULL OR length(pv) NOT BETWEEN 1 AND 128
    OR retention IS NULL OR retention<interval '0' OR expected IS NULL OR expected<0
    OR reason IS NULL OR length(reason) NOT BETWEEN 8 AND 256 THEN
  RAISE EXCEPTION 'invalid deletion policy proposal';
 END IF;
 SELECT * INTO existing FROM identity.deletion_policy_proposals WHERE id=pid FOR UPDATE;
 IF FOUND THEN
  IF existing.tenant_id=t::text AND existing.country=c AND existing.owner=o
     AND existing.policy_version=pv AND existing.retention_interval=retention
     AND existing.expected_revision=expected AND existing.reason_reference=reason
     AND existing.requested_by=a THEN RETURN pid; END IF;
  RAISE EXCEPTION 'deletion policy proposal id conflicts';
 END IF;
 IF NOT EXISTS(SELECT 1 FROM identity.deletion_owner_requirements WHERE owner=o) THEN
  RAISE EXCEPTION 'unknown deletion owner';
 END IF;
 SELECT revision INTO current_revision FROM identity.deletion_country_policies
 WHERE tenant_id=t::text AND country=c AND owner=o FOR UPDATE;
 IF NOT FOUND THEN current_revision:=0; END IF;
 IF current_revision<>expected THEN RAISE EXCEPTION 'deletion policy revision conflict'; END IF;
 INSERT INTO identity.deletion_policy_proposals
 (id,tenant_id,country,owner,expected_revision,policy_version,retention_interval,
  reason_reference,status,requested_by,requested_at)
 VALUES(pid,t::text,c,o,expected,pv,retention,reason,'PENDING',a,clock_timestamp());
 INSERT INTO identity.deletion_governance_events
 (tenant_id,country,actor_id,actor_session_id,action,target_reference,reason_reference,revision,recorded_at)
 VALUES(t::text,c,a,s,'POLICY_PROPOSED',pid::text,reason,expected+1,clock_timestamp());
 RETURN pid;
END $$;

CREATE FUNCTION identity.publish_deletion_policy(pid uuid,t uuid,c char(2),a uuid,s uuid,rev bigint)
RETURNS bigint LANGUAGE plpgsql VOLATILE SECURITY DEFINER SET search_path=pg_catalog,identity AS $$
DECLARE proposal identity.deletion_policy_proposals%ROWTYPE; current_revision bigint;
BEGIN
 PERFORM identity.require_deletion_governor(t,c,a,s,rev);
 SELECT * INTO proposal FROM identity.deletion_policy_proposals WHERE id=pid FOR UPDATE;
 IF NOT FOUND OR proposal.tenant_id<>t::text OR proposal.country<>c THEN
  RAISE EXCEPTION 'deletion policy proposal not found in scope';
 END IF;
 IF proposal.requested_by=a THEN RAISE EXCEPTION 'independent deletion policy approver required'; END IF;
 IF proposal.status='PUBLISHED' THEN
  IF proposal.approved_by=a THEN RETURN proposal.expected_revision+1; END IF;
  RAISE EXCEPTION 'deletion policy already published';
 END IF;
 SELECT revision INTO current_revision FROM identity.deletion_country_policies
 WHERE tenant_id=t::text AND country=c AND owner=proposal.owner FOR UPDATE;
 IF NOT FOUND THEN current_revision:=0; END IF;
 IF current_revision<>proposal.expected_revision THEN
  RAISE EXCEPTION 'deletion policy revision conflict';
 END IF;
 INSERT INTO identity.deletion_country_policies
 (tenant_id,country,owner,revision,policy_version,retention_interval,published_at)
 VALUES(t::text,c,proposal.owner,current_revision+1,proposal.policy_version,
        proposal.retention_interval,clock_timestamp())
 ON CONFLICT(tenant_id,country,owner) DO UPDATE SET
 revision=EXCLUDED.revision,policy_version=EXCLUDED.policy_version,
 retention_interval=EXCLUDED.retention_interval,published_at=EXCLUDED.published_at;
 UPDATE identity.deletion_policy_proposals SET status='PUBLISHED',approved_by=a,
 published_at=clock_timestamp() WHERE id=pid;
 INSERT INTO identity.deletion_governance_events
 (tenant_id,country,actor_id,actor_session_id,action,target_reference,reason_reference,revision,recorded_at)
 VALUES(t::text,c,a,s,'POLICY_PUBLISHED',pid::text,proposal.reason_reference,
        current_revision+1,clock_timestamp());
 RETURN current_revision+1;
END $$;

CREATE FUNCTION identity.place_deletion_hold(r text,t uuid,c char(2),o text,
 reason text,a uuid,s uuid,rev bigint)
RETURNS bigint LANGUAGE plpgsql VOLATILE SECURITY DEFINER SET search_path=pg_catalog,identity AS $$
DECLARE request_row record; existing identity.deletion_legal_holds%ROWTYPE; hold_id bigint;
BEGIN
 PERFORM identity.require_deletion_governor(t,c,a,s,rev);
 IF r IS NULL OR o IS NULL OR reason IS NULL OR length(reason) NOT BETWEEN 8 AND 256 THEN
  RAISE EXCEPTION 'invalid deletion hold';
 END IF;
 SELECT d.status,d.country,i.tenant_id INTO request_row
 FROM identity.account_deletion_requests d JOIN identity.identities i ON i.id=d.identity_id
 WHERE d.id=r FOR UPDATE OF d;
 IF NOT FOUND OR request_row.status<>'SCHEDULED' OR request_row.country<>c
    OR request_row.tenant_id<>t::text THEN
  RAISE EXCEPTION 'scheduled deletion not found in scope';
 END IF;
 IF NOT EXISTS(SELECT 1 FROM identity.deletion_owner_requirements WHERE owner=o) THEN
  RAISE EXCEPTION 'unknown deletion owner';
 END IF;
 SELECT * INTO existing FROM identity.deletion_legal_holds
 WHERE request_id=r AND owner=o AND released_at IS NULL ORDER BY id LIMIT 1 FOR UPDATE;
 IF FOUND THEN
  IF existing.recorded_by=a AND existing.reason_reference=reason THEN RETURN existing.id; END IF;
  RAISE EXCEPTION 'deletion owner already has an active hold';
 END IF;
 INSERT INTO identity.deletion_legal_holds
 (request_id,owner,reason_reference,recorded_at,recorded_by)
 VALUES(r,o,reason,clock_timestamp(),a) RETURNING id INTO hold_id;
 INSERT INTO identity.deletion_governance_events
 (tenant_id,country,actor_id,actor_session_id,action,target_reference,reason_reference,revision,recorded_at)
 VALUES(t::text,c,a,s,'HOLD_PLACED',hold_id::text,reason,1,clock_timestamp());
 RETURN hold_id;
END $$;

CREATE FUNCTION identity.release_deletion_hold(hid bigint,t uuid,c char(2),expected bigint,
 reason text,a uuid,s uuid,rev bigint)
RETURNS bigint LANGUAGE plpgsql VOLATILE SECURITY DEFINER SET search_path=pg_catalog,identity AS $$
DECLARE r text; request_row record; hold_row identity.deletion_legal_holds%ROWTYPE;
BEGIN
 PERFORM identity.require_deletion_governor(t,c,a,s,rev);
 IF hid IS NULL OR expected IS NULL OR expected<1 OR reason IS NULL
    OR length(reason) NOT BETWEEN 8 AND 256 THEN
  RAISE EXCEPTION 'invalid deletion hold release';
 END IF;
 SELECT request_id INTO r FROM identity.deletion_legal_holds WHERE id=hid;
 IF NOT FOUND THEN RAISE EXCEPTION 'deletion hold not found'; END IF;
 SELECT d.status,d.country,i.tenant_id INTO request_row
 FROM identity.account_deletion_requests d JOIN identity.identities i ON i.id=d.identity_id
 WHERE d.id=r FOR UPDATE OF d;
 IF NOT FOUND OR request_row.status<>'SCHEDULED' OR request_row.country<>c
    OR request_row.tenant_id<>t::text THEN
  RAISE EXCEPTION 'scheduled deletion not found in scope';
 END IF;
 SELECT * INTO hold_row FROM identity.deletion_legal_holds WHERE id=hid FOR UPDATE;
 IF hold_row.recorded_by IS NULL OR hold_row.recorded_by=a THEN
  RAISE EXCEPTION 'independent hold releaser required';
 END IF;
 IF hold_row.released_at IS NOT NULL THEN
  IF hold_row.released_by=a AND hold_row.release_reason_reference=reason
     AND hold_row.revision=expected+1 THEN RETURN hold_row.revision; END IF;
  RAISE EXCEPTION 'deletion hold already released';
 END IF;
 IF hold_row.revision<>expected THEN RAISE EXCEPTION 'deletion hold revision conflict'; END IF;
 UPDATE identity.deletion_legal_holds SET released_at=clock_timestamp(),
 released_by=a,release_reason_reference=reason,revision=revision+1 WHERE id=hid;
 INSERT INTO identity.deletion_governance_events
 (tenant_id,country,actor_id,actor_session_id,action,target_reference,reason_reference,revision,recorded_at)
 VALUES(t::text,c,a,s,'HOLD_RELEASED',hid::text,reason,expected+1,clock_timestamp());
 RETURN expected+1;
END $$;

REVOKE ALL ON FUNCTION identity.propose_deletion_policy(uuid,uuid,char,text,text,interval,bigint,text,uuid,uuid,bigint),
 identity.publish_deletion_policy(uuid,uuid,char,uuid,uuid,bigint),
 identity.place_deletion_hold(text,uuid,char,text,text,uuid,uuid,bigint),
 identity.release_deletion_hold(bigint,uuid,char,bigint,text,uuid,uuid,bigint) FROM PUBLIC;
GRANT USAGE ON SCHEMA identity TO planext4u_admin_runtime;
GRANT EXECUTE ON FUNCTION identity.propose_deletion_policy(uuid,uuid,char,text,text,interval,bigint,text,uuid,uuid,bigint),
 identity.publish_deletion_policy(uuid,uuid,char,uuid,uuid,bigint),
 identity.place_deletion_hold(text,uuid,char,text,text,uuid,uuid,bigint),
 identity.release_deletion_hold(bigint,uuid,char,bigint,text,uuid,uuid,bigint) TO planext4u_identity_runtime,planext4u_admin_runtime;
RESET ROLE;
