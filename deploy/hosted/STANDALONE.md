# Standalone workspace deployment

The deployable unit is one Go service, PostgreSQL 17+, a private file directory,
and the existing TLS proxy. The source and deployment files live in this public
repository. Credentials and customer data do not. A second infrastructure
repository is unnecessary.

Keep an older Bus or worker gateway running under its existing binary, user and
data directory until its callers have moved. The standalone service uses its own
`/opt/october-bus-hosted` release path, `october-bus-hosted` system account and
`/var/lib/october-bus-hosted` data. It never takes over another product's ledger.

## Database provisioning

Use a separate staging database/project before enabling public signup. Do not
point conformance, reset, or rehearsal commands at customer data.

1. As a database administrator, run [database-bootstrap.sql](database-bootstrap.sql)
   with `psql -X -v ON_ERROR_STOP=1 -f ...`. It requires two different random
   passwords of at least 32 characters in `OCTOBER_BUS_MIGRATION_PASSWORD` and
   `OCTOBER_BUS_RUNTIME_PASSWORD`. Pass them through a private environment file or
   secret manager, not command arguments or SQL pasted into an issue. Bootstrap
   refuses existing role/schema names. It creates only `october_bus`, its
   migration owner and its runtime account; neither role is an administrator.
2. Using the **migration** connection as `OCTOBER_BUS_DATABASE_URL`, run
   `october-bus database migrate`, then run
   [database-runtime-grants.sql](database-runtime-grants.sql) with `psql` using
   the same account. Repeat grants after future migrations. The runtime can read
   migration history but cannot change it or create tables.
3. Using the **runtime** connection, run `october-bus database check`. Put only
   that connection in the running service's environment. Keep the migration
   credential separate and unavailable to the service process.

The canonical migration history remains in [migrations](migrations/README.md).
Bootstrap/grants are operator provisioning scripts, not extra schema migrations.
For Supabase, keep this schema out of the exposed PostgREST schema list. Do not
reuse the project's `service_role` key as a database password or broaden another
application's database role. Use a direct or session-pooler endpoint for `psql`,
`pg_dump` and restore. Runtime transaction-pooler connections are supported.
All remote PostgreSQL connections require a trusted CA and `sslmode=verify-full`.

## Release and configuration

Create a dedicated system user with no login shell and no home directory. Put
each reviewed release into `/opt/october-bus-hosted/releases/<version>/`, owned by
root and not writable by the service account. Include the executable, `backup.py`,
this directory's SQL files and the source commit identifier. Point `current` at
the selected release using an atomic symlink replacement. Do not overwrite the
executable used by an older service.

Install [october-bus-hosted.service](october-bus-hosted.service). Its initial
memory limits suit a small host and must be checked under the intended session
load. Configure root-only `/etc/october-bus/hosted.env` with:

```text
OCTOBER_BUS_PUBLIC_URL=https://bus.example.com
OCTOBER_BUS_DATABASE_URL=<runtime PostgreSQL URL with verified TLS>
OCTOBER_BUS_ENCRYPTION_KEY=<base64 encoding of 32 random bytes>
OCTOBER_BUS_AUTH_URL=<trusted Supabase project origin>
OCTOBER_BUS_AUTH_PUBLIC_KEY=<public anon or publishable API key>
```

The unit supplies the private attachment directory. Preserve the encryption key
across deployments; changing it is a credential migration, not a restart. Configure
Supabase's signup/confirmation/recovery redirects for the actual `/app` origin.
The account API accepts verified direct Auth sessions and does not accept a
third-party OAuth client token as a user's direct session.

Use [Caddyfile.standalone](Caddyfile.standalone) as a separate staging site, validate
Caddy, then reload it. Only HTTPS is public; port 4780 stays on loopback. Preserve
Host and Authorization. Keep body/header logging disabled and exclude MCP from
page caches and interactive browser challenges. The older pilot's 1 MiB proxy
limit is too small for the standalone file API.

Configure payment settings only after the test-mode rehearsal in
[hosted billing](../../docs/hosted-billing.md). A service without Stripe settings
can run account/trial, messaging and file acceptance tests. That does not make
checkout available or prove a paid launch is ready.

