CREATE TABLE model_price_cards (
 account_id uuid NOT NULL REFERENCES upstream_accounts(id) ON DELETE CASCADE,
 model text NOT NULL,
 source_generation bigint NOT NULL,
 config jsonb NOT NULL,
 updated_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(account_id,model)
);
CREATE TABLE usage_observations (
 site_id uuid NOT NULL REFERENCES sites(id) ON DELETE CASCADE,
 remote_id bigint NOT NULL,
 account_remote_id bigint NOT NULL,
 group_remote_id bigint,
 request_id text NOT NULL,
 model text NOT NULL,
 input_tokens bigint NOT NULL,
 output_tokens bigint NOT NULL,
 cache_read_tokens bigint NOT NULL,
 cache_write_tokens bigint NOT NULL,
 native_first_chunk_ms integer,
 synthetic boolean NOT NULL DEFAULT false,
 created_at timestamptz NOT NULL,
 PRIMARY KEY(site_id,remote_id)
);
CREATE INDEX usage_observations_group_window ON usage_observations(site_id,group_remote_id,model,created_at DESC);
ALTER TABLE sites ADD COLUMN usage_collection jsonb NOT NULL DEFAULT '{}';
ALTER TABLE sites ADD COLUMN next_usage_at timestamptz NOT NULL DEFAULT now();
ALTER TABLE sites ADD COLUMN usage_lease_until timestamptz;
