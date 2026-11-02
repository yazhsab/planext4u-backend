BEGIN;
SET ROLE planext4u_admin_owner;
DROP FUNCTION admin.fulfillment_export_scope(uuid,char,uuid,uuid,bigint);
CREATE OR REPLACE FUNCTION admin.fulfillment_territory_allowed(t uuid,c char(2),s uuid,r uuid,cap text)
RETURNS boolean LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog,admin AS $$
 WITH RECURSIVE path AS (
  SELECT n.id,n.parent_id,n.active FROM admin.organization_nodes n
  JOIN admin.fulfillment_territory_bindings b ON (b.tenant_id,b.country,b.node_id)=(n.tenant_id,n.country,n.id)
  WHERE b.tenant_id=t AND b.country=c AND b.territory_id=r
  UNION ALL SELECT n.id,n.parent_id,n.active FROM admin.organization_nodes n JOIN path p ON n.id=p.parent_id
  WHERE n.tenant_id=t AND n.country=c
 ) SELECT cap IN ('resource.read','resource.command') AND coalesce((SELECT bool_and(active) FROM path),false) AND EXISTS(
  SELECT 1 FROM admin.staff_assignments a JOIN admin.job_roles j ON (j.tenant_id,j.country,j.id)=(a.tenant_id,a.country,a.role_id)
  WHERE a.tenant_id=t AND a.country=c AND a.subject_id=s AND a.status='ACTIVE'
  AND a.node_id IN (SELECT id FROM path) AND cap=ANY(j.capabilities))
$$;
RESET ROLE;
COMMIT;
