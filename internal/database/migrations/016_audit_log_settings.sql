CREATE TABLE IF NOT EXISTS audit_log_settings (
  owner_id uuid PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  retention_days integer NOT NULL DEFAULT 14 CHECK (retention_days BETWEEN 1 AND 365),
  last_purged_at timestamptz,
  last_purge_removed_files integer NOT NULL DEFAULT 0,
  last_purge_removed_records integer NOT NULL DEFAULT 0,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
