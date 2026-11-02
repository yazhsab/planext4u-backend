BEGIN;
SET ROLE planext4u_admin_owner;
CREATE TABLE admin.master_data (
 tenant_id uuid NOT NULL, country char(2) NOT NULL CHECK(country ~ '^[A-Z]{2}$'),
 id uuid NOT NULL, kind text NOT NULL CHECK(kind IN ('COUNTRY','STATE','DISTRICT','CITY','AREA','OCCUPATION','REASON')),
 code text NOT NULL CHECK(code ~ '^[A-Z][A-Z0-9_]{0,63}$'), parent_id uuid,
 name_en text NOT NULL CHECK(char_length(name_en) BETWEEN 1 AND 128),
 name_ta text NOT NULL CHECK(char_length(name_ta) BETWEEN 1 AND 128),
 active boolean NOT NULL, revision bigint NOT NULL CHECK(revision>0),
 PRIMARY KEY(tenant_id,country,id), UNIQUE(tenant_id,country,kind,code),
 FOREIGN KEY(tenant_id,country,parent_id) REFERENCES admin.master_data(tenant_id,country,id),
 CHECK(id<>parent_id),
 CHECK((kind IN ('COUNTRY','OCCUPATION','REASON') AND parent_id IS NULL) OR (kind IN ('STATE','DISTRICT','CITY','AREA') AND parent_id IS NOT NULL)),
 CHECK(kind<>'COUNTRY' OR code=country)
);
CREATE INDEX master_data_list_idx ON admin.master_data(tenant_id,country,kind,id);
CREATE INDEX master_data_parent_idx ON admin.master_data(tenant_id,country,parent_id);
CREATE TABLE admin.master_data_versions (
 tenant_id uuid NOT NULL,country char(2) NOT NULL,id uuid NOT NULL,revision bigint NOT NULL,
 definition jsonb NOT NULL, actor_id uuid NOT NULL,reason text NOT NULL CHECK(char_length(reason) BETWEEN 8 AND 500),
 occurred_at timestamptz NOT NULL,
 PRIMARY KEY(tenant_id,country,id,revision),
 FOREIGN KEY(tenant_id,country,id) REFERENCES admin.master_data(tenant_id,country,id)
);
-- Stable identifiers and append-only evidence preserve existing references.
-- Country labels do not alter currency, tax, launch or allowed-country controls.
GRANT SELECT,INSERT,UPDATE ON admin.master_data TO planext4u_admin_runtime;
GRANT SELECT,INSERT ON admin.master_data_versions TO planext4u_admin_runtime;
RESET ROLE;
COMMIT;
