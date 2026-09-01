SET ROLE planext4u_social_owner;

ALTER TABLE social.posts ADD COLUMN share_count integer NOT NULL DEFAULT 0 CHECK (share_count >= 0);
ALTER TABLE social.reward_events
    ADD COLUMN lease_owner text,
    ADD COLUMN lease_until timestamptz;
CREATE INDEX social_reward_claim_idx ON social.reward_events (status, next_attempt_at, lease_until, created_at);

GRANT USAGE ON SCHEMA social TO planext4u_admin_runtime;
GRANT SELECT, INSERT, UPDATE ON social.policies TO planext4u_admin_runtime;
GRANT SELECT, UPDATE ON social.posts TO planext4u_admin_runtime;

RESET ROLE;