For a deployed handshake check, create a manual MCP key in a synthetic workspace
and save it to an owner-only file, then run:

```sh
node scripts/check-hosted-mcp.mjs https://bus.example.com/mcp /private/mcp-key
```

This verifies public readiness, hidden admin routes, browser routing and security
policy, OAuth resource discovery and authenticated MCP tool discovery. It creates
or renews that connector's execution but sends no messages. Use a synthetic
workspace and revoke the key afterward. It does not replace real account sign-in,
message/request/reply, file, backup, named-client or payment qualification.

## Encrypted backups

Install Python 3.11+, PostgreSQL client tools matching or newer than the server,
and Restic (tested command surface: 0.18+). Create and initialize a dedicated,
private **off-host** Restic repository. Encryption does not replace repository
access controls. Store its password separately from both the host and repository;
losing it makes the backups unrecoverable.

Install [october-bus-backup.service](october-bus-backup.service) and its
[timer](october-bus-backup.timer). Supply root-only `/etc/october-bus/backup.env`:

```text
PGHOST=<direct or session-pooler hostname>
PGPORT=5432
PGDATABASE=postgres
PGUSER=<Bus runtime database identity>
PGPASSFILE=/etc/october-bus/backup.pgpass
PGSSLMODE=verify-full
PGSSLROOTCERT=/etc/october-bus/postgres-ca.crt
RESTIC_REPOSITORY=<private off-host repository>
RESTIC_PASSWORD_FILE=/etc/october-bus/restic.password
# Provider credentials, when needed, belong here or in a supported credential file.
```

Keep `backup.pgpass` and `restic.password` mode 0600. Do not store a repository
password in its own backup. The backup includes the runtime environment (including
the Bus encryption key), so restrict backup access as tightly as live service
credentials. Provider passwords and the backup repository key need separate
operator recovery storage.

Run one backup manually and verify an isolated restore **before** enabling the
timer. It runs every six hours with up to five minutes of jitter. This is an
initial recovery-point target, not a guarantee: monitor completed snapshots and
alert if `last-success.json` in `/var/lib/october-bus-backup` is over seven hours
old. A nonzero Restic exit, incomplete dump, full disk or interrupted upload never
advances that timestamp. A snapshot on the same disk is a rehearsal, not disaster
recovery. Test restores regularly and after backup-format changes.

The backup process holds `attachments/.backup.lock` while it creates a consistent
schema-only PostgreSQL dump and copies the file directory into Restic. The lock
is persistent; never delete or replace it while either process is running.
Messages, heartbeats, tasks and file downloads continue. New file writes return
BACKPRESSURE and can be retried without being charged. Deletes revoke read access
immediately, while removal of their bytes waits for the next garbage collection.
This keeps every file referenced by the dump available throughout copying. A
crash releases the OS lock automatically. The job has a 30-minute upper bound;
check its duration and capacity before increasing workspace storage limits.

## Restore and rollback

Restore a specific completed Restic snapshot into a **fresh private directory**,
using `restic restore <snapshot-id> --target <directory>`. Restic preserves the
original absolute path beneath that directory. Locate `manifest.json` and
`bus.dump` in its snapshot staging directory; the manifest names the attachment
path and the SHA-256 of the matching executable. Verify the binary digest before
running it. Do not restore files over a live daemon's directory or open a restored
database with an unrelated release.

Provision an empty target schema and roles using bootstrap. Prepare the restore
list with `pg_restore --list bus.dump | python3 restore-list.py --quarantine`.
The [filter](restore-list.py) omits the schema creation entry (bootstrap already
created it) and, if present, the optional shared Auth membership trigger. All
other triggers and constraints remain. Shared Auth objects belong to the identity
provider's separate backup, so a private Bus recovery must not require them. Restore the
remaining list as the migration owner with `--single-transaction --exit-on-error
--no-owner --no-privileges --use-list <filtered-list>`. Apply runtime grants, then
run the matching binary's `database check`. No `--clean` or schema drop is needed.
Compare every live attachment's length and SHA-256 against restored metadata;
missing bytes are a failed restore even if PostgreSQL starts.

