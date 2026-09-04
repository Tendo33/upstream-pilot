-- Keep the administration target alongside ownership, including after a pending
-- intent is acknowledged. An address change must not restore another server's
-- baseline merely because its current numeric values happen to match.
ALTER TABLE quality_states ADD COLUMN control_scope text NOT NULL DEFAULT '';
