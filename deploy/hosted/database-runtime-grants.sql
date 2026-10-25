-- Run as october_bus_migrator after every successful database migrate.
-- These roles have no grants on another product's tables.
\set ON_ERROR_STOP on
BEGIN;
GRANT USAGE ON SCHEMA october_bus TO october_bus_runtime;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA october_bus TO october_bus_runtime;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA october_bus TO october_bus_runtime;
GRANT EXECUTE ON ALL FUNCTIONS IN SCHEMA october_bus TO october_bus_runtime;
REVOKE INSERT, UPDATE, DELETE ON october_bus.schema_migrations FROM october_bus_runtime;
-- Optional Supabase lifecycle fencing is owned by the identity administrator.
DO $$ BEGIN
 IF to_regclass('october_bus.supabase_account_deletions') IS NOT NULL THEN
  REVOKE ALL ON october_bus.supabase_account_deletions FROM october_bus_runtime;
 END IF;
END $$;
REVOKE ALL ON SCHEMA october_bus FROM PUBLIC;
REVOKE ALL ON ALL TABLES IN SCHEMA october_bus FROM PUBLIC;
REVOKE ALL ON ALL FUNCTIONS IN SCHEMA october_bus FROM PUBLIC;
COMMIT;
