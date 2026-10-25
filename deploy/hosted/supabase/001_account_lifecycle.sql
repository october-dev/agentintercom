-- Optional October/Supabase identity integration. Apply as postgres AFTER the
-- standalone Bus migrations and October's account deletion migration (0020).
-- This is intentionally outside the embedded, provider-independent schema.
\set ON_ERROR_STOP on
BEGIN;
SET LOCAL lock_timeout='5s';
SET LOCAL statement_timeout='30s';

-- Do not overwrite a newer storage deletion contract. Reconcile any changed
-- buckets/query before applying this migration to another project/revision.
DO $check$
DECLARE source text; lang text;
BEGIN
 SELECT p.prosrc,l.lanname INTO source,lang FROM pg_proc p JOIN pg_language l ON l.oid=p.prolang
 WHERE p.oid=to_regprocedure('public.account_storage_objects(uuid)');
 IF source IS NULL OR lang<>'sql' OR regexp_replace(source,'\s+','','g')<>
 regexp_replace($baseline$select o.bucket_id, o.name from storage.objects o
  where (o.bucket_id = 'october-code' and o.name like p_user::text || '/%')
    or (o.bucket_id = 'code-sync'
      and split_part(o.name, '/', 1) in (select id::text from public.desktop_canvases where owner_id = p_user))
    or (o.bucket_id = 'log-reports'
      and o.name in (select storage_path from public.log_reports where user_id = p_user))$baseline$,'\s+','','g') THEN
  RAISE EXCEPTION 'Account storage contract changed or is missing; reconcile before applying Bus integration';
 END IF;
 IF EXISTS(SELECT 1 FROM october_bus.hosted_members m WHERE NOT EXISTS(
  SELECT 1 FROM auth.users u WHERE u.id::text=m.user_id)) THEN
  RAISE EXCEPTION 'Resolve Bus memberships without live Auth accounts before enabling identity integration';
 END IF;
END $check$;

CREATE TABLE october_bus.supabase_account_deletions (
 user_id uuid PRIMARY KEY REFERENCES auth.users(id) ON DELETE CASCADE,
 started_at timestamptz NOT NULL DEFAULT now()
);
ALTER TABLE october_bus.supabase_account_deletions OWNER TO october_bus_migrator;
REVOKE ALL ON october_bus.supabase_account_deletions FROM PUBLIC,anon,authenticated,october_bus_runtime;

-- The existing account-delete handler calls account_storage_objects before
-- canceling subscriptions, revoking devices or removing files. Fence new Bus
-- memberships here, while checking that the user has handled every workspace.
CREATE FUNCTION public.bus_prepare_account_deletion(p_user uuid) RETURNS void
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,pg_temp AS $body$
BEGIN
 PERFORM 1 FROM auth.users WHERE id=p_user FOR UPDATE;
 IF NOT FOUND THEN RAISE EXCEPTION 'account not found' USING ERRCODE='P0002'; END IF;
 IF EXISTS(SELECT 1 FROM october_bus.hosted_members WHERE user_id=p_user::text) THEN
  RAISE EXCEPTION 'OCTOBER_BUS_WORKSPACES_REMAIN' USING ERRCODE='23505';
 END IF;
 INSERT INTO october_bus.supabase_account_deletions(user_id) VALUES(p_user) ON CONFLICT DO NOTHING;
END $body$;
REVOKE ALL ON FUNCTION public.bus_prepare_account_deletion(uuid) FROM PUBLIC,anon,authenticated;
GRANT EXECUTE ON FUNCTION public.bus_prepare_account_deletion(uuid) TO service_role;

CREATE FUNCTION public.bus_check_account_membership() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,pg_temp AS $body$
BEGIN
 -- Key-share conflicts with the deletion preflight's UPDATE lock. Whichever
 -- transaction wins, no membership can appear after successful preflight.
 PERFORM 1 FROM auth.users WHERE id=NEW.user_id::uuid FOR KEY SHARE;
 IF NOT FOUND THEN RAISE EXCEPTION 'Bus membership requires a live account' USING ERRCODE='23503'; END IF;
 IF EXISTS(SELECT 1 FROM october_bus.supabase_account_deletions WHERE user_id=NEW.user_id::uuid) THEN
  RAISE EXCEPTION 'Account deletion is in progress' USING ERRCODE='40001';
 END IF;
 RETURN NEW;
END $body$;
REVOKE ALL ON FUNCTION public.bus_check_account_membership() FROM PUBLIC,anon,authenticated;
CREATE TRIGGER bus_member_account BEFORE INSERT OR UPDATE OF user_id ON october_bus.hosted_members
 FOR EACH ROW EXECUTE FUNCTION public.bus_check_account_membership();

-- Protect direct Auth admin deletes too. They must never leave devices or
-- subscriptions authorizing an identity whose account has disappeared.
CREATE FUNCTION public.bus_check_account_delete() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,pg_temp AS $body$
BEGIN
 IF EXISTS(SELECT 1 FROM october_bus.hosted_members WHERE user_id=OLD.id::text) THEN
  RAISE EXCEPTION 'OCTOBER_BUS_WORKSPACES_REMAIN' USING ERRCODE='23505';
 END IF;
 RETURN OLD;
END $body$;
REVOKE ALL ON FUNCTION public.bus_check_account_delete() FROM PUBLIC,anon,authenticated;
CREATE TRIGGER bus_account_delete BEFORE DELETE ON auth.users
 FOR EACH ROW EXECUTE FUNCTION public.bus_check_account_delete();

CREATE OR REPLACE FUNCTION public.account_storage_objects(p_user uuid)
RETURNS TABLE(bucket text,name text)
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,pg_temp AS $body$
BEGIN
 PERFORM public.bus_prepare_account_deletion(p_user);
 RETURN QUERY select o.bucket_id, o.name from storage.objects o
  where (o.bucket_id = 'october-code' and o.name like p_user::text || '/%')
    or (o.bucket_id = 'code-sync'
      and split_part(o.name, '/', 1) in (select id::text from public.desktop_canvases where owner_id = p_user))
    or (o.bucket_id = 'log-reports'
      and o.name in (select storage_path from public.log_reports where user_id = p_user));
END $body$;
REVOKE ALL ON FUNCTION public.account_storage_objects(uuid) FROM PUBLIC,anon,authenticated;
GRANT EXECUTE ON FUNCTION public.account_storage_objects(uuid) TO service_role;
NOTIFY pgrst,'reload schema';
COMMIT;
