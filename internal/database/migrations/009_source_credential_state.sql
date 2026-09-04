ALTER TABLE upstream_accounts
  ADD COLUMN IF NOT EXISTS source_credential_state text NOT NULL DEFAULT 'unknown'
    CHECK (source_credential_state IN ('unknown', 'valid', 'invalid')),
  ADD COLUMN IF NOT EXISTS source_credential_checked_at timestamptz;
