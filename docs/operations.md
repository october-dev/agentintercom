# Local runtime operations

October Bus runs as one local daemon. The reference profile binds to `127.0.0.1` and stores accepted work in SQLite.

For remote and shared deployments, see the [cross-machine scope architecture](architecture/cross-machine-scopes.md). A shared Bus service owns logical coordination state while each host retains authority over the executions it runs.

For an experimental authenticated HTTPS endpoint for connector evaluation, use the
[hosted MCP deployment guide](../deploy/hosted/README.md). It includes a restricted
gateway, operator-issued connector API keys, remote laptop bridges, Linux services,
and reverse-proxy configuration. It does not certify Muse or automatic session wake-up.

## Start and stop

```bash
october-bus start
october-bus status
october-bus doctor
october-bus stop
```

`stop` sends an authenticated shutdown request through the local endpoint. The admin token stays in the protected run file and is not passed on the command line.

Service supervisors can use `GET /health/live` for liveness and `GET /health/ready` for readiness. Readiness includes storage availability and returns HTTP 503 when storage cannot be reached. Health responses do not include database addresses or credentials.

Use `october-bus doctor --json` for machine-readable diagnostics. It reports versions, paths, process state, and endpoint health. It does not print credentials or message content.

## MCP over stdio

Create a local scope, then configure a host to start:

```bash
october-bus mcp stdio --scope my-project --agent reviewer
```

The bridge reads local discovery and the saved scope token, registers an execution, renews its lease and retires on EOF or failure. Use `harness config <host> --scope <id> --agent <id>` to render the correct host format with explicit paths. `link --scope <id> <a> <b>` links two registered agents. `doctor --harness <host> --scope <id> --json` probes local setup without invoking a model.

The argument-free form remains available inside `agent run`, which supplies `OCTOBER_BUS_ADDRESS` and `OCTOBER_BUS_AGENT_TOKEN`. Missing credentials/identity or mixed modes now fail. Independently launched editors should use configuration-only mode, not rely on a terminal's environment.

A host controller such as October Desktop can instead hand the bridge a private, renewable execution file: `mcp stdio --connection-file <path>` (or `OCTOBER_BUS_CONNECTION_FILE`). The same binary provides `mcp check` for bounded setup diagnostics and `hook` for Claude/Codex lifecycle reporting without a Node runtime. See [managed connections](managed-connections.md) and the [Desktop remote integration contract](desktop-remote-integration.md).

### Saved scope credentials

Local CLI create, successful import and token rotation save a raw scope credential under `DataDir/scopes/<sha256(scope-id)>.token`. Case-sensitive IDs remain distinct and filenames avoid Windows reserved names/characters. Files request mode 0600, directories 0700; Unix reads reject shared permissions and symlinks. Windows requires owner-restricted directory ACLs: Unix mode bits do not establish that guarantee. Same-user processes remain trusted.

Successful local CLI deletion removes the matching token file. Remote/API/SDK mutations cannot update another machine's cache. If a response or disk write is lost, repair permissions and rotate again using local admin authority. Stop/restart affected bridges after rotation. A token file alone is not a backup of collaboration data; a full database snapshot does not include these separate files. Keep backup access restricted and recreate cached tokens by rotation after restore when necessary.

Generated configurations contain no tokens but do contain executable paths; review them as code. Never import configuration from an untrusted repository automatically. Removing a host entry does not delete its durable scope data.

## Reaching /mcp from another machine

The daemon always binds `127.0.0.1`. Remote access needs a bridge or reverse proxy on the daemon's machine; the allowlist alone changes nothing about reachability.

`/mcp` additionally checks the request `Host`. Loopback Hosts pass at any port; anything else receives HTTP 403 `PERMISSION_DENIED`. This is DNS-rebinding protection. `/v1` has no such check.

Option A is an HTTP-aware proxy. Terminate the remote connection in a proxy that validates its own incoming `Host` and restricts clients, then configure it with:

```nginx
proxy_set_header Host 127.0.0.1:4765;
```

A proxy that rewrites every `Host` it receives removes the protection instead of enforcing it. A plain TCP bridge such as `socat` forwards the client's `Host` unchanged, which is why it is rejected.

