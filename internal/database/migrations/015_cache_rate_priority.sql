ALTER TABLE sites
  ADD COLUMN IF NOT EXISTS cache_rate_priority_enabled boolean NOT NULL DEFAULT false,
  ADD COLUMN IF NOT EXISTS cache_rate_window_seconds integer NOT NULL DEFAULT 3600,
  ADD COLUMN IF NOT EXISTS rate_priority_weight numeric(12,4) NOT NULL DEFAULT 1,
  ADD COLUMN IF NOT EXISTS cache_rate_priority_weight numeric(12,4) NOT NULL DEFAULT 1,
  ADD COLUMN IF NOT EXISTS next_cache_sample_at timestamptz NOT NULL DEFAULT now(),
  ADD COLUMN IF NOT EXISTS cache_sample_lease_until timestamptz,
  ADD COLUMN IF NOT EXISTS last_cache_sample_at timestamptz;

ALTER TABLE upstream_accounts
  ADD COLUMN IF NOT EXISTS cache_rate numeric(12,8),
  ADD COLUMN IF NOT EXISTS cache_rate_tokens bigint NOT NULL DEFAULT 0,
  ADD COLUMN IF NOT EXISTS cache_rate_sampled_at timestamptz;

CREATE TABLE IF NOT EXISTS account_cache_samples (
  id bigserial PRIMARY KEY,
  account_id uuid NOT NULL REFERENCES upstream_accounts(id) ON DELETE CASCADE,
  site_id uuid NOT NULL REFERENCES sites(id) ON DELETE CASCADE,
  sampled_at timestamptz NOT NULL DEFAULT now(),
  input_tokens bigint NOT NULL DEFAULT 0,
  cache_creation_tokens bigint NOT NULL DEFAULT 0,
  cache_read_tokens bigint NOT NULL DEFAULT 0
);

DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint
    WHERE conrelid = 'sites'::regclass
      AND conname = 'sites_cache_rate_window_seconds_check'
  ) THEN
    ALTER TABLE sites
      ADD CONSTRAINT sites_cache_rate_window_seconds_check
        CHECK (cache_rate_window_seconds BETWEEN 300 AND 86400);
  END IF;

  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint
    WHERE conrelid = 'sites'::regclass
      AND conname = 'sites_rate_priority_weight_check'
  ) THEN
    ALTER TABLE sites
      ADD CONSTRAINT sites_rate_priority_weight_check
        CHECK (rate_priority_weight >= 0 AND rate_priority_weight <= 100);
  END IF;

  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint
    WHERE conrelid = 'sites'::regclass
      AND conname = 'sites_cache_rate_priority_weight_check'
  ) THEN
    ALTER TABLE sites
      ADD CONSTRAINT sites_cache_rate_priority_weight_check
        CHECK (cache_rate_priority_weight >= 0 AND cache_rate_priority_weight <= 100);
  END IF;

  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint
    WHERE conrelid = 'upstream_accounts'::regclass
      AND conname = 'upstream_accounts_cache_rate_check'
  ) THEN
    ALTER TABLE upstream_accounts
      ADD CONSTRAINT upstream_accounts_cache_rate_check
        CHECK (cache_rate IS NULL OR (cache_rate >= 0 AND cache_rate <= 1));
  END IF;

  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint
    WHERE conrelid = 'upstream_accounts'::regclass
      AND conname = 'upstream_accounts_cache_rate_tokens_check'
  ) THEN
    ALTER TABLE upstream_accounts
      ADD CONSTRAINT upstream_accounts_cache_rate_tokens_check
        CHECK (cache_rate_tokens >= 0);
  END IF;

  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint
    WHERE conrelid = 'account_cache_samples'::regclass
      AND conname = 'account_cache_samples_input_tokens_check'
  ) THEN
    ALTER TABLE account_cache_samples
      ADD CONSTRAINT account_cache_samples_input_tokens_check
        CHECK (input_tokens >= 0);
  END IF;

  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint
    WHERE conrelid = 'account_cache_samples'::regclass
      AND conname = 'account_cache_samples_cache_creation_tokens_check'
  ) THEN
    ALTER TABLE account_cache_samples
      ADD CONSTRAINT account_cache_samples_cache_creation_tokens_check
        CHECK (cache_creation_tokens >= 0);
  END IF;

  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint
    WHERE conrelid = 'account_cache_samples'::regclass
      AND conname = 'account_cache_samples_cache_read_tokens_check'
  ) THEN
    ALTER TABLE account_cache_samples
      ADD CONSTRAINT account_cache_samples_cache_read_tokens_check
        CHECK (cache_read_tokens >= 0);
  END IF;
END $$;

CREATE INDEX IF NOT EXISTS account_cache_samples_account_time_idx
  ON account_cache_samples(account_id, sampled_at DESC);
CREATE INDEX IF NOT EXISTS account_cache_samples_site_time_idx
  ON account_cache_samples(site_id, sampled_at);
CREATE INDEX IF NOT EXISTS sites_cache_sample_due_idx
  ON sites(next_cache_sample_at)
  WHERE enabled AND cache_rate_priority_enabled;
