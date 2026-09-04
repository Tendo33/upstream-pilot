ALTER TABLE quality_states ADD COLUMN controller_error text NOT NULL DEFAULT '';
ALTER TABLE quality_states ADD COLUMN controller_error_at timestamptz;
ALTER TABLE quality_states ADD COLUMN pending_since timestamptz;
UPDATE quality_states SET pending_since=evaluated_at WHERE pending_priority IS NOT NULL OR COALESCE(pending_control->'to','{}')<>'{}';
CREATE TABLE operational_incidents (
 account_id uuid NOT NULL REFERENCES upstream_accounts(id) ON DELETE CASCADE,
 channel text NOT NULL,
 owner_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 source_generation bigint NOT NULL,
 episode bigint NOT NULL DEFAULT 0,
 active boolean NOT NULL DEFAULT false,
 message text NOT NULL DEFAULT '',
 opened_at timestamptz,
 checked_at timestamptz NOT NULL DEFAULT now(),
 resolved_at timestamptz,
 PRIMARY KEY(account_id,channel)
);
CREATE INDEX operational_incidents_owner_active ON operational_incidents(owner_id,active,checked_at DESC);
