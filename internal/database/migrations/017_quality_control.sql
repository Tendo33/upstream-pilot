ALTER TABLE probe_attempts ADD COLUMN first_content_ms integer CHECK (first_content_ms >= 0);
ALTER TABLE probe_attempts ADD COLUMN duration_ms integer CHECK (duration_ms >= 0);
ALTER TABLE probe_attempts ADD COLUMN actual_model text;
ALTER TABLE probe_attempts ADD COLUMN stream_complete boolean NOT NULL DEFAULT false;

CREATE TABLE quality_policies (
 account_id uuid PRIMARY KEY REFERENCES upstream_accounts(id) ON DELETE CASCADE,
 config jsonb NOT NULL DEFAULT '{}'::jsonb,
 updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE quality_states (
 account_id uuid PRIMARY KEY REFERENCES upstream_accounts(id) ON DELETE CASCADE,
 baseline_priority integer NOT NULL,
 last_applied_priority integer,
 pending_priority integer,
 desired_priority integer NOT NULL,
 tier integer NOT NULL DEFAULT 0,
 recovery_streak integer NOT NULL DEFAULT 0,
 last_sample_at timestamptz,
 last_changed_at timestamptz,
 status text NOT NULL DEFAULT 'unknown',
 reason text NOT NULL DEFAULT '等待探测',
 conflict boolean NOT NULL DEFAULT false,
 owned_pause boolean NOT NULL DEFAULT false,
 evaluated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE quality_decisions (
 id uuid PRIMARY KEY,
 account_id uuid NOT NULL REFERENCES upstream_accounts(id) ON DELETE CASCADE,
 owner_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 mode text NOT NULL,
 status text NOT NULL,
 reason text NOT NULL,
 before_priority integer NOT NULL,
 desired_priority integer NOT NULL,
 applied boolean NOT NULL DEFAULT false,
 error text NOT NULL DEFAULT '',
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX quality_decisions_account_created ON quality_decisions(account_id,created_at DESC);
CREATE TABLE upstream_price_history (
 id uuid PRIMARY KEY,
 account_id uuid NOT NULL REFERENCES upstream_accounts(id) ON DELETE CASCADE,
 source_rate double precision NOT NULL,
 effective_rate double precision NOT NULL,
 endpoint text NOT NULL DEFAULT '',
 checked_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX upstream_price_history_account_created ON upstream_price_history(account_id,checked_at DESC);
CREATE TABLE quality_traffic (
 account_id uuid PRIMARY KEY REFERENCES upstream_accounts(id) ON DELETE CASCADE,
 snapshot jsonb NOT NULL,
 checked_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE quality_alert_settings (
 owner_id uuid PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
 enabled boolean NOT NULL DEFAULT false,
 webhook_ciphertext text,
 updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE quality_notifications (
 id uuid PRIMARY KEY,
 owner_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 account_id uuid REFERENCES upstream_accounts(id) ON DELETE CASCADE,
 kind text NOT NULL,
 message text NOT NULL,
 attempts integer NOT NULL DEFAULT 0,
 next_attempt_at timestamptz NOT NULL DEFAULT now(),
 lease_until timestamptz,
 delivered_at timestamptz,
 last_error text NOT NULL DEFAULT '',
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX quality_notifications_due ON quality_notifications(next_attempt_at) WHERE delivered_at IS NULL;

-- The quality controller owns priority; collectors no longer write rates or pause.
ALTER TABLE upstream_accounts ALTER COLUMN probe_interval_seconds SET DEFAULT 120;
ALTER TABLE upstream_accounts ALTER COLUMN probe_timeout_seconds SET DEFAULT 45;
ALTER TABLE upstream_accounts ALTER COLUMN failure_threshold SET DEFAULT 3;

ALTER TABLE upstream_accounts ADD COLUMN observed_cost_rate double precision;
