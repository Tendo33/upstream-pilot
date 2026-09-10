ALTER TABLE sites
  ADD COLUMN IF NOT EXISTS database_url_ciphertext text NOT NULL DEFAULT '';
