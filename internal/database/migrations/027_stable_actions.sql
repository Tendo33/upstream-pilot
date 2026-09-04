ALTER TABLE quality_states ADD COLUMN last_control_applied_at timestamptz;
CREATE TABLE engine_actions (
 id uuid PRIMARY KEY,
 plan_id text NOT NULL,
 account_id uuid NOT NULL REFERENCES upstream_accounts(id) ON DELETE CASCADE,
 owner_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 source_generation bigint NOT NULL,
 config_generation bigint NOT NULL,
 control_scope text NOT NULL,
 before_values jsonb NOT NULL,
 after_values jsonb NOT NULL,
 pools jsonb NOT NULL DEFAULT '[]',
 before_sli jsonb NOT NULL DEFAULT '[]',
 after_sli jsonb NOT NULL DEFAULT '[]',
 effect_status text NOT NULL DEFAULT 'unverified',
 effect_reason text NOT NULL DEFAULT '等待观察窗口',
 window_seconds integer NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(),
 checked_at timestamptz
);
CREATE INDEX engine_actions_owner_time ON engine_actions(owner_id,created_at DESC);
CREATE INDEX engine_actions_pending ON engine_actions(created_at) WHERE checked_at IS NULL;
