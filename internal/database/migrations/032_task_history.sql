CREATE TABLE task_runs (
 id uuid PRIMARY KEY,
 owner_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 resource_type text NOT NULL,
 resource_id uuid NOT NULL,
 kind text NOT NULL,
 started_at timestamptz NOT NULL,
 finished_at timestamptz,
 success boolean,
 duration_ms bigint,
 last_error text NOT NULL DEFAULT ''
);
CREATE INDEX task_runs_owner_time ON task_runs(owner_id,started_at DESC);
DELETE FROM task_leases WHERE resource_type LIKE 'site-%';
