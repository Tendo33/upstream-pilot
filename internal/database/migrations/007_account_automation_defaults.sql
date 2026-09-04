ALTER TABLE upstream_accounts
  ALTER COLUMN probe_interval_seconds SET DEFAULT 30,
  ALTER COLUMN probe_timeout_seconds SET DEFAULT 7,
  ALTER COLUMN failure_threshold SET DEFAULT 2,
  ALTER COLUMN rate_sync_interval_seconds SET DEFAULT 30;

-- Only move disabled automation settings that still equal the original defaults.
-- Each field is handled independently so one customized value does not prevent
-- the remaining untouched values from receiving the new defaults.
UPDATE upstream_accounts
SET probe_interval_seconds = CASE WHEN probe_interval_seconds = 300 THEN 30 ELSE probe_interval_seconds END,
    probe_timeout_seconds = CASE WHEN probe_timeout_seconds = 60 THEN 7 ELSE probe_timeout_seconds END,
    failure_threshold = CASE WHEN failure_threshold = 3 THEN 2 ELSE failure_threshold END,
    probe_model = CASE
      WHEN probe_model IS NULL AND lower(btrim(platform)) = 'openai' THEN 'gpt-5.5'
      ELSE probe_model
    END,
    updated_at = now()
WHERE health_enabled = false
  AND managed_hold = false
  AND (
    probe_interval_seconds = 300 OR
    probe_timeout_seconds = 60 OR
    failure_threshold = 3 OR
    (probe_model IS NULL AND lower(btrim(platform)) = 'openai')
  );

UPDATE upstream_accounts
SET rate_sync_interval_seconds = 30,
    updated_at = now()
WHERE rate_sync_enabled = false
  AND rate_sync_interval_seconds = 1800;
