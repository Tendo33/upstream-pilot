ALTER TABLE sites
  ADD CONSTRAINT sites_id_owner_unique UNIQUE (id, owner_id);

ALTER TABLE upstream_groups
  ADD CONSTRAINT upstream_groups_id_site_unique UNIQUE (id, site_id);

ALTER TABLE upstream_accounts
  ADD CONSTRAINT upstream_accounts_id_site_unique UNIQUE (id, site_id);

ALTER TABLE account_group_memberships
  ADD COLUMN site_id uuid;

UPDATE account_group_memberships membership
SET site_id = account.site_id
FROM upstream_accounts account
WHERE account.id = membership.account_id;

ALTER TABLE account_group_memberships
  ALTER COLUMN site_id SET NOT NULL,
  DROP CONSTRAINT account_group_memberships_account_id_fkey,
  DROP CONSTRAINT account_group_memberships_group_id_fkey,
  ADD CONSTRAINT account_group_memberships_account_site_fkey
    FOREIGN KEY (account_id, site_id) REFERENCES upstream_accounts(id, site_id) ON DELETE CASCADE,
  ADD CONSTRAINT account_group_memberships_group_site_fkey
    FOREIGN KEY (group_id, site_id) REFERENCES upstream_groups(id, site_id) ON DELETE CASCADE;

CREATE INDEX account_group_memberships_site_idx
  ON account_group_memberships(site_id);

ALTER TABLE probe_attempts
  ADD CONSTRAINT probe_attempts_site_owner_fkey
    FOREIGN KEY (site_id, owner_id) REFERENCES sites(id, owner_id) ON DELETE CASCADE,
  ADD CONSTRAINT probe_attempts_account_site_fkey
    FOREIGN KEY (account_id, site_id) REFERENCES upstream_accounts(id, site_id) ON DELETE CASCADE;
