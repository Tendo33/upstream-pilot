CREATE TABLE account_operations (
 account_id uuid PRIMARY KEY REFERENCES upstream_accounts(id) ON DELETE CASCADE,
 source_generation bigint NOT NULL,
 config jsonb NOT NULL DEFAULT '{}',
 updated_at timestamptz NOT NULL DEFAULT now()
);
