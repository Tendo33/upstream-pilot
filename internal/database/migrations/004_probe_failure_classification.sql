ALTER TABLE upstream_accounts
  ADD COLUMN IF NOT EXISTS last_failure_reason text
    CHECK (last_failure_reason IN ('AUTH', 'BALANCE', 'RATE_LIMIT', 'UPSTREAM', 'TIMEOUT', 'CONFIGURATION', 'UNKNOWN')),
  ADD COLUMN IF NOT EXISTS last_failure_http_status integer
    CHECK (last_failure_http_status BETWEEN 100 AND 599);

ALTER TABLE probe_attempts
  ADD COLUMN IF NOT EXISTS failure_reason text
    CHECK (failure_reason IN ('AUTH', 'BALANCE', 'RATE_LIMIT', 'UPSTREAM', 'TIMEOUT', 'CONFIGURATION', 'UNKNOWN')),
  ADD COLUMN IF NOT EXISTS http_status integer
    CHECK (http_status BETWEEN 100 AND 599);
