SET ROLE planext4u_social_owner;

REVOKE SELECT, UPDATE ON social.posts FROM planext4u_admin_runtime;
REVOKE SELECT, INSERT, UPDATE ON social.policies FROM planext4u_admin_runtime;
REVOKE USAGE ON SCHEMA social FROM planext4u_admin_runtime;
DROP INDEX social.social_reward_claim_idx;
ALTER TABLE social.reward_events DROP COLUMN lease_until, DROP COLUMN lease_owner;
ALTER TABLE social.posts DROP COLUMN share_count;

RESET ROLE;
