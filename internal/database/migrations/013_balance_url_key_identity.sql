ALTER TABLE upstream_accounts
  ADD COLUMN IF NOT EXISTS observed_source_credential_fingerprint text CHECK (
    observed_source_credential_fingerprint IS NULL OR
    observed_source_credential_fingerprint ~ '^[0-9a-f]{64}$'
  );
