-- Run once with psql as the database administrator, before database migrate.
-- Passwords come from private environment variables, never psql -v arguments.
-- This deliberately refuses an existing schema or role instead of taking it over.
\set ON_ERROR_STOP on
\getenv migration_password OCTOBER_BUS_MIGRATION_PASSWORD
\getenv runtime_password OCTOBER_BUS_RUNTIME_PASSWORD
SELECT length(:'migration_password') >= 32 AND length(:'runtime_password') >= 32
  AND :'migration_password' <> :'runtime_password' AS valid_passwords \gset
\if :valid_passwords
\else
  \echo 'Provide two different random passwords of at least 32 characters through the environment.'
  \quit 1
\endif

BEGIN;
SELECT pg_advisory_xact_lock(6863746, 1);
DO $$ BEGIN
  IF EXISTS (SELECT 1 FROM pg_namespace WHERE nspname = 'october_bus')
     OR EXISTS (SELECT 1 FROM pg_roles WHERE rolname IN ('october_bus_migrator', 'october_bus_runtime')) THEN
    RAISE EXCEPTION 'Bus schema or roles already exist; inspect ownership instead of rerunning bootstrap';
  END IF;
END $$;
CREATE ROLE october_bus_migrator LOGIN NOINHERIT NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS PASSWORD :'migration_password';
CREATE ROLE october_bus_runtime LOGIN NOINHERIT NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS CONNECTION LIMIT 24 PASSWORD :'runtime_password';
CREATE SCHEMA october_bus AUTHORIZATION october_bus_migrator;
REVOKE ALL ON SCHEMA october_bus FROM PUBLIC;
GRANT USAGE ON SCHEMA october_bus TO october_bus_runtime;
SELECT format('GRANT CONNECT ON DATABASE %I TO october_bus_migrator, october_bus_runtime', current_database()) \gexec
ALTER ROLE october_bus_migrator SET search_path = october_bus, pg_catalog;
ALTER ROLE october_bus_runtime SET search_path = october_bus, pg_catalog;
ALTER ROLE october_bus_runtime SET statement_timeout = '30s';
ALTER ROLE october_bus_runtime SET lock_timeout = '10s';
ALTER DEFAULT PRIVILEGES FOR ROLE october_bus_migrator REVOKE ALL ON FUNCTIONS FROM PUBLIC;
ALTER DEFAULT PRIVILEGES FOR ROLE october_bus_migrator IN SCHEMA october_bus GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO october_bus_runtime;
ALTER DEFAULT PRIVILEGES FOR ROLE october_bus_migrator IN SCHEMA october_bus GRANT USAGE, SELECT ON SEQUENCES TO october_bus_runtime;
ALTER DEFAULT PRIVILEGES FOR ROLE october_bus_migrator IN SCHEMA october_bus GRANT EXECUTE ON FUNCTIONS TO october_bus_runtime;
COMMIT;
\unset migration_password
\unset runtime_password
