-- A source generation is independent of config_generation (inventory refreshes).
-- Legacy evidence has no proven lineage: retain it for history, exclude it from
-- new decisions until each collector has run against the current source.
ALTER TABLE upstream_accounts ADD COLUMN source_generation bigint NOT NULL DEFAULT 0;
ALTER TABLE upstream_accounts ADD COLUMN source_mapping_fingerprint text NOT NULL DEFAULT '';
ALTER TABLE upstream_accounts ADD COLUMN native_constraints jsonb NOT NULL DEFAULT '{}';
ALTER TABLE upstream_accounts ADD COLUMN native_checked_at timestamptz;
ALTER TABLE upstream_accounts ADD COLUMN price_source_generation bigint NOT NULL DEFAULT -1;
UPDATE upstream_accounts SET source_generation=1,price_status='unknown';
ALTER TABLE probe_attempts ADD COLUMN source_generation bigint NOT NULL DEFAULT 0;
ALTER TABLE upstream_price_history ADD COLUMN source_generation bigint NOT NULL DEFAULT 0;
ALTER TABLE account_balance_snapshots ADD COLUMN source_generation bigint NOT NULL DEFAULT 0;
ALTER TABLE quality_traffic ADD COLUMN source_generation bigint NOT NULL DEFAULT 0;
ALTER TABLE quality_states ADD COLUMN source_generation bigint NOT NULL DEFAULT 0;
CREATE INDEX probe_attempts_source_window ON probe_attempts(account_id,source_generation,created_at DESC);
CREATE TABLE balance_observations (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 account_id uuid NOT NULL REFERENCES upstream_accounts(id) ON DELETE CASCADE,
 source_generation bigint NOT NULL,
 status text NOT NULL,
 remaining double precision,
 used double precision,
 total double precision,
 unit text NOT NULL,
 checked_at timestamptz NOT NULL
);
CREATE INDEX balance_observations_source_window ON balance_observations(account_id,source_generation,checked_at DESC);
INSERT INTO balance_observations(account_id,source_generation,status,remaining,used,total,unit,checked_at)
SELECT account_id,source_generation,status,remaining,used,total,unit,checked_at FROM account_balance_snapshots;

CREATE FUNCTION pilot_source_changed() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF ROW(NEW.site_id,NEW.remote_id,NEW.platform,NEW.account_type,NEW.source_type,
        NEW.source_base_url,NEW.source_credential_ciphertext,NEW.source_user_id,NEW.source_group,
        NEW.observed_source_base_url,NEW.observed_source_credential_fingerprint,
        NEW.source_mapping_fingerprint,NEW.probe_model,NEW.recharge_ratio,NEW.deleted_at)
 IS DISTINCT FROM
    ROW(OLD.site_id,OLD.remote_id,OLD.platform,OLD.account_type,OLD.source_type,
        OLD.source_base_url,OLD.source_credential_ciphertext,OLD.source_user_id,OLD.source_group,
        OLD.observed_source_base_url,OLD.observed_source_credential_fingerprint,
        OLD.source_mapping_fingerprint,OLD.probe_model,OLD.recharge_ratio,OLD.deleted_at)
 OR NEW.source_generation IS DISTINCT FROM OLD.source_generation THEN
   NEW.source_generation := OLD.source_generation+1;
   NEW.observed_cost_rate := NULL;
   NEW.price_reference_rate := NULL;
   NEW.price_status := 'unknown';
   NEW.price_source_generation := -1;
   NEW.last_rate_sync_at := NULL;
   NEW.health_state := 'unknown';
   NEW.last_probe_at := NULL;
   NEW.consecutive_failures := 0;
   NEW.consecutive_recovery_successes := 0;
   NEW.last_failure_reason := NULL;
   NEW.next_probe_at := now();
   NEW.next_rate_sync_at := now();
   NEW.next_traffic_at := now();
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER pilot_source_generation BEFORE UPDATE ON upstream_accounts
FOR EACH ROW EXECUTE FUNCTION pilot_source_changed();

CREATE FUNCTION pilot_site_source_changed() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF ROW(NEW.base_url,NEW.api_key_ciphertext) IS DISTINCT FROM ROW(OLD.base_url,OLD.api_key_ciphertext) THEN
   UPDATE upstream_accounts SET source_generation=source_generation+1 WHERE site_id=NEW.id;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER pilot_site_source_generation AFTER UPDATE ON sites
FOR EACH ROW EXECUTE FUNCTION pilot_site_source_changed();
