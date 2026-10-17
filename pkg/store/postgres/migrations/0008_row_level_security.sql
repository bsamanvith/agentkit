-- Row-level security (T093): Postgres enforces the store's tenant scoping
-- too, so a query that forgets its tenant filter sees no other tenant's rows.
-- Every connection the store takes from its pool first sets app.tenant to
-- the tenant of the request (or '' for none); the policy compares each row's
-- tenant with it. FORCE applies it to the tables' owner as well; only a
-- superuser or a role with BYPASSRLS (store.Admin's, AGENTKIT_STORE_ADMIN)
-- reads across tenants. A data migration that must touch every tenant's
-- rows runs as such a role.
ALTER TABLE entries ENABLE ROW LEVEL SECURITY;
ALTER TABLE entries FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON entries
    USING (tenant = current_setting('app.tenant', true))
    WITH CHECK (tenant = current_setting('app.tenant', true));

ALTER TABLE runs ENABLE ROW LEVEL SECURITY;
ALTER TABLE runs FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON runs
    USING (tenant = current_setting('app.tenant', true))
    WITH CHECK (tenant = current_setting('app.tenant', true));

ALTER TABLE checkpoints ENABLE ROW LEVEL SECURITY;
ALTER TABLE checkpoints FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON checkpoints
    USING (tenant = current_setting('app.tenant', true))
    WITH CHECK (tenant = current_setting('app.tenant', true));

ALTER TABLE events ENABLE ROW LEVEL SECURITY;
ALTER TABLE events FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON events
    USING (tenant = current_setting('app.tenant', true))
    WITH CHECK (tenant = current_setting('app.tenant', true));

ALTER TABLE conversations ENABLE ROW LEVEL SECURITY;
ALTER TABLE conversations FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON conversations
    USING (tenant = current_setting('app.tenant', true))
    WITH CHECK (tenant = current_setting('app.tenant', true));

ALTER TABLE commands ENABLE ROW LEVEL SECURITY;
ALTER TABLE commands FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON commands
    USING (tenant = current_setting('app.tenant', true))
    WITH CHECK (tenant = current_setting('app.tenant', true));
