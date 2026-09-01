SET ROLE planext4u_wallet_owner;
DROP INDEX IF EXISTS wallet.wallet_reward_device_time_idx;
DROP TABLE IF EXISTS wallet.referral_requests;
DROP TABLE IF EXISTS wallet.referral_applications;
DROP TABLE IF EXISTS wallet.referral_codes;
DROP TABLE IF EXISTS wallet.debit_allocations;
DROP INDEX IF EXISTS wallet.wallet_ledger_sequence_idx;
ALTER TABLE wallet.ledger_entries DROP COLUMN IF EXISTS sequence_id;
RESET ROLE;
