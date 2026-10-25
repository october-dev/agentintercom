# October account lifecycle integration

The standalone daemon verifies external accounts without owning the identity
provider's schema. October's deployment uses the same Supabase Auth accounts as
its other products. Install [001_account_lifecycle.sql](001_account_lifecycle.sql)
as the project's database administrator before opening self-service hosting.
It is a separate, privileged integration, not an embedded Bus migration.

Prerequisites: standalone migrations 001–013, the `october_bus_migrator` and
`october_bus_runtime` roles, and October's existing `account_storage_objects(uuid)`
contract from the account deletion migration. The script checks that exact
contract before replacing it and refuses to overwrite an unfamiliar version.
Apply it once; record its checksum with the deployment. Reapply runtime grants
afterward. It does not grant the daemon access to Auth or another product's data.

The existing account-delete Edge Function calls `account_storage_objects` before
any billing cancellation, device revocation or file removal. That call now:

1. Refuses deletion while the account belongs to any Intercom workspace. Users
   leave shared workspaces; owners transfer ownership and leave, or delete the
   workspace in **Data & recovery**, which ends its own subscription.
2. Once no memberships remain, records account deletion intent under an Auth row
   lock. Concurrent workspace creation or invitation acceptance cannot join that
   identity afterward. The original storage-object query is otherwise unchanged.
3. A second trigger protects direct Auth administrator deletion too. No account
   can disappear while its workspace grants remain valid.

The shared website recognizes the exact `OCTOBER_BUS_WORKSPACES_REMAIN` conflict
and links users to their workspaces. No Desktop Bus implementation change is
required. Other clients see the same conflict and can direct users to the web.

An interrupted account deletion can be retried through the existing endpoint.
If an operator must **cancel** an unfinished account deletion, first confirm no
account-delete request is running and review whether its other steps already ran.
Only then remove that user's row from `october_bus.supabase_account_deletions`.
The Bus runtime cannot remove the fence. An actual Auth deletion removes it by
foreign-key cascade; a missing Auth user still cannot receive a membership.

Future changes to `account_storage_objects` must retain this preflight before
returning objects. Restoring or replacing shared Auth functions must requalify
this integration. The standalone backup covers the Bus schema, not these shared
Auth functions; maintain the shared database's own backup and deployment history.

Validate locally with `python3 scripts/test-supabase-account-lifecycle.py` using
`OCTOBER_BUS_TEST_POSTGRES_URL` pointing at a disposable loopback test cluster.
The script creates and drops a separate test database; it never contacts Supabase.
