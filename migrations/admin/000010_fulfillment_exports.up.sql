BEGIN;
SET ROLE planext4u_admin_owner;

CREATE OR REPLACE FUNCTION admin.fulfillment_territory_allowed(t uuid,c char(2),s uuid,r uuid,cap text)
RETURNS boolean LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog,admin AS $$
 WITH RECURSIVE path AS (
  SELECT n.id,n.parent_id,n.active FROM admin.organization_nodes n
  JOIN admin.fulfillment_territory_bindings b ON (b.tenant_id,b.country,b.node_id)=(n.tenant_id,n.country,n.id)
  WHERE b.tenant_id=t AND b.country=c AND b.territory_id=r
  UNION ALL SELECT n.id,n.parent_id,n.active FROM admin.organization_nodes n JOIN path p ON n.id=p.parent_id
  WHERE n.tenant_id=t AND n.country=c
 ) SELECT cap IN ('resource.read','resource.command','resource.export') AND coalesce((SELECT bool_and(active) FROM path),false) AND EXISTS(
  SELECT 1 FROM admin.staff_assignments a JOIN admin.job_roles j ON (j.tenant_id,j.country,j.id)=(a.tenant_id,a.country,a.role_id)
  WHERE a.tenant_id=t AND a.country=c AND a.subject_id=s AND a.status='ACTIVE'
  AND a.node_id IN (SELECT id FROM path) AND cap=ANY(j.capabilities))
$$;

-- Directory and source-session locks are retained through the owning export
-- transaction. Country authority comes from the current directory/session,
-- never from caller-provided fulfillment role names.
CREATE FUNCTION admin.fulfillment_export_scope(t uuid,c char(2),s uuid,source_session uuid,expected_revision bigint)
RETURNS text LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,admin AS $$
DECLARE current_roles text[];
BEGIN
 IF NOT admin.lock_fulfillment_operator(t,c,s,source_session,expected_revision) THEN RETURN NULL; END IF;
 SELECT roles INTO current_roles FROM admin.sessions WHERE session_id=source_session;
 IF current_roles && ARRAY['SUPER_ADMIN','COUNTRY_ADMIN']::text[] THEN RETURN 'COUNTRY'; END IF;
 RETURN 'SCOPED';
END $$;
REVOKE ALL ON FUNCTION admin.fulfillment_export_scope(uuid,char,uuid,uuid,bigint) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION admin.fulfillment_export_scope(uuid,char,uuid,uuid,bigint) TO planext4u_fulfillment_runtime;
RESET ROLE;
COMMIT;
