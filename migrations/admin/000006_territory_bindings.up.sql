BEGIN;
SET ROLE planext4u_admin_owner;
CREATE TABLE admin.fulfillment_territory_bindings (
 tenant_id uuid NOT NULL,country char(2) NOT NULL,territory_id uuid NOT NULL,
 node_id uuid NOT NULL,revision bigint NOT NULL CHECK(revision>0),reason text NOT NULL CHECK(char_length(reason) BETWEEN 8 AND 500),
 PRIMARY KEY(tenant_id,country,territory_id),
 FOREIGN KEY(tenant_id,country,node_id) REFERENCES admin.organization_nodes(tenant_id,country,id)
);
GRANT SELECT,INSERT,UPDATE ON admin.fulfillment_territory_bindings TO planext4u_admin_runtime;
CREATE TABLE admin.fulfillment_territory_binding_events (
 sequence_id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 tenant_id uuid NOT NULL,country char(2) NOT NULL,territory_id uuid NOT NULL,node_id uuid NOT NULL,
 revision bigint NOT NULL,actor_id uuid NOT NULL,reason text NOT NULL,occurred_at timestamptz NOT NULL
);
GRANT SELECT,INSERT ON admin.fulfillment_territory_binding_events TO planext4u_admin_runtime;
GRANT USAGE,SELECT ON SEQUENCE admin.fulfillment_territory_binding_events_sequence_id_seq TO planext4u_admin_runtime;
-- The owning API can ask a bounded boolean question without receiving staff,
-- invitations or organization table access. Actor/scope come from its gateway.
CREATE FUNCTION admin.fulfillment_territory_allowed(t uuid,c char(2),s uuid,r uuid,cap text)
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
REVOKE ALL ON FUNCTION admin.fulfillment_territory_allowed(uuid,char,uuid,uuid,text) FROM PUBLIC;
GRANT USAGE ON SCHEMA admin TO planext4u_fulfillment_runtime;
GRANT EXECUTE ON FUNCTION admin.fulfillment_territory_allowed(uuid,char,uuid,uuid,text) TO planext4u_fulfillment_runtime;
RESET ROLE;
COMMIT;
