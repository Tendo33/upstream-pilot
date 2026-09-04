ALTER TABLE upstream_accounts
  ADD COLUMN IF NOT EXISTS recovery_success_threshold integer NOT NULL DEFAULT 1,
  ADD COLUMN IF NOT EXISTS consecutive_recovery_successes integer NOT NULL DEFAULT 0;

DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint
    WHERE conrelid = 'upstream_accounts'::regclass
      AND conname = 'upstream_accounts_recovery_success_threshold_check'
  ) THEN
    ALTER TABLE upstream_accounts
      ADD CONSTRAINT upstream_accounts_recovery_success_threshold_check
        CHECK (recovery_success_threshold BETWEEN 1 AND 100);
  END IF;

  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint
    WHERE conrelid = 'upstream_accounts'::regclass
      AND conname = 'upstream_accounts_consecutive_recovery_successes_check'
  ) THEN
    ALTER TABLE upstream_accounts
      ADD CONSTRAINT upstream_accounts_consecutive_recovery_successes_check
        CHECK (consecutive_recovery_successes >= 0);
  END IF;
END $$;
