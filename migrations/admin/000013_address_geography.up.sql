BEGIN;
SET ROLE planext4u_admin_owner;
CREATE FUNCTION admin.active_geography(t uuid,c char(2),k text,p uuid,term text,after_id uuid)
RETURNS TABLE(id uuid,kind text,code text,parent_id uuid,name_en text,name_ta text,master_revision bigint)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog,admin AS $$
 WITH RECURSIVE visible AS (
   SELECT m.id,m.kind,m.code,m.parent_id,m.name_en,m.name_ta,m.revision
   FROM admin.master_data m WHERE m.tenant_id=t AND m.country=c AND m.kind='COUNTRY' AND m.code=c AND m.active
   UNION ALL
   SELECT m.id,m.kind,m.code,m.parent_id,m.name_en,m.name_ta,m.revision
   FROM admin.master_data m JOIN visible v ON m.parent_id=v.id
   WHERE m.tenant_id=t AND m.country=c AND m.active AND
     (v.kind,m.kind) IN (('COUNTRY','STATE'),('STATE','DISTRICT'),('DISTRICT','CITY'),('CITY','AREA'))
 )
 SELECT v.id,v.kind,v.code,v.parent_id,v.name_en,v.name_ta,v.revision FROM visible v
 WHERE v.kind=k AND k IN ('COUNTRY','STATE','DISTRICT','CITY','AREA')
 AND ((k='COUNTRY' AND p IS NULL) OR (k<>'COUNTRY' AND v.parent_id=p))
 AND length(term)<=128 AND strpos(lower(v.name_en||' '||v.name_ta||' '||v.code),lower(term))>0
 AND (after_id IS NULL OR v.id>after_id) ORDER BY v.id LIMIT 51
$$;
CREATE FUNCTION admin.lock_address_geography(t uuid,c char(2),reference_id uuid)
RETURNS jsonb LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,admin AS $$
DECLARE co admin.master_data%ROWTYPE; st admin.master_data%ROWTYPE;
 di admin.master_data%ROWTYPE; ci admin.master_data%ROWTYPE; ar admin.master_data%ROWTYPE;
BEGIN
 SELECT * INTO ar FROM admin.master_data WHERE tenant_id=t AND country=c AND id=reference_id AND kind='AREA' AND active FOR SHARE;
 IF NOT FOUND THEN RETURN NULL; END IF;
 SELECT * INTO ci FROM admin.master_data WHERE tenant_id=t AND country=c AND id=ar.parent_id AND kind='CITY' AND active FOR SHARE;
 IF NOT FOUND THEN RETURN NULL; END IF;
 SELECT * INTO di FROM admin.master_data WHERE tenant_id=t AND country=c AND id=ci.parent_id AND kind='DISTRICT' AND active FOR SHARE;
 IF NOT FOUND THEN RETURN NULL; END IF;
 SELECT * INTO st FROM admin.master_data WHERE tenant_id=t AND country=c AND id=di.parent_id AND kind='STATE' AND active FOR SHARE;
 IF NOT FOUND THEN RETURN NULL; END IF;
 SELECT * INTO co FROM admin.master_data WHERE tenant_id=t AND country=c AND id=st.parent_id AND kind='COUNTRY' AND code=c AND active FOR SHARE;
 IF NOT FOUND THEN RETURN NULL; END IF;
 RETURN jsonb_build_object('area_id',ar.id,'path',(SELECT jsonb_agg(jsonb_build_object(
   'id',v.id,'country',v.country,'kind',v.kind,'code',v.code,'parent_id',v.parent_id,
   'name_en',v.name_en,'name_ta',v.name_ta,'master_revision',v.revision) ORDER BY v.depth)
   FROM (VALUES (co.id,co.country,co.kind,co.code,co.parent_id,co.name_en,co.name_ta,co.revision,1),
    (st.id,st.country,st.kind,st.code,st.parent_id,st.name_en,st.name_ta,st.revision,2),
    (di.id,di.country,di.kind,di.code,di.parent_id,di.name_en,di.name_ta,di.revision,3),
    (ci.id,ci.country,ci.kind,ci.code,ci.parent_id,ci.name_en,ci.name_ta,ci.revision,4),
    (ar.id,ar.country,ar.kind,ar.code,ar.parent_id,ar.name_en,ar.name_ta,ar.revision,5))
   AS v(id,country,kind,code,parent_id,name_en,name_ta,revision,depth)));
END $$;
REVOKE ALL ON FUNCTION admin.active_geography(uuid,char,text,uuid,text,uuid),admin.lock_address_geography(uuid,char,uuid) FROM PUBLIC;
GRANT USAGE ON SCHEMA admin TO planext4u_configuration_runtime,planext4u_commerce_runtime;
GRANT EXECUTE ON FUNCTION admin.active_geography(uuid,char,text,uuid,text,uuid) TO planext4u_configuration_runtime;
GRANT EXECUTE ON FUNCTION admin.lock_address_geography(uuid,char,uuid) TO planext4u_commerce_runtime;
RESET ROLE;
COMMIT;
