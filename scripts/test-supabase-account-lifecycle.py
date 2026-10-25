#!/usr/bin/env python3
"""Exercise the optional identity bridge against a disposable loopback PostgreSQL."""
import os
import pathlib
import re
import subprocess
import time
import urllib.parse
import uuid

ROOT = pathlib.Path(__file__).resolve().parents[1]
url = urllib.parse.urlsplit(os.environ.get('OCTOBER_BUS_TEST_POSTGRES_URL', ''))
if url.hostname not in ('localhost', '127.0.0.1', '::1') or not re.fullmatch(r'[a-zA-Z0-9_]*_test[a-zA-Z0-9_]*', url.path[1:]):
    raise SystemExit('Set OCTOBER_BUS_TEST_POSTGRES_URL to a disposable loopback test database')
if url.scheme not in ('postgres', 'postgresql'):
    raise SystemExit('Expected a PostgreSQL test URL')
env = dict(os.environ, PGHOST=url.hostname, PGPORT=str(url.port or 5432),
           PGUSER=urllib.parse.unquote(url.username or ''), PGPASSWORD=urllib.parse.unquote(url.password or ''),
           PGDATABASE=url.path[1:], PGSSLMODE='disable', PGCONNECT_TIMEOUT='5')
args = ['psql', '-X', '-q', '-t', '-A', '-v', 'ON_ERROR_STOP=1', '-v', 'VERBOSITY=verbose']
created_roles = []
database = 'october_bus_identity_test_' + uuid.uuid4().hex[:12]
restore_database = database + '_restore'
dump_path = '/tmp/' + database + '.dump'
container = os.environ.get('OCTOBER_BUS_TEST_POSTGRES_CONTAINER', '')
if container and not re.fullmatch(r'[a-f0-9]+', container):
    raise SystemExit('Expected a test PostgreSQL container ID')
u1, u2, u3 = [str(uuid.uuid4()) for _ in range(3)]


def query(sql, db=None, failure=None):
    result = subprocess.run(args, input=sql, text=True, capture_output=True,
                            env=dict(env, PGDATABASE=db or database), timeout=15)
    if failure:
        assert result.returncode != 0 and failure in result.stderr, (failure, result.stderr)
    elif result.returncode:
        raise RuntimeError(result.stderr)
    return result.stdout.strip()


def member(user, scope='fixture'):
    return f"SET ROLE october_bus_runtime; INSERT INTO october_bus.hosted_members VALUES('{scope}','{user}');"


def pgtool(name, *arguments, db=None, input=None):
    command = [name, *arguments]
    if container:
        # Use the service container's matching PostgreSQL version in CI. No
        # database passwords are interpolated into process arguments.
        command = ['docker', 'exec', '-i', *sum((['--env', key] for key in ('PGHOST', 'PGPORT', 'PGUSER', 'PGPASSWORD', 'PGDATABASE', 'PGSSLMODE')), []), container, *command]
    result = subprocess.run(command, env=dict(env, PGDATABASE=db or database), input=input,
                            text=True, capture_output=True, timeout=20)
    if result.returncode:
        raise RuntimeError(result.stderr)
    return result.stdout


def race(sql, follower, expected):
    # The marker is emitted only after the first transaction has acquired its
    # locks. A second connection then exercises the contested order.
    process = subprocess.Popen(args, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                               text=True, env=dict(env, PGDATABASE=database))
    process.stdin.write('BEGIN;\n' + sql + "\n\\echo READY\nSELECT pg_sleep(2);\nCOMMIT;\n")
    process.stdin.close()
    assert any(line.strip() == 'READY' for line in process.stdout), process.stderr.read()
    before = time.monotonic()
    query(follower, failure=expected)
    assert time.monotonic() - before > 0.5, 'contested operation did not wait for the account lock'
    process.wait(timeout=10)
    assert process.returncode == 0, process.stderr.read()


