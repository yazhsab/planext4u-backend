BEGIN;
SET ROLE planext4u_admin_owner;
CREATE TABLE admin.participant_territory_bindings (
 tenant_id uuid NOT NULL,country char(2) NOT NULL,kind text NOT NULL CHECK(kind IN ('VENDOR','RIDER')),
 participant_id uuid NOT NULL,node_id uuid NOT NULL,active boolean NOT NULL,
 revision bigint NOT NULL CHECK(revision>0),reason text NOT NULL CHECK(char_length(reason) BETWEEN 8 AND 500),
 PRIMARY KEY(tenant_id,country,kind,participant_id),
 FOREIGN KEY(tenant_id,country,node_id) REFERENCES admin.organization_nodes(tenant_id,country,id)
);
CREATE TABLE admin.participant_territory_events (
 tenant_id uuid NOT NULL,country char(2) NOT NULL,kind text NOT NULL,participant_id uuid NOT NULL,
 revision bigint NOT NULL,node_id uuid NOT NULL,active boolean NOT NULL,actor_id uuid NOT NULL,
 reason text NOT NULL,occurred_at timestamptz NOT NULL,
 PRIMARY KEY(tenant_id,country,kind,participant_id,revision)
);
GRANT SELECT,INSERT,UPDATE ON admin.participant_territory_bindings TO planext4u_admin_runtime;
GRANT SELECT,INSERT ON admin.participant_territory_events TO planext4u_admin_runtime;

CREATE FUNCTION admin.participant_access_allowed(t uuid,c char(2),s uuid,sid uuid,k text,p uuid,cap text)
RETURNS boolean LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog,admin AS $$
 WITH RECURSIVE path AS (
  SELECT n.id,n.parent_id,n.active FROM admin.organization_nodes n
  JOIN admin.participant_territory_bindings b ON (b.tenant_id,b.country,b.node_id)=(n.tenant_id,n.country,n.id)
  WHERE b.tenant_id=t AND b.country=c AND b.kind=k AND b.participant_id=p AND b.active
  UNION ALL SELECT n.id,n.parent_id,n.active FROM admin.organization_nodes n JOIN path x ON n.id=x.parent_id
  WHERE n.tenant_id=t AND n.country=c
 ) SELECT k IN ('VENDOR','RIDER') AND cap IN ('resource.read','resource.command') AND (
  EXISTS(SELECT 1 FROM admin.sessions WHERE session_id=sid AND tenant_id=t AND subject_id=s AND selected_country=c AND roles && ARRAY['SUPER_ADMIN','COUNTRY_ADMIN']::text[])
  OR coalesce((SELECT bool_and(active) FROM path),false) AND EXISTS(
   SELECT 1 FROM admin.staff_assignments a JOIN admin.job_roles j ON (j.tenant_id,j.country,j.id)=(a.tenant_id,a.country,a.role_id)
   WHERE a.tenant_id=t AND a.country=c AND a.subject_id=s AND a.status='ACTIVE'
   AND a.node_id IN(SELECT id FROM path) AND cap=ANY(j.capabilities)))
$$;
CREATE FUNCTION admin.lock_participant_access(t uuid,c char(2),s uuid,sid uuid,rev bigint,k text,p uuid,cap text)
RETURNS boolean LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,admin AS $$
BEGIN
 IF k NOT IN ('VENDOR','RIDER') OR cap NOT IN ('resource.read','resource.command')
  OR NOT admin.lock_fulfillment_operator(t,c,s,sid,rev) THEN RETURN false; END IF;
 PERFORM pg_advisory_xact_lock_shared(hashtextextended(t::text||':'||c::text||':organization',0));
 IF p IS NULL THEN RETURN cap='resource.read'; END IF;
 RETURN admin.participant_access_allowed(t,c,s,sid,k,p,cap);
END $$;
REVOKE ALL ON FUNCTION admin.participant_access_allowed(uuid,char,uuid,uuid,text,uuid,text) FROM PUBLIC;
REVOKE ALL ON FUNCTION admin.lock_participant_access(uuid,char,uuid,uuid,bigint,text,uuid,text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION admin.participant_access_allowed(uuid,char,uuid,uuid,text,uuid,text),admin.lock_participant_access(uuid,char,uuid,uuid,bigint,text,uuid,text)
 TO planext4u_supply_runtime,planext4u_fulfillment_runtime;
GRANT EXECUTE ON FUNCTION admin.lock_participant_reviewer(uuid,char,uuid,uuid,bigint) TO planext4u_admin_runtime;
GRANT EXECUTE ON FUNCTION admin.lock_fulfillment_operator(uuid,char,uuid,uuid,bigint) TO planext4u_admin_runtime;
RESET ROLE;
COMMIT;
