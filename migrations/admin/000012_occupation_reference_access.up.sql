BEGIN;
SET ROLE planext4u_admin_owner;
-- Bounded safe reference data only. Neither reader can access operator,
-- participant or change-history tables through these functions.
CREATE FUNCTION admin.active_occupations(t uuid,c char(2),term text,after_id uuid)
RETURNS TABLE(id uuid,code text,name_en text,name_ta text,master_revision bigint)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog,admin AS $$
 SELECT m.id,m.code,m.name_en,m.name_ta,m.revision FROM admin.master_data m
 WHERE m.tenant_id=t AND m.country=c AND m.kind='OCCUPATION' AND m.active
 AND length(term)<=128 AND strpos(lower(m.name_en||' '||m.name_ta||' '||m.code),lower(term))>0
 AND (after_id IS NULL OR m.id>after_id) ORDER BY m.id LIMIT 51
$$;
CREATE FUNCTION admin.lock_occupation_reference(t uuid,c char(2),reference_id uuid)
RETURNS jsonb LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,admin AS $$
DECLARE value jsonb;
BEGIN
 SELECT jsonb_build_object('id',m.id,'code',m.code,'country',m.country,'name_en',m.name_en,'name_ta',m.name_ta,'master_revision',m.revision)
 INTO value FROM admin.master_data m
 WHERE m.tenant_id=t AND m.country=c AND m.id=reference_id AND m.kind='OCCUPATION' AND m.active FOR SHARE;
 RETURN value;
END $$;
REVOKE ALL ON FUNCTION admin.active_occupations(uuid,char,text,uuid),admin.lock_occupation_reference(uuid,char,uuid) FROM PUBLIC;
GRANT USAGE ON SCHEMA admin TO planext4u_configuration_runtime,planext4u_identity_runtime;
GRANT EXECUTE ON FUNCTION admin.active_occupations(uuid,char,text,uuid) TO planext4u_configuration_runtime;
GRANT EXECUTE ON FUNCTION admin.lock_occupation_reference(uuid,char,uuid) TO planext4u_identity_runtime;
RESET ROLE;
COMMIT;