try:
    for role in ('anon', 'authenticated', 'service_role', 'october_bus_migrator', 'october_bus_runtime'):
        if not query(f"SELECT 1 FROM pg_roles WHERE rolname='{role}'", env['PGDATABASE']):
            query(f'CREATE ROLE {role} NOLOGIN', env['PGDATABASE'])
            created_roles.append(role)
    query(f'CREATE DATABASE {database}', env['PGDATABASE'])
    # Keep the original shared storage query as a contract fixture, independent
    # of the new guard logic. These stand-ins contain no application/user data.
    query('''
CREATE SCHEMA auth; CREATE SCHEMA storage;
CREATE SCHEMA october_bus AUTHORIZATION october_bus_migrator;
CREATE TABLE auth.users(id uuid PRIMARY KEY);
CREATE TABLE storage.objects(bucket_id text,name text);
CREATE TABLE public.desktop_canvases(id uuid,owner_id uuid);
CREATE TABLE public.log_reports(user_id uuid,storage_path text);
CREATE TABLE october_bus.hosted_members(scope_id text,user_id text);
ALTER TABLE october_bus.hosted_members OWNER TO october_bus_migrator;
CREATE TABLE october_bus.schema_migrations(version text);
ALTER TABLE october_bus.schema_migrations OWNER TO october_bus_migrator;
CREATE FUNCTION public.account_storage_objects(p_user uuid)
RETURNS TABLE(bucket text,name text) LANGUAGE sql STABLE SECURITY DEFINER SET search_path=public,pg_temp AS $$
  select o.bucket_id, o.name from storage.objects o
  where (o.bucket_id = 'october-code' and o.name like p_user::text || '/%')
    or (o.bucket_id = 'code-sync'
      and split_part(o.name, '/', 1) in (select id::text from public.desktop_canvases where owner_id = p_user))
    or (o.bucket_id = 'log-reports'
      and o.name in (select storage_path from public.log_reports where user_id = p_user))
$$;
''')
    query(f"INSERT INTO auth.users VALUES('{u1}'),('{u2}'),('{u3}');")
    migration = (ROOT / 'deploy/hosted/supabase/001_account_lifecycle.sql').read_text()
    query(migration)
    # Reapply normal runtime grants as the limited migrator, just as deployment
    # does after every embedded migration. It must not reopen identity fencing.
    query('SET ROLE october_bus_migrator;\n' + (ROOT / 'deploy/hosted/database-runtime-grants.sql').read_text())
    query('SET ROLE october_bus_runtime; SELECT * FROM auth.users;', failure='42501')
    query('SET ROLE october_bus_runtime; DELETE FROM october_bus_identity.account_deletions;', failure='42501')
    query(f"SET ROLE anon; SELECT public.bus_prepare_account_deletion('{u1}');", failure='42501')
    query(member(u1))
    query(member(str(uuid.uuid4())), failure='23503')
    query(f"SET ROLE service_role; SELECT * FROM public.account_storage_objects('{u1}');", failure='OCTOBER_BUS_WORKSPACES_REMAIN')
    query(f"DELETE FROM auth.users WHERE id='{u1}';", failure='OCTOBER_BUS_WORKSPACES_REMAIN')
    assert query('SELECT count(*) FROM october_bus_identity.account_deletions') == '0'
    query('DELETE FROM october_bus.hosted_members;')
    canvas = str(uuid.uuid4())
    query(f"""INSERT INTO public.desktop_canvases VALUES('{canvas}','{u1}');
INSERT INTO public.log_reports VALUES('{u1}','diagnostic');
INSERT INTO storage.objects VALUES('october-code','{u1}/file'),('code-sync','{canvas}/file'),('log-reports','diagnostic'),('october-code','other/file');""")
    result = query(f"SET ROLE service_role; SELECT * FROM public.account_storage_objects('{u1}');")
    assert len(result.splitlines()) == 3, 'preflight changed the storage object contract'
    query(member(u1), failure='40001')
    query(f"DELETE FROM auth.users WHERE id='{u1}';")
    assert query('SELECT count(*) FROM october_bus_identity.account_deletions') == '0'
    query(member(u1), failure='23503')
    race(f"SELECT public.bus_prepare_account_deletion('{u2}');", member(u2), '40001')
    race(member(u3), f"SET ROLE service_role; SELECT * FROM public.account_storage_objects('{u3}');", 'OCTOBER_BUS_WORKSPACES_REMAIN')
    pgtool('pg_dump', '--format=custom', '--schema=october_bus', '--role=october_bus_runtime', '--file='+dump_path)
    listing = pgtool('pg_restore', '--list', dump_path)
    assert 'TRIGGER october_bus hosted_members bus_member_account' in listing
    assert not any(re.search(r'\boctober_bus_identity\b', line) for line in listing.splitlines() if not line.startswith(';')), 'identity metadata leaked into the standalone archive'
    filtered = subprocess.run(['python3', str(ROOT / 'deploy/hosted/restore-list.py'), '--quarantine'],
                              input=listing, capture_output=True, text=True, check=True).stdout
    assert 'TRIGGER october_bus hosted_members bus_member_account' not in filtered
    query(f'CREATE DATABASE {restore_database}', env['PGDATABASE'])
    query('CREATE SCHEMA october_bus AUTHORIZATION october_bus_migrator;', restore_database)
    pgtool('pg_restore', '--dbname='+restore_database, '--role=october_bus_migrator', '--single-transaction',
           '--exit-on-error', '--no-owner', '--no-privileges', '--use-list=/dev/stdin', dump_path,
           db=restore_database, input=filtered)
    assert query('SELECT count(*) FROM october_bus.hosted_members', restore_database) == '1'
    assert query("SELECT count(*) FROM pg_namespace WHERE nspname='auth'", restore_database) == '0'
    repair = (ROOT / 'deploy/hosted/supabase/restore-membership-guard.sql').read_text()
    query(repair, restore_database, failure='Shared identity integration missing')
    query('DROP TRIGGER bus_member_account ON october_bus.hosted_members;')
    query(repair)
    query(member(u1), failure='23503')
    # A later migration must not silently overwrite a changed storage contract.
    query(migration, failure='Account storage contract changed or is missing')
    print('Supabase identity lifecycle: membership, deletion, storage, privileges, both race orders and standalone backup/restore passed')
finally:
    if container:
        subprocess.run(['docker', 'exec', container, 'rm', '-f', dump_path], check=True)
    else:
        pathlib.Path(dump_path).unlink(missing_ok=True)
    query(f'DROP DATABASE IF EXISTS {restore_database} WITH (FORCE)', env['PGDATABASE'])
    query(f'DROP DATABASE IF EXISTS {database} WITH (FORCE)', env['PGDATABASE'])
    for role in reversed(created_roles):
        query(f'DROP ROLE {role}', env['PGDATABASE'])
