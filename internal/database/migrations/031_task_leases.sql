CREATE TABLE task_leases (
 resource_type text NOT NULL,
 resource_id uuid NOT NULL,
 owner_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 kind text NOT NULL,
 owner_token uuid NOT NULL,
 lease_until timestamptz NOT NULL,
 started_at timestamptz NOT NULL,
 finished_at timestamptz,
 last_success_at timestamptz,
 duration_ms bigint,
 last_error text NOT NULL DEFAULT '',
 PRIMARY KEY(resource_type,resource_id)
);
CREATE INDEX task_leases_owner ON task_leases(owner_id,started_at DESC);
