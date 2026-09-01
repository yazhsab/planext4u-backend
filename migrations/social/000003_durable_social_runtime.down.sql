SET ROLE planext4u_social_owner;

ALTER TABLE social.media_jobs DROP COLUMN IF EXISTS updated_at;
DROP INDEX IF EXISTS social.social_conversation_pair_unique;
DROP TABLE IF EXISTS social.reward_events;
DROP TABLE IF EXISTS social.post_shares;
DROP TABLE IF EXISTS social.call_signals;
DROP TABLE IF EXISTS social.presence;
DROP TABLE IF EXISTS social.policies;

RESET ROLE;
