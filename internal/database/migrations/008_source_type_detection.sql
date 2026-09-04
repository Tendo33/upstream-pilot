ALTER TABLE upstream_accounts
  ADD COLUMN IF NOT EXISTS source_type_locked boolean NOT NULL DEFAULT false;

-- Existing NewAPI credentials and overrides are operator-owned configuration.
UPDATE upstream_accounts
SET source_type_locked = true,
    updated_at = now()
WHERE source_type = 'newapi'
  AND (
    source_base_url IS NOT NULL OR
    source_credential_ciphertext IS NOT NULL OR
    source_user_id IS NOT NULL OR
    source_group IS NOT NULL
  );
