CREATE TABLE IF NOT EXISTS schema_migrations (
  version text PRIMARY KEY,
  applied_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE users (
  id uuid PRIMARY KEY,
  email text NOT NULL,
  password_hash text NOT NULL,
  role text NOT NULL CHECK (role IN ('admin', 'user')),
  enabled boolean NOT NULL DEFAULT true,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  last_login_at timestamptz
);
CREATE UNIQUE INDEX users_email_unique ON users (lower(email));

CREATE TABLE sessions (
  id uuid PRIMARY KEY,
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  token_hash bytea NOT NULL UNIQUE,
  csrf_hash bytea NOT NULL,
  expires_at timestamptz NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  last_seen_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX sessions_user_id_idx ON sessions(user_id);
CREATE INDEX sessions_expires_at_idx ON sessions(expires_at);

CREATE TABLE sites (
  id uuid PRIMARY KEY,
  owner_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name text NOT NULL,
  base_url text NOT NULL,
  api_key_ciphertext text NOT NULL,
  enabled boolean NOT NULL DEFAULT true,
  connection_state text NOT NULL DEFAULT 'unknown' CHECK (connection_state IN ('unknown', 'healthy', 'unreachable', 'auth_error')),
  last_error text,
  version_hint text,
  inventory_interval_seconds integer NOT NULL DEFAULT 300 CHECK (inventory_interval_seconds BETWEEN 30 AND 86400),
  priority_start integer NOT NULL DEFAULT 1 CHECK (priority_start BETWEEN 0 AND 1000000),
  priority_step integer NOT NULL DEFAULT 1 CHECK (priority_step BETWEEN 1 AND 100000),
  reconcile_interval_seconds integer NOT NULL DEFAULT 60 CHECK (reconcile_interval_seconds BETWEEN 10 AND 86400),
  next_inventory_at timestamptz NOT NULL DEFAULT now(),
  next_reconcile_at timestamptz NOT NULL DEFAULT now(),
  inventory_lease_until timestamptz,
  reconcile_lease_until timestamptz,
  last_inventory_at timestamptz,
  last_reconcile_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE(owner_id, base_url)
);
CREATE INDEX sites_owner_id_idx ON sites(owner_id);
CREATE INDEX sites_inventory_due_idx ON sites(next_inventory_at) WHERE enabled;
CREATE INDEX sites_reconcile_due_idx ON sites(next_reconcile_at) WHERE enabled;

CREATE TABLE upstream_groups (
  id uuid PRIMARY KEY,
  site_id uuid NOT NULL REFERENCES sites(id) ON DELETE CASCADE,
  remote_id bigint NOT NULL,
  name text NOT NULL,
  platform text,
  status text,
  rate_multiplier numeric(20,8),
  deleted_at timestamptz,
  observed_at timestamptz NOT NULL DEFAULT now(),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE(site_id, remote_id)
);
CREATE INDEX upstream_groups_site_idx ON upstream_groups(site_id, deleted_at);

CREATE TABLE upstream_accounts (
  id uuid PRIMARY KEY,
  site_id uuid NOT NULL REFERENCES sites(id) ON DELETE CASCADE,
  remote_id bigint NOT NULL,
  name text NOT NULL,
  platform text NOT NULL DEFAULT '',
  account_type text NOT NULL DEFAULT '',
  remote_status text NOT NULL DEFAULT '',
  schedulable boolean NOT NULL DEFAULT false,
  priority integer NOT NULL DEFAULT 0,
  rate_multiplier numeric(20,8),
  remote_updated_at timestamptz,
  deleted_at timestamptz,
  observed_at timestamptz NOT NULL DEFAULT now(),

  health_enabled boolean NOT NULL DEFAULT false,
  probe_interval_seconds integer NOT NULL DEFAULT 30 CHECK (probe_interval_seconds BETWEEN 10 AND 86400),
  probe_timeout_seconds integer NOT NULL DEFAULT 7 CHECK (probe_timeout_seconds BETWEEN 3 AND 600),
  failure_threshold integer NOT NULL DEFAULT 2 CHECK (failure_threshold BETWEEN 1 AND 100),
  recovery_success_threshold integer NOT NULL DEFAULT 1 CHECK (recovery_success_threshold BETWEEN 1 AND 100),
  probe_model text,

  rate_sync_enabled boolean NOT NULL DEFAULT false,
  rate_sync_interval_seconds integer NOT NULL DEFAULT 30 CHECK (rate_sync_interval_seconds BETWEEN 30 AND 604800),
  source_type text NOT NULL DEFAULT 'sub2api' CHECK (source_type IN ('sub2api', 'newapi')),
  source_type_locked boolean NOT NULL DEFAULT false,
  source_base_url text,
  source_credential_ciphertext text,
  source_credential_state text NOT NULL DEFAULT 'unknown' CHECK (source_credential_state IN ('unknown', 'valid', 'invalid')),
  source_credential_checked_at timestamptz,
  source_user_id text,
  source_group text,
  observed_source_base_url text CHECK (
    observed_source_base_url IS NULL OR (
      char_length(observed_source_base_url) BETWEEN 1 AND 2048 AND
      observed_source_base_url ~ '^https?://'
    )
  ),
  observed_source_credential_fingerprint text CHECK (
    observed_source_credential_fingerprint IS NULL OR
    observed_source_credential_fingerprint ~ '^[0-9a-f]{64}$'
  ),
  recharge_ratio numeric(20,8) NOT NULL DEFAULT 1 CHECK (recharge_ratio > 0),
  source_rate_multiplier numeric(20,8),
  source_rate_endpoint text,

  priority_enabled boolean NOT NULL DEFAULT false,
  guard_enabled boolean NOT NULL DEFAULT false,
  guard_operator text NOT NULL DEFAULT 'gte' CHECK (guard_operator IN ('gt', 'gte')),
  guard_priority integer NOT NULL DEFAULT 999 CHECK (guard_priority BETWEEN 0 AND 1000000),
  guard_holding boolean NOT NULL DEFAULT false,
  guard_restore_priority integer,

  health_state text NOT NULL DEFAULT 'unknown' CHECK (health_state IN ('unknown', 'healthy', 'failing', 'paused')),
  consecutive_failures integer NOT NULL DEFAULT 0,
  consecutive_recovery_successes integer NOT NULL DEFAULT 0 CHECK (consecutive_recovery_successes >= 0),
  managed_hold boolean NOT NULL DEFAULT false,
  probe_sequence bigint NOT NULL DEFAULT 0 CHECK (probe_sequence >= 0),
  applied_probe_sequence bigint NOT NULL DEFAULT 0 CHECK (applied_probe_sequence >= 0 AND applied_probe_sequence <= probe_sequence),
  scheduling_generation bigint NOT NULL DEFAULT 0 CHECK (scheduling_generation >= 0),
  next_probe_at timestamptz NOT NULL DEFAULT now(),
  next_rate_sync_at timestamptz NOT NULL DEFAULT now(),
  work_lease_until timestamptz,
  last_probe_at timestamptz,
  last_probe_latency_ms integer,
  last_success_at timestamptz,
  last_failure_at timestamptz,
  last_failure_reason text CHECK (last_failure_reason IN ('AUTH', 'BALANCE', 'RATE_LIMIT', 'UPSTREAM', 'TIMEOUT', 'CONFIGURATION', 'UNKNOWN')),
  last_failure_http_status integer CHECK (last_failure_http_status BETWEEN 100 AND 599),
  last_rate_sync_at timestamptz,
  last_error text,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE(site_id, remote_id)
);
CREATE INDEX upstream_accounts_site_idx ON upstream_accounts(site_id, deleted_at);
CREATE INDEX upstream_accounts_probe_due_idx ON upstream_accounts(next_probe_at) WHERE health_enabled AND deleted_at IS NULL;
CREATE INDEX upstream_accounts_rate_due_idx ON upstream_accounts(next_rate_sync_at) WHERE rate_sync_enabled AND deleted_at IS NULL;

CREATE TABLE account_group_memberships (
  account_id uuid NOT NULL REFERENCES upstream_accounts(id) ON DELETE CASCADE,
  group_id uuid NOT NULL REFERENCES upstream_groups(id) ON DELETE CASCADE,
  group_priority integer,
  PRIMARY KEY(account_id, group_id)
);
CREATE INDEX account_group_memberships_group_idx ON account_group_memberships(group_id);

CREATE TABLE probe_attempts (
  id uuid PRIMARY KEY,
  owner_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  site_id uuid NOT NULL REFERENCES sites(id) ON DELETE CASCADE,
  account_id uuid NOT NULL REFERENCES upstream_accounts(id) ON DELETE CASCADE,
  kind text NOT NULL CHECK (kind IN ('scheduled', 'manual')),
  success boolean NOT NULL,
  latency_ms integer,
  model text,
  message text,
  failure_reason text CHECK (failure_reason IN ('AUTH', 'BALANCE', 'RATE_LIMIT', 'UPSTREAM', 'TIMEOUT', 'CONFIGURATION', 'UNKNOWN')),
  http_status integer CHECK (http_status BETWEEN 100 AND 599),
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX probe_attempts_account_created_idx ON probe_attempts(account_id, created_at DESC);
CREATE INDEX probe_attempts_owner_created_idx ON probe_attempts(owner_id, created_at DESC);

CREATE TABLE audit_events (
  id uuid PRIMARY KEY,
  owner_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  actor_user_id uuid REFERENCES users(id) ON DELETE SET NULL,
  site_id uuid REFERENCES sites(id) ON DELETE CASCADE,
  account_id uuid REFERENCES upstream_accounts(id) ON DELETE CASCADE,
  action text NOT NULL,
  outcome text NOT NULL CHECK (outcome IN ('success', 'failed', 'skipped')),
  detail jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX audit_events_owner_created_idx ON audit_events(owner_id, created_at DESC);
CREATE INDEX audit_events_site_created_idx ON audit_events(site_id, created_at DESC);
