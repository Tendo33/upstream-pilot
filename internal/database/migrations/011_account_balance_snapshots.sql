CREATE TABLE account_balance_snapshots (
  account_id uuid PRIMARY KEY REFERENCES upstream_accounts(id) ON DELETE CASCADE,
  cache_key text NOT NULL CHECK (char_length(cache_key) BETWEEN 1 AND 4096),
  status text NOT NULL CHECK (status IN ('ok', 'unsupported', 'invalid', 'error')),
  provider text NOT NULL DEFAULT '',
  plan_name text NOT NULL DEFAULT '',
  remaining double precision,
  used double precision,
  total double precision,
  unit text NOT NULL DEFAULT '',
  message text NOT NULL DEFAULT '',
  endpoint text NOT NULL DEFAULT '',
  checked_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX account_balance_snapshots_cache_key_idx
  ON account_balance_snapshots(cache_key, checked_at DESC);
