BEGIN;
SET ROLE planext4u_admin_owner;
GRANT EXECUTE ON FUNCTION admin.lock_fulfillment_operator(uuid,char,uuid,uuid,bigint) TO planext4u_admin_runtime;
CREATE TABLE admin.audience_groups (
 tenant_id uuid NOT NULL,country char(2) NOT NULL,id uuid NOT NULL,node_id uuid NOT NULL,
 name text NOT NULL CHECK(char_length(name) BETWEEN 1 AND 128),
 role_ids uuid[] NOT NULL CHECK(cardinality(role_ids) BETWEEN 1 AND 32),
 include_descendants boolean NOT NULL,active boolean NOT NULL,revision bigint NOT NULL CHECK(revision>0),
 PRIMARY KEY(tenant_id,country,id),
 FOREIGN KEY(tenant_id,country,node_id) REFERENCES admin.organization_nodes(tenant_id,country,id)
);
CREATE INDEX audience_groups_node_idx ON admin.audience_groups(tenant_id,country,node_id);
CREATE TABLE admin.audience_group_versions (
 tenant_id uuid NOT NULL,country char(2) NOT NULL,group_id uuid NOT NULL,revision bigint NOT NULL,
 definition jsonb NOT NULL,actor_id uuid NOT NULL,occurred_at timestamptz NOT NULL,
 PRIMARY KEY(tenant_id,country,group_id,revision),
 FOREIGN KEY(tenant_id,country,group_id) REFERENCES admin.audience_groups(tenant_id,country,id)
);
GRANT SELECT,INSERT,UPDATE ON admin.audience_groups TO planext4u_admin_runtime;
GRANT SELECT,INSERT ON admin.audience_group_versions TO planext4u_admin_runtime;

-- Membership is derived from current active assignments and active ancestors.
-- No client supplied role, cached member list or cross-country tree is trusted.
CREATE FUNCTION admin.audience_member(t uuid,c char(2),s uuid,g uuid)
RETURNS boolean LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog,admin AS $$
 WITH RECURSIVE definition AS (
  SELECT node_id,role_ids,include_descendants FROM admin.audience_groups
  WHERE tenant_id=t AND country=c AND id=g AND active
 ), subtree AS (
  SELECT n.id FROM admin.organization_nodes n JOIN definition d ON d.node_id=n.id
  WHERE n.tenant_id=t AND n.country=c
  UNION ALL SELECT n.id FROM admin.organization_nodes n JOIN subtree p ON n.parent_id=p.id
  WHERE n.tenant_id=t AND n.country=c AND (SELECT include_descendants FROM definition)
 ), candidates AS (
  SELECT DISTINCT a.node_id FROM admin.staff_assignments a
  JOIN definition d ON a.role_id=ANY(d.role_ids)
  WHERE a.tenant_id=t AND a.country=c AND a.subject_id=s AND a.status='ACTIVE'
  AND a.node_id IN(SELECT id FROM subtree)
 ), paths AS (
  SELECT n.id,n.parent_id,n.active,n.id AS leaf FROM admin.organization_nodes n
  JOIN candidates x ON x.node_id=n.id WHERE n.tenant_id=t AND n.country=c
  UNION ALL SELECT n.id,n.parent_id,n.active,p.leaf FROM admin.organization_nodes n
  JOIN paths p ON n.id=p.parent_id WHERE n.tenant_id=t AND n.country=c
 ) SELECT EXISTS(SELECT leaf FROM paths GROUP BY leaf HAVING bool_and(active))
$$;
REVOKE ALL ON FUNCTION admin.audience_member(uuid,char,uuid,uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION admin.audience_member(uuid,char,uuid,uuid) TO planext4u_admin_runtime,planext4u_configuration_runtime;
GRANT USAGE ON SCHEMA admin TO planext4u_configuration_runtime;
RESET ROLE;
COMMIT;
