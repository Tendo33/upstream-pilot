CREATE TABLE notification_channels (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 owner_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 name text NOT NULL,
 provider text NOT NULL CHECK(provider IN('auto','feishu','wecom','webhook')),
 enabled boolean NOT NULL DEFAULT false,
 categories text[] NOT NULL DEFAULT ARRAY['quality','price','balance','collector','controller','runway'],
 webhook_ciphertext text NOT NULL,
 signing_secret_ciphertext text,
 secret_purpose text NOT NULL,
 revision bigint NOT NULL DEFAULT 0,
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 legacy_source text,
 UNIQUE(owner_id,legacy_source)
);
CREATE TABLE notification_rules (
 owner_id uuid PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
 price_rise_percent double precision NOT NULL DEFAULT 5 CHECK(price_rise_percent BETWEEN 0 AND 10000),
 price_cooldown_seconds integer NOT NULL DEFAULT 3600 CHECK(price_cooldown_seconds BETWEEN 60 AND 86400),
 balance_enabled boolean NOT NULL DEFAULT false,
 balance_threshold double precision NOT NULL DEFAULT 10 CHECK(balance_threshold BETWEEN 0 AND 1000000000000),
 balance_cooldown_seconds integer NOT NULL DEFAULT 21600 CHECK(balance_cooldown_seconds BETWEEN 300 AND 2592000),
 balance_snoozed_until timestamptz,
 updated_at timestamptz NOT NULL DEFAULT now()
);
INSERT INTO notification_rules(owner_id,balance_enabled,balance_threshold,balance_cooldown_seconds,balance_snoozed_until)
 SELECT owner_id,enabled,threshold,cooldown_seconds,cooldown_until FROM balance_alert_settings;

-- Preserve both old destinations, their enablement and their original audiences.
-- AEAD ciphertext keeps its original purpose until that channel is edited.
INSERT INTO notification_channels(owner_id,name,provider,enabled,categories,webhook_ciphertext,secret_purpose,legacy_source)
 SELECT owner_id,'原质量通知','auto',enabled,ARRAY['quality','price','collector','controller','runway'],webhook_ciphertext,'quality-alert:'||owner_id::text,'quality'
 FROM quality_alert_settings WHERE webhook_ciphertext IS NOT NULL;
INSERT INTO notification_channels(owner_id,name,provider,enabled,categories,webhook_ciphertext,secret_purpose,legacy_source)
 SELECT owner_id,'原余额通知','wecom',enabled,ARRAY['balance'],webhook_url_ciphertext,'balance-alert:'||owner_id::text,'balance'
 FROM balance_alert_settings WHERE webhook_url_ciphertext IS NOT NULL;

ALTER TABLE quality_notifications ADD COLUMN category text NOT NULL DEFAULT '';
ALTER TABLE quality_notifications ADD COLUMN severity text NOT NULL DEFAULT '';
ALTER TABLE quality_notifications ADD COLUMN context jsonb NOT NULL DEFAULT '{}';
ALTER TABLE quality_notifications ADD COLUMN source_generation bigint;

CREATE FUNCTION pilot_notification_category(kind text) RETURNS text LANGUAGE sql IMMUTABLE AS $$
 SELECT CASE WHEN kind='test' THEN 'test' WHEN kind LIKE 'price_%' THEN 'price'
 WHEN kind LIKE 'balance_runway%' THEN 'runway' WHEN kind LIKE 'balance_%' THEN 'balance'
 WHEN kind LIKE 'collector_%' THEN 'collector'
 WHEN kind LIKE 'controller%' OR kind LIKE 'pending_control%' THEN 'controller' ELSE 'quality' END
$$;
UPDATE quality_notifications SET category=pilot_notification_category(kind),
 severity=CASE WHEN kind='healthy' OR kind LIKE '%_recovered' THEN 'recovery' ELSE 'warning' END,
 context='{"legacy":true}';

CREATE TABLE notification_deliveries (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 event_id uuid NOT NULL REFERENCES quality_notifications(id) ON DELETE CASCADE,
 channel_id uuid NOT NULL REFERENCES notification_channels(id) ON DELETE CASCADE,
 channel_revision bigint NOT NULL,
 channel_name text NOT NULL DEFAULT '',
 provider text NOT NULL DEFAULT '',
 status text NOT NULL DEFAULT 'pending' CHECK(status IN('pending','sending','delivered','failed','cancelled','expired')),
 attempts integer NOT NULL DEFAULT 0,
 next_attempt_at timestamptz NOT NULL DEFAULT now(),
 lease_until timestamptz,
 lease_token uuid,
 delivered_at timestamptz,
 last_error text NOT NULL DEFAULT '',
 created_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(event_id,channel_id)
);
CREATE FUNCTION pilot_delivery_context() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE event_owner uuid; channel_owner uuid;
BEGIN
 SELECT owner_id INTO event_owner FROM quality_notifications WHERE id=NEW.event_id;
 SELECT owner_id,name,provider INTO channel_owner,NEW.channel_name,NEW.provider FROM notification_channels WHERE id=NEW.channel_id;
 IF event_owner IS DISTINCT FROM channel_owner THEN RAISE EXCEPTION 'notification delivery owner mismatch'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER pilot_delivery_context BEFORE INSERT ON notification_deliveries FOR EACH ROW EXECUTE FUNCTION pilot_delivery_context();
