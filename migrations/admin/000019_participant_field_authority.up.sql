BEGIN;
SET ROLE planext4u_admin_owner;

-- A country reviewer may reject or approve applications without a territory
-- assignment, but a field visit attests presence at a particular participant.
-- That attestation requires a current explicit command assignment at an active
-- ancestor of the participant's active binding, for every operator role.
CREATE FUNCTION admin.participant_field_assignment_allowed(t uuid,c char(2),s uuid,p uuid)
RETURNS boolean LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog,admin AS $$
 WITH RECURSIVE path AS (
  SELECT n.id,n.parent_id,n.active FROM admin.organization_nodes n
  JOIN admin.participant_territory_bindings b ON (b.tenant_id,b.country,b.node_id)=(n.tenant_id,n.country,n.id)
  WHERE b.tenant_id=t AND b.country=c AND b.kind='VENDOR' AND b.participant_id=p AND b.active
  UNION ALL SELECT n.id,n.parent_id,n.active FROM admin.organization_nodes n JOIN path x ON n.id=x.parent_id
  WHERE n.tenant_id=t AND n.country=c
 ) SELECT coalesce((SELECT bool_and(active) FROM path),false) AND EXISTS(
  SELECT 1 FROM admin.staff_assignments a JOIN admin.job_roles j ON (j.tenant_id,j.country,j.id)=(a.tenant_id,a.country,a.role_id)
  WHERE a.tenant_id=t AND a.country=c AND a.subject_id=s AND a.status='ACTIVE'
  AND a.node_id IN(SELECT id FROM path) AND 'resource.command'=ANY(j.capabilities))
$$;

CREATE FUNCTION admin.lock_participant_field_access(t uuid,c char(2),s uuid,sid uuid,rev bigint,p uuid)
RETURNS boolean LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,admin AS $$
BEGIN
 IF p IS NULL OR p=s OR NOT admin.lock_fulfillment_operator(t,c,s,sid,rev) THEN RETURN false; END IF;
 PERFORM pg_advisory_xact_lock_shared(hashtextextended(t::text||':'||c::text||':organization',0));
 RETURN admin.participant_field_assignment_allowed(t,c,s,p);
END $$;

REVOKE ALL ON FUNCTION admin.participant_field_assignment_allowed(uuid,char,uuid,uuid) FROM PUBLIC;
REVOKE ALL ON FUNCTION admin.lock_participant_field_access(uuid,char,uuid,uuid,bigint,uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION admin.participant_field_assignment_allowed(uuid,char,uuid,uuid),admin.lock_participant_field_access(uuid,char,uuid,uuid,bigint,uuid)
 TO planext4u_supply_runtime;
RESET ROLE;
COMMIT;
