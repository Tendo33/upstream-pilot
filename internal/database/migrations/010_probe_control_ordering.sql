ALTER TABLE upstream_accounts
  ADD COLUMN IF NOT EXISTS probe_sequence bigint NOT NULL DEFAULT 0 CHECK (probe_sequence >= 0),
  ADD COLUMN IF NOT EXISTS applied_probe_sequence bigint NOT NULL DEFAULT 0,
  ADD COLUMN IF NOT EXISTS scheduling_generation bigint NOT NULL DEFAULT 0 CHECK (scheduling_generation >= 0);

DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1
    FROM pg_constraint
    WHERE conrelid = 'upstream_accounts'::regclass
      AND conname = 'upstream_accounts_applied_probe_sequence_check'
  ) THEN
    ALTER TABLE upstream_accounts
      ADD CONSTRAINT upstream_accounts_applied_probe_sequence_check
        CHECK (applied_probe_sequence >= 0 AND applied_probe_sequence <= probe_sequence);
  END IF;
END
$$;
