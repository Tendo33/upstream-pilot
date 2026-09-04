ALTER TABLE upstream_accounts
  ADD COLUMN IF NOT EXISTS observed_source_base_url text CHECK (
    observed_source_base_url IS NULL OR (
      char_length(observed_source_base_url) BETWEEN 1 AND 2048 AND
      observed_source_base_url ~ '^https?://'
    )
  );
