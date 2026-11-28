BEGIN;
SET ROLE planext4u_admin_owner;
CREATE FUNCTION admin.active_reasons(t uuid,c char(2),term text,after_id uuid)
RETURNS TABLE(id uuid,code text,name_en text,name_ta text,master_revision bigint)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog,admin AS $$
 SELECT m.id,m.code,m.name_en,m.name_ta,m.revision FROM admin.master_data m
 WHERE m.tenant_id=t AND m.country=c AND m.kind='REASON' AND m.active
 AND m.code IN ('TOO_FAR','VEHICLE_OR_CAPACITY','ENDING_DUTY','SAFETY_CONCERN','OTHER')
 AND length(term)<=128 AND strpos(lower(m.name_en||' '||m.name_ta||' '||m.code),lower(term))>0
 AND (after_id IS NULL OR m.id>after_id) ORDER BY m.id LIMIT 51
$$;
REVOKE ALL ON FUNCTION admin.active_reasons(uuid,char,text,uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION admin.active_reasons(uuid,char,text,uuid) TO planext4u_configuration_runtime;
RESET ROLE;
COMMIT;
