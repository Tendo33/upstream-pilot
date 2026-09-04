CREATE TABLE service_profiles (
 id uuid PRIMARY KEY,
 group_id uuid NOT NULL REFERENCES upstream_groups(id) ON DELETE CASCADE,
 config jsonb NOT NULL,
 key_ciphertext text NOT NULL DEFAULT '',
 generation bigint NOT NULL DEFAULT 1,
 enabled boolean NOT NULL DEFAULT false,
 next_probe_at timestamptz NOT NULL DEFAULT now(),
 lease_token uuid,
 lease_until timestamptz,
 last_probe_at timestamptz,
 last_error text NOT NULL DEFAULT '',
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX service_profiles_due ON service_profiles(next_probe_at) WHERE enabled;
CREATE TABLE service_canary_runs (
 id uuid PRIMARY KEY,
 profile_id uuid NOT NULL REFERENCES service_profiles(id) ON DELETE CASCADE,
 generation bigint NOT NULL,
 status text NOT NULL CHECK(status IN ('reserved','passed','failed','abandoned')),
 reserved_tokens integer NOT NULL CHECK(reserved_tokens>0),
 reserved_cost double precision,
 profile_snapshot jsonb NOT NULL DEFAULT '{}',
 result jsonb NOT NULL DEFAULT '{}',
 started_at timestamptz NOT NULL DEFAULT now(),
 completed_at timestamptz
);
CREATE INDEX service_canary_runs_profile_window ON service_canary_runs(profile_id,started_at DESC);

CREATE OR REPLACE FUNCTION pilot_site_source_changed() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF ROW(NEW.base_url,NEW.api_key_ciphertext) IS DISTINCT FROM ROW(OLD.base_url,OLD.api_key_ciphertext) THEN
   UPDATE upstream_accounts SET source_generation=source_generation+1 WHERE site_id=NEW.id;
   UPDATE service_profiles SET generation=generation+1,next_probe_at=now()
     WHERE group_id IN(SELECT id FROM upstream_groups WHERE site_id=NEW.id);
 END IF;
 RETURN NEW;
END $$;
