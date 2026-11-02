SET ROLE planext4u_supply_owner;
CREATE TABLE supply.application_submissions (
 application_id uuid NOT NULL REFERENCES supply.vendor_applications(id),
 revision bigint NOT NULL CHECK(revision>0),
 documents jsonb NOT NULL CHECK(jsonb_typeof(documents)='array'),
 submitted_by uuid NOT NULL,
 submitted_at timestamptz NOT NULL,
 PRIMARY KEY(application_id,revision)
);
INSERT INTO supply.application_submissions(application_id,revision,documents,submitted_by,submitted_at)
 SELECT a.id,a.revision,jsonb_agg(jsonb_build_object('kind',d.kind,'asset_id',d.private_asset_id,'ocr_status',d.ocr_status)),a.vendor_identity_id,a.updated_at
 FROM supply.vendor_applications a JOIN supply.vendor_documents d ON d.application_id=a.id
 GROUP BY a.id;
GRANT SELECT,INSERT ON supply.application_submissions TO planext4u_supply_runtime;
RESET ROLE;
