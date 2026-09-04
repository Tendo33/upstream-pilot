ALTER TABLE sites ADD COLUMN telemetry_generation bigint NOT NULL DEFAULT 0;
ALTER TABLE sites ADD COLUMN next_traffic_sample_at timestamptz NOT NULL DEFAULT now();
ALTER TABLE sites ADD COLUMN traffic_sample_lease_until timestamptz;
ALTER TABLE sites ADD COLUMN traffic_collection jsonb NOT NULL DEFAULT '{}';
CREATE FUNCTION pilot_telemetry_scope() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF ROW(NEW.base_url,NEW.api_key_ciphertext) IS DISTINCT FROM ROW(OLD.base_url,OLD.api_key_ciphertext) THEN
  NEW.telemetry_generation:=OLD.telemetry_generation+1;
  NEW.next_usage_at:=now();NEW.next_traffic_sample_at:=now();
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER pilot_telemetry_scope BEFORE UPDATE ON sites FOR EACH ROW EXECUTE FUNCTION pilot_telemetry_scope();
ALTER TABLE usage_observations ADD COLUMN site_generation bigint NOT NULL DEFAULT 0;
ALTER TABLE usage_observations DROP CONSTRAINT usage_observations_pkey;
ALTER TABLE usage_observations ADD PRIMARY KEY(site_id,site_generation,remote_id);
CREATE TABLE request_outcome_observations (
 site_id uuid NOT NULL REFERENCES sites(id) ON DELETE CASCADE,
 site_generation bigint NOT NULL,
 group_remote_id bigint NOT NULL,
 request_id text NOT NULL,
 model text NOT NULL,
 outcome text NOT NULL CHECK(outcome IN('unknown','success','failure','conflict')),
 seen_at timestamptz NOT NULL,
 PRIMARY KEY(site_id,site_generation,group_remote_id,request_id)
);
CREATE INDEX request_outcomes_window ON request_outcome_observations(site_id,site_generation,seen_at DESC);
