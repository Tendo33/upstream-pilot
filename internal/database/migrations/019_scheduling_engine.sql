ALTER TABLE quality_states ADD COLUMN risks jsonb NOT NULL DEFAULT '[]';
ALTER TABLE quality_states ADD COLUMN plan_error text NOT NULL DEFAULT '';
ALTER TABLE quality_states ADD COLUMN plan_strategy text NOT NULL DEFAULT '';
ALTER TABLE quality_states ADD COLUMN baseline_control jsonb NOT NULL DEFAULT '{}';
ALTER TABLE quality_states ADD COLUMN applied_control jsonb NOT NULL DEFAULT '{}';
ALTER TABLE quality_states ADD COLUMN pending_control jsonb NOT NULL DEFAULT '{}';
ALTER TABLE quality_states ADD COLUMN control_warning text NOT NULL DEFAULT '';
-- Convert old human-readable reasons once. Unknown legacy holds require release.
UPDATE quality_states SET risks = jsonb_build_array(jsonb_build_object(
 'kind',CASE WHEN reason LIKE '%价格%' OR reason LIKE '%成本%' THEN 'price' WHEN reason LIKE '%余额%' THEN 'balance' WHEN reason LIKE '%首字%' THEN 'slow' WHEN reason LIKE '%失败%' OR reason LIKE '%错误率%' THEN 'failure' ELSE 'legacy' END,
 'level',tier,'hard',false,'since',COALESCE(last_changed_at,evaluated_at),'last_changed_at',COALESCE(last_changed_at,evaluated_at),'recovery',0,'unknown',true)) WHERE tier>0;
CREATE TABLE engine_group_policies (
 group_id uuid NOT NULL REFERENCES upstream_groups(id) ON DELETE CASCADE,
 model text NOT NULL DEFAULT '',
 config jsonb NOT NULL,
 updated_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(group_id,model)
);
CREATE TABLE engine_plans (
 site_id uuid PRIMARY KEY REFERENCES sites(id) ON DELETE CASCADE,
 generation uuid NOT NULL,
 plan jsonb NOT NULL,
 evaluated_at timestamptz NOT NULL DEFAULT now()
);
ALTER TABLE upstream_accounts ADD COLUMN price_reference_rate double precision;
ALTER TABLE upstream_accounts ADD COLUMN price_status text NOT NULL DEFAULT 'unknown';
ALTER TABLE upstream_accounts ADD COLUMN last_rate_attempt_at timestamptz;
ALTER TABLE upstream_accounts ADD COLUMN config_generation bigint NOT NULL DEFAULT 0;
ALTER TABLE upstream_accounts ADD COLUMN next_traffic_at timestamptz NOT NULL DEFAULT now();
ALTER TABLE upstream_accounts ADD COLUMN traffic_lease_until timestamptz;
UPDATE upstream_accounts a SET price_reference_rate=(SELECT effective_rate FROM upstream_price_history p WHERE p.account_id=a.id ORDER BY checked_at LIMIT 1),price_status=CASE WHEN last_rate_sync_at IS NOT NULL THEN 'ok' ELSE 'unknown' END;
ALTER TABLE quality_decisions ADD COLUMN detail jsonb NOT NULL DEFAULT '{}';
CREATE INDEX quality_decisions_owner_created ON quality_decisions(owner_id,created_at DESC);