CREATE INDEX notification_deliveries_due ON notification_deliveries(next_attempt_at) WHERE status IN('pending','sending');
INSERT INTO notification_deliveries(event_id,channel_id,channel_revision,status,attempts,next_attempt_at,delivered_at,last_error,created_at)
 SELECT n.id,c.id,c.revision,
 CASE WHEN n.delivered_at IS NOT NULL THEN 'delivered' WHEN n.created_at<now()-interval '1 hour' THEN 'expired' WHEN n.attempts>=5 THEN 'failed' ELSE 'pending' END,
 n.attempts,n.next_attempt_at,n.delivered_at,n.last_error,n.created_at
 FROM quality_notifications n JOIN notification_channels c ON c.owner_id=n.owner_id AND c.legacy_source='quality';

CREATE FUNCTION pilot_notification_context() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE account_context jsonb; generation bigint;
BEGIN
 IF NEW.category='' THEN NEW.category:=pilot_notification_category(NEW.kind); END IF;
 IF NEW.severity='' THEN NEW.severity:=CASE WHEN NEW.kind='healthy' OR NEW.kind LIKE '%_recovered' THEN 'recovery' WHEN NEW.kind='test' THEN 'info' ELSE 'warning' END; END IF;
 IF NEW.account_id IS NOT NULL THEN
  SELECT jsonb_build_object('account_name',left(a.name,200),'site_name',left(s.name,200),'remote_account_id',a.remote_id,
   'groups',COALESCE((SELECT jsonb_agg(g.name) FROM (SELECT left(g.name,200) AS name FROM account_group_memberships m JOIN upstream_groups g ON g.id=m.group_id WHERE m.account_id=a.id AND g.deleted_at IS NULL ORDER BY g.name LIMIT 50)g),'[]'::jsonb)),a.source_generation
   INTO account_context,generation FROM upstream_accounts a JOIN sites s ON s.id=a.site_id WHERE a.id=NEW.account_id AND s.owner_id=NEW.owner_id;
  IF NOT FOUND THEN RAISE EXCEPTION 'notification account owner mismatch'; END IF;
  NEW.context:=COALESCE(account_context,'{}')||NEW.context;
  NEW.source_generation:=COALESCE(NEW.source_generation,generation);
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER pilot_notification_context BEFORE INSERT ON quality_notifications FOR EACH ROW EXECUTE FUNCTION pilot_notification_context();
CREATE FUNCTION pilot_notification_route() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 INSERT INTO notification_deliveries(event_id,channel_id,channel_revision)
 SELECT NEW.id,c.id,c.revision FROM notification_channels c
 WHERE c.owner_id=NEW.owner_id AND c.enabled AND NEW.category=ANY(c.categories);
 RETURN NEW;
END $$;
CREATE TRIGGER pilot_notification_route AFTER INSERT ON quality_notifications FOR EACH ROW EXECUTE FUNCTION pilot_notification_route();

CREATE TABLE notification_price_states (
 account_id uuid PRIMARY KEY REFERENCES upstream_accounts(id) ON DELETE CASCADE,
 source_generation bigint NOT NULL,
 reference_rate double precision NOT NULL,
 last_event_at timestamptz
);
INSERT INTO notification_price_states(account_id,source_generation,reference_rate)
 SELECT id,source_generation,observed_cost_rate FROM upstream_accounts
 WHERE observed_cost_rate IS NOT NULL AND price_source_generation=source_generation AND deleted_at IS NULL;
CREATE TABLE notification_balance_states (
 account_id uuid PRIMARY KEY REFERENCES upstream_accounts(id) ON DELETE CASCADE,
 source_generation bigint NOT NULL,
 active boolean NOT NULL DEFAULT false,
 unit text NOT NULL DEFAULT '',
 last_event_at timestamptz
);
-- Retained only for rollback/provenance; the application no longer reads or writes these settings.
ALTER TABLE quality_alert_settings RENAME TO quality_alert_settings_legacy;
ALTER TABLE balance_alert_settings RENAME TO balance_alert_settings_legacy;
