CREATE TABLE auth_throttles (
  key_hash bytea PRIMARY KEY,
  failures integer NOT NULL DEFAULT 0,
  window_started_at timestamptz NOT NULL DEFAULT now(),
  blocked_until timestamptz,
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX auth_throttles_updated_idx ON auth_throttles(updated_at);
