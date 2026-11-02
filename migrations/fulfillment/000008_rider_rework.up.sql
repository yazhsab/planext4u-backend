SET ROLE planext4u_fulfillment_owner;
ALTER TABLE fulfillment.rider_profiles
 ADD COLUMN review_reason text NOT NULL DEFAULT '',
 ADD COLUMN reviewed_at timestamptz,
 ADD COLUMN rework_allowed boolean NOT NULL DEFAULT false,
 ADD CONSTRAINT rider_rework_only_suspended CHECK(NOT rework_allowed OR status='SUSPENDED');
CREATE TABLE fulfillment.rider_submissions (
 profile_id uuid NOT NULL REFERENCES fulfillment.rider_profiles(id),
 revision bigint NOT NULL CHECK(revision>0),
 documents jsonb NOT NULL CHECK(jsonb_typeof(documents)='array'),
 submitted_by uuid NOT NULL,
 submitted_at timestamptz NOT NULL,
 PRIMARY KEY(profile_id,revision)
);
INSERT INTO fulfillment.rider_submissions(profile_id,revision,documents,submitted_by,submitted_at)
 SELECT id,revision,documents,rider_identity_id,updated_at FROM fulfillment.rider_profiles WHERE jsonb_array_length(documents)>0;
GRANT SELECT,INSERT ON fulfillment.rider_submissions TO planext4u_fulfillment_runtime;
RESET ROLE;