Option B is to pass the real authority through the bridge and allowlist it exactly:

```sh
OCTOBER_BUS_ALLOWED_HOSTS=192.168.1.20:4765,bus.internal:4765 october-bus start
```

PowerShell uses the same setting:

```powershell
$env:OCTOBER_BUS_ALLOWED_HOSTS = "192.168.1.20:4765,bus.internal:4765"
october-bus start
```

The value is a comma-separated list of exact `Host` authorities, including the port as sent. Wildcards and suffixes are not supported.

Each entry gives up rebinding protection for that name, so list only authorities you control and pair remote exposure with authenticated, encrypted transport as described in the README under "Remote transport needs separate trust". This setting applies only to `/mcp`; other routes retain their existing authentication and authorization controls.

socat passes a backlog of 5 to listen(2) by default. This bounds queued, not-yet-accepted connections rather than established MCP concurrency; connection bursts may therefore be delayed or fail. Raise it with backlog=64 or use a reverse proxy. This tunes the bridge, not the Bus.

## Inspect message delivery state

Agents can inspect the durable delivery state of a message they sent or
received without opening SQLite or writing a client program. The command
requires the agent credential and never reveals message bodies or shared
context, only the receipt.

```bash
october-bus message receipt <message-id> [--json] [--address <addr>]
```

The credential is read from `OCTOBER_BUS_AGENT_TOKEN`. The daemon address is
resolved from `--address`, then the `OCTOBER_BUS_ADDRESS` environment
variable, then the local run file. The output shows the current delivery
state plus any timestamps that have been recorded (`accepted`, `delivered`,
`acknowledged`, `replied`) and, when present, the linked response message
ID. Use `--json` for a stable machine-readable form.

## Inspect scope agents

A scope owner can list the agents registered in a collaboration scope without
calling the HTTP API directly. The command exposes agent metadata only. It does
not expose credentials or message contents.

```bash
october-bus agent list [--json] [--address <addr>]
```

The credential is read from `OCTOBER_BUS_SCOPE_TOKEN`. The daemon address is
resolved from `--address`, then `OCTOBER_BUS_ADDRESS`, then the local run file.
The output includes each agent's id, display name, lifecycle, readiness,
reachability, capabilities, and last update time. Results are sorted by agent
id. Use `--json` for machine-readable output.

## Project task board

A scope owner can add work and inspect dependency-ready tasks:

```bash
export OCTOBER_BUS_SCOPE_TOKEN=<scope-token>
october-bus task add --title "Review checkout retries" --description "Check idempotency and error handling."
october-bus task list --ready
```

Claims, progress updates, release, and completion require an execution-bound agent credential. Task listings include the most recent progress, notes, and blockers so a later agent or user can continue from durable state.

## Storage and retention

The runtime accesses durable state through the [storage backend contract](architecture/storage-backends.md). SQLite remains the default and currently supported backend.

Scope owners can inspect storage growth without reading message, task, or escalation content:

```bash
export OCTOBER_BUS_SCOPE_TOKEN=<scope-token>
october-bus scope storage
```

The summary groups record counts and estimated payload bytes by state. It also reports the oldest state timestamp. Payload sizes are estimates and do not include SQLite indexes or other database overhead.

Retention is explicit and keeps indefinite storage as the default. First run a dry run:

```bash
october-bus scope prune --before 2026-08-01T00:00:00Z
```

Pass `--yes` to remove the reported records in one transaction. Only terminal messages, completed tasks, resolved escalations, and old scope events can be removed. Requests and responses are removed together. Work with an outstanding delivery, reply, task, dependency, or human obligation is preserved.

Pruning scope events can make an old event cursor incomplete. Event clients receive `resyncRequired` and must rebuild their projection from the resource APIs before continuing.

Agent Card publications and remote principals are configuration records and are not removed by retention. Disable a publication to stop serving its public card and reject its principals. Disable an individual principal to suspend only that caller.

Inbound A2A work is limited independently for each remote principal. The defaults are 1,000 unfinished messages and 16 MiB of unfinished text. Set `OCTOBER_BUS_A2A_PRINCIPAL_MESSAGE_LIMIT` and `OCTOBER_BUS_A2A_PRINCIPAL_BYTE_LIMIT` before starting the daemon to choose stricter limits. The message limit must be from 1 through 9,999. The byte limit must be from 1 through 655,294,464.