Keep the recovered service private. Apply
[database-recovery-fence.sql](database-recovery-fence.sql) before allowing
reconnection: a backup can predate a revocation. This disables restored workspaces
and invalidates device/MCP authority while retaining history and work. Review
memberships and operator suspensions changed since the backup,
and reconcile Stripe's current subscriptions/refunds before enabling payments.
For October's Supabase deployment, restore the shared identity project if needed,
then [reinstall the membership guard](supabase/restore-membership-guard.sql).
It refuses memberships for deleted or deleting accounts until they are reconciled.
Never enable recovered workspaces with that guard omitted.
An operator re-enables only reviewed workspaces, preserving prior suspensions.
Members reconnect their own machines/apps with fresh grants. Use explicit
connection replacement during device pairing/OAuth consent, or **Replace key**
for a manual MCP key, to retain the restored inboxes. Terminal sessions must
then resume their existing IDs; a new connection/session creates a new inbox.
See [connection recovery](../../docs/connect-sessions.md#recover-a-hosted-connection).
Confirm message/request
round trips and file downloads on a synthetic workspace before switching HTTPS.
Do not use a customer workspace for the acceptance test.

An application-only rollback may reuse the database only when its migration
history matches the older binary. Otherwise keep writers stopped, preserve the
failed deployment for investigation, and restore the matching database/files to
fresh locations. Never edit applied migration checksums or silently delete data
to make an older binary start. Restoration loses changes after the snapshot;
communicate the recovery point and preserve newer data for reconciliation.

## Shared October identity lifecycle

For October's Supabase deployment, apply the privileged
[account lifecycle integration](supabase/README.md) before allowing sign-ups.
It fences account deletion before other products' cancellation or data removal,
requires users to leave or close their Intercom workspaces, and prevents concurrent
membership creation for an account being deleted. Run its disposable database
rehearsal and deploy the matching website explanation before opening hosting.
Keep these shared Auth functions in the identity deployment's recovery procedure.

## Preserve older Bus connections during cutover

If this origin already serves operator-provisioned Bus connectors and laptop
bridges, use [Caddyfile.coexistence](Caddyfile.coexistence) after standalone
qualification. Replace only that site's block; preserve unrelated sites such as
the worker gateway. The configuration routes `obg_` connector keys at `/mcp` and
all `/bus/*` requests to the legacy gateway. New MCP keys, OAuth, account APIs and
protocol requests use the new service. Unauthenticated `/mcp` advertises its OAuth
metadata. The `/health/ready` endpoint now checks the standalone service; monitor
the legacy gateway on loopback separately while clients still depend on it.

Exact workspace asset paths take precedence over the marketing `/assets/*`
matcher. Marketing receives only GET/HEAD requests, with Authorization and Cookie
removed. The 17 MiB proxy limit allows a 16 MiB file upload; each runtime retains
its own smaller JSON limits. Validate the combined configuration and reload Caddy
only after both backends are healthy. Keep a private copy of the prior site block
for rollback. Test routing without touching a running site using
`python3 scripts/test-hosted-proxy.py` on a host with Caddy installed; it starts
three synthetic loopback backends and a temporary proxy, then stops them all.

## Backup retention

Use a dedicated private Restic repository for this deployment. After a successful
backup and restore rehearsal, install
[october-bus-backup-maintenance.service](october-bus-backup-maintenance.service) and
[its timer](october-bus-backup-maintenance.timer). The daily job checks repository
structure before expiring snapshots outside the newest snapshot's 30-day window
and reclaiming their unreferenced data. It groups by the deployment tag rather
than temporary dump paths or host names. New backups must keep running for this
window to advance; monitor both backup freshness and the maintenance unit's exit
status. Restic locking prevents overlapping backup/prune from modifying the
repository unsafely; a failed operation needs retry and investigation.

Deleted workspace content may remain in retained private backups for up to that
window plus scheduling delay. A restored service stays quarantined until newer
revocations/deletions are reconciled. A repository structure check does not read
all file content: run `restic check --read-data` periodically and perform an
isolated application restore at least monthly and after backup-format changes.
For a retired deployment, make an explicit retention/removal decision; a stopped
backup job cannot expire its own final snapshots. Monitor the host and backup
jobs externally; systemd exit status alone is not an on-call notification.
