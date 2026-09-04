CREATE TABLE balance_alert_settings (
  owner_id uuid PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  enabled boolean NOT NULL DEFAULT false,
  threshold double precision NOT NULL DEFAULT 10 CHECK (threshold >= 0 AND threshold <= 1000000000000),
  cooldown_seconds integer NOT NULL DEFAULT 21600 CHECK (cooldown_seconds BETWEEN 300 AND 2592000),
  webhook_url_ciphertext text,
  cooldown_until timestamptz,
  last_attempt_at timestamptz,
  last_notified_at timestamptz,
  last_error text,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX balance_alert_settings_due_idx
  ON balance_alert_settings(cooldown_until)
  WHERE enabled AND webhook_url_ciphertext IS NOT NULL;