Use the scope client `ListA2APrincipalUsage` method or `GET /v1/a2a/principals/usage` to inspect current usage. The result contains identifiers, counts, bytes, and limits, but no message content.

Output streams apply their own bounded retention on every publication. The default is 1,000 values and the owner can select 1 through 10,000 when creating a stream. Removing a stream also removes its values and scoped principals.

Browser output access is disabled unless the request origin is explicitly configured. Set a comma-separated exact allowlist before starting the daemon:

```sh
OCTOBER_BUS_ALLOWED_ORIGINS=http://127.0.0.1:8080,https://dashboard.example october-bus start
```

PowerShell uses the same setting:

```powershell
$env:OCTOBER_BUS_ALLOWED_ORIGINS = "http://127.0.0.1:8080,https://dashboard.example"
october-bus start
```

The Bus never accepts output credentials in query strings. A server-to-server request without an `Origin` header is not affected by browser CORS configuration.

Choose a cutoff older than the longest client retry window you support. Removing a message also removes its idempotency-key binding.

## Backup, restore, and migration

Stop or disconnect every agent in a scope before exporting it:

```bash
october-bus scope export --id my-project --output my-project.bus.json
```

Archive files contain message bodies, context, task details, escalation answers, and output values. They do not contain reusable Bus credentials, but they can still hold sensitive project data. The CLI creates a new archive with user-only permissions and refuses to overwrite an existing file.

Import the archive into another compatible runtime:

```bash
october-bus scope import --input my-project.bus.json
```

The command prints the new scope token only on the first successful import. Store it securely. Imported agents are offline, active task claims are open, and Agent Card publications are disabled. Register agents again and review publications before enabling them.

For a remote runtime, set `OCTOBER_BUS_ADMIN_TOKEN` and pass `--address`. Importing the exact same archive again is safe and does not duplicate state.

## Default paths

| Platform | Data directory | Runtime directory |
| --- | --- | --- |
| Windows | `%LOCALAPPDATA%\October Bus` | Per-user temporary directory |
| macOS | `~/.local/share/october-bus` | Per-user temporary directory |
| Linux | `$XDG_DATA_HOME/october-bus` or `~/.local/share/october-bus` | `$XDG_RUNTIME_DIR/october-bus` or a per-user temporary directory |

Set `OCTOBER_BUS_DATA_DIR` and `OCTOBER_BUS_RUNTIME_DIR` to override these paths. The data directory contains `bus.db`. The runtime directory contains the current lock and credential-bearing run file.

Do not share the runtime directory. OS-held locks protect both runtime discovery and the canonical database path. Lock files intentionally remain after shutdown; their existence or age does not indicate ownership. Do not remove them while any daemon is running. Symlink aliases resolve to one database lock; hard-linked databases and network filesystems are unsupported.

Startup also refuses a run file naming a possibly live process, to avoid taking over from a legacy daemon that did not use OS locks. PID reuse or permission-denied process inspection can conservatively trigger this refusal. Verify that the recorded owner and all old daemons have stopped before archiving a stale run file or choosing a fresh runtime directory; never remove a live owner's lock to bypass the check.

## Supervision

The daemon handles interrupt and termination signals and shuts down active HTTP requests before closing SQLite. A service supervisor should restart unexpected exits with bounded backoff.

The repository does not yet ship platform service definitions. Keep credentials out of service arguments and logs when adding one.

## Recovery

Accepted messages and active tasks survive a normal daemon restart. If startup reports an invalid database schema or integrity problem, preserve the database before attempting repair. Do not delete a database to get past a version error.

### Upgrade from rc.4

Stop the old daemon and all harnesses before changing binaries. The next runtime supports the released schema 2 from v0.1.0-rc.4. On first open it creates a consistent SQLite backup beside the database named `bus.db.schema2-backup-*`, then migrates all tables in one transaction to schema 11. Existing identities, tokens, messages, outstanding requests, claims, dependencies, descriptions, and escalations are preserved. Legacy tasks gain a stable generated title; their full descriptions remain intact. Execution leases still use their original expiration times.

