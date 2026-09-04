ALTER TABLE service_profiles ALTER COLUMN group_id DROP NOT NULL;
ALTER TABLE service_profiles ADD COLUMN account_id uuid REFERENCES upstream_accounts(id) ON DELETE CASCADE;
ALTER TABLE service_profiles ADD CONSTRAINT service_profile_one_target CHECK(num_nonnulls(group_id,account_id)=1);
CREATE INDEX service_profiles_account ON service_profiles(account_id) WHERE account_id IS NOT NULL;

CREATE FUNCTION pilot_account_profile_source_changed() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 UPDATE service_profiles SET generation=generation+1,next_probe_at=now() WHERE account_id=NEW.id;
 RETURN NEW;
END $$;
CREATE TRIGGER pilot_account_probe_generation AFTER UPDATE ON upstream_accounts
FOR EACH ROW WHEN(OLD.source_generation IS DISTINCT FROM NEW.source_generation)
EXECUTE FUNCTION pilot_account_profile_source_changed();
