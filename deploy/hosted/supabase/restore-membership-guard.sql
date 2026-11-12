-- Run as the identity administrator after a quarantined Bus-only restore into
-- the existing October Supabase project. For a fresh identity project install
-- the full lifecycle integration instead. Keep recovered workspaces disabled.
\set ON_ERROR_STOP on
BEGIN;
SET LOCAL lock_timeout='5s';
SET LOCAL statement_timeout='30s';
DO $$ BEGIN
 IF to_regclass('october_bus_identity.account_deletions') IS NULL
 OR NOT EXISTS(SELECT 1 FROM pg_proc WHERE oid=to_regprocedure('public.bus_check_account_membership()') AND prosecdef) THEN
  RAISE EXCEPTION 'Shared identity integration missing; install it before restoring access';
 END IF;
 IF EXISTS(SELECT 1 FROM october_bus.hosted_members m WHERE NOT EXISTS(SELECT 1 FROM auth.users u WHERE u.id::text=m.user_id)
 OR EXISTS(SELECT 1 FROM october_bus_identity.account_deletions d WHERE d.user_id::text=m.user_id)) THEN
  RAISE EXCEPTION 'Reconcile restored memberships for deleted/deleting accounts before restoring access';
 END IF;
END $$;
DROP TRIGGER IF EXISTS bus_member_account ON october_bus.hosted_members;
CREATE TRIGGER bus_member_account BEFORE INSERT OR UPDATE OF user_id ON october_bus.hosted_members
 FOR EACH ROW EXECUTE FUNCTION public.bus_check_account_membership();
COMMIT;