Migration failure or process death before commit leaves schema 2 intact. The backup is retained even if migration fails. Schemas 3–8 were unreleased intermediates and are intentionally rejected; no conversion is guessed. Test a copy before upgrading a valuable installation.

For rollback, stop the new daemon and harnesses, preserve its complete data directory separately, and place a copy of the schema-2 backup in a **fresh private data directory** as `bus.db`. Point the rc.4 binary at that directory. Never combine the restored file with the upgraded database's WAL/SHM files. The old binary must not be started against the upgraded database; it rejects schema 11. Rollback discards work accepted after the backup—preserve the new data for reconciliation.

### Online database snapshots and restore

```sh
october-bus backup --output /private/backup-location/bus-snapshot.db
```

Choose a private, existing directory and a new output filename. The CLI refuses overwrite, streams the snapshot to a mode-0600 file, syncs it, and removes incomplete output on failure. Unlike portable scope JSON, snapshots include every scope, credentials/hashes, execution leases, reservations, event history and rate state. Protect them as sensitive data and encrypt off-machine copies. The endpoint is `GET /v1/admin/backup`; only an admin can use it. Snapshot creation may queue other SQLite operations; the five-minute transfer deadline is not a heartbeat-latency guarantee.

To restore: stop the daemon and all managed harnesses; retain the original data directory; put the snapshot at `bus.db` in a fresh owner-only directory; point `OCTOBER_BUS_DATA_DIR` there; and start one runtime of the same or a supported newer schema. Do not copy stale WAL/SHM files alongside it. Check `doctor`, scope listings and representative receipts/tasks. Restored credentials and leases retain their authority, so use scope-token rotation before reconnecting if credentials were compromised or old executions might still run.

### Lost or compromised scope credentials

```sh
october-bus scope list
october-bus scope rotate-token --id my-project
```

Rotation returns a new owner token, retires all executions, releases claims/reservations and disables all scoped A2A/output credentials in that scope. Save the new token, re-register workers, and rotate/re-enable only reviewed scoped credentials. If the response is lost, rotate again. Listing makes an unknown ID recoverable after a lost create response. Portable-import retries still return no old token; rotate the listed imported scope to recover authority.

An admin may explicitly abandon a scope:

```sh
october-bus scope delete --id my-project --confirm my-project
```

This permanently deletes that scope and its dependent data. Back up first; recovery requires a snapshot/archive. The command is not run automatically by cleanup or retention.

### Operating bounds

Ordinary HTTP admission is capped at 256 requests; heartbeat/retirement/health/shutdown have 32 separate slots, and credential classification (`GET /v1/credential`) has 32 more. Inbox waits are capped at 32 per agent and 128 per scope, and return backpressure rather than accumulating indefinitely. Across remote principals, queued/reserved/delivered A2A messages are limited to 5,000 slots and 64 MiB of bodies per scope, preserving half the active backlog slots for local senders. Per-principal unfinished-work quotas still apply. These are ceilings, not a supported hosted-load claim. Retained history still needs an operator retention policy and disk monitoring.

Legacy task listing rejects scopes with more than 10,000 tasks. Traverse history with `october-bus task list --limit 100 [--after <nextCursor>]`, Go `TaskPage`, or TypeScript `taskPage`. Portable exports have a conservative preflight budget and never successfully return an archive their importer rejects. Use the full database snapshot for larger state.

Pruning keeps the dependency closure of every retained task. A2A task/correlation history is pruned only as a whole terminal unit whose Bus messages are all eligible. Its client-message deduplication history ends when that unit is pruned. Unanswered delivered requests stay protected.

Do not blindly retry ambiguous task creation, escalation creation or completion outcomes: inspect the task/escalation list or receipt first. Message idempotency keys are supported; those other mutations do not gain message-style idempotency from this hardening work.

Schema 9 also upgrades to schema 11. It creates a `bus.db.schema9-backup-*` snapshot, rebuilds the message participant constraints transactionally and checks all foreign keys before commit. Restore that snapshot in a fresh private directory with the matching older binary to roll back.

Schema 10 to 11 adds native wake tables in one transaction. Back up before deployment. Existing messages and credentials are not rewritten, and a failed addition rolls back the schema version.
