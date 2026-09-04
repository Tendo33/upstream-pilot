CREATE TABLE group_rate_rules (
  group_id uuid PRIMARY KEY,
  target_site_id uuid NOT NULL,
  owner_id uuid NOT NULL,
  enabled boolean NOT NULL DEFAULT false,
  mode text NOT NULL DEFAULT 'first' CHECK (mode IN ('first', 'average', 'min', 'max', 'custom')),
  rate_offset numeric(20,8) NOT NULL DEFAULT 0 CHECK (rate_offset BETWEEN -100000 AND 100000),
  expression text,
  last_calculated_rate numeric(20,8),
  last_applied_at timestamptz,
  last_error text,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT group_rate_rules_expression_length CHECK (expression IS NULL OR char_length(expression) <= 500),
  CONSTRAINT group_rate_rules_group_site_fkey
    FOREIGN KEY (group_id, target_site_id) REFERENCES upstream_groups(id, site_id) ON DELETE CASCADE,
  CONSTRAINT group_rate_rules_site_owner_fkey
    FOREIGN KEY (target_site_id, owner_id) REFERENCES sites(id, owner_id) ON DELETE CASCADE,
  UNIQUE (group_id, target_site_id, owner_id)
);

CREATE INDEX group_rate_rules_owner_idx
  ON group_rate_rules(owner_id, enabled, updated_at DESC);

CREATE TABLE group_rate_bindings (
  id uuid PRIMARY KEY,
  owner_id uuid NOT NULL,
  target_group_id uuid NOT NULL,
  target_site_id uuid NOT NULL,
  source_account_id uuid NOT NULL,
  source_site_id uuid NOT NULL,
  position integer NOT NULL CHECK (position BETWEEN 0 AND 999),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT group_rate_bindings_rule_fkey
    FOREIGN KEY (target_group_id, target_site_id, owner_id)
    REFERENCES group_rate_rules(group_id, target_site_id, owner_id) ON DELETE CASCADE,
  CONSTRAINT group_rate_bindings_account_site_fkey
    FOREIGN KEY (source_account_id, source_site_id)
    REFERENCES upstream_accounts(id, site_id) ON DELETE CASCADE,
  CONSTRAINT group_rate_bindings_source_site_owner_fkey
    FOREIGN KEY (source_site_id, owner_id)
    REFERENCES sites(id, owner_id) ON DELETE CASCADE,
  UNIQUE (target_group_id, source_account_id),
  UNIQUE (target_group_id, position)
);

CREATE INDEX group_rate_bindings_source_idx
  ON group_rate_bindings(owner_id, source_account_id);
