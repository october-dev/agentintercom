# Hosted MCP connector pilot

For the standalone PostgreSQL workspace service, use
[the deployment and recovery runbook](STANDALONE.md),
[hosted workspaces](../../docs/hosted-workspaces.md),
[hosted billing](../../docs/hosted-billing.md) and
[october-bus-hosted.service](october-bus-hosted.service). The instructions below
preserve the older operator-provisioned SQLite gateway deployment.

Deploy the Bus on a Linux server with systemd and an existing HTTPS reverse proxy.
Use **Existing MCP** for a Muse application: the connector gets the Bus's agent
tools directly, while registration, heartbeat, and credential handling remain
outside the model loop. The HTTP API is also available for approved remote hosts.

The recommended endpoint is **`https://bus.october.dev/mcp`**. This is a proposed
deployment address, not a claim that it is live. Your product page remains
`https://www.october.dev/october-bus`. The existing website's `/mcp` serves themes;
do not replace it or use its OpenAPI document for the Bus connector.

## What this implements

```text
Muse / connector client -- HTTPS + connector API key --> gateway /mcp
                                                          |
                                                    managed execution
                                                          |
                                                   private Bus + SQLite
                                                          |
local harness <-- stdio bridge -- outbound HTTPS --> gateway /bus/*
```

- The daemon listens on `127.0.0.1:4765`; the gateway on `127.0.0.1:8787`.
- The reverse proxy owns HTTPS and preserves the public Host header.
- A connector API key is stored as a SHA-256 digest in gateway configuration.
  Each key maps to one scope and dedicated connector agent. The gateway owns
  that execution's registration, heartbeat, and retirement; Muse never receives
  scope/admin credentials. The connector execution reports lifecycle `idle`
  with `ready: true`, meaning the gateway will accept and hold work; it never
  asserts that a model is awake. `/mcp` accepts `POST` only: the daemon serves
  MCP stateless with JSON responses, so there is no SSE listen stream or
  session to delete. The connector's agent ID (default `muse`) is reserved for
  the gateway; a laptop bridge registering the same ID replaces the connector
  execution and takes that connector offline until the gateway restarts.
- Remote laptop bridges own their own executions. They read scope credentials
  from private files and expose only agent tools over stdio to the harness.
- Only explicitly allowed agent/scope routes are public under `/bus`. Scope
  creation, admin operations, backups, storage pruning, and publication/principal
  administration remain local to the server.
- Messages and tasks are durable. The gateway is stateless apart from managed
  executions; restart replaces its execution without discarding queued messages.
- An unavailable individual connector returns 503 without disabling other scopes.
  If all connector executions end, the process exits for systemd to restart it.
  `gateway start` waits up to 60 s for the daemon's readiness probe before
  binding, so the start order is daemon, then gateway, then laptop bridges.

This is an **experimental, operator-provisioned pilot**, not a self-service
multitenant cloud or a verified Muse integration. Use one scope per person or
trusted team; task state is shared within that scope. The gateway does not add
OAuth, arbitrary terminal control, automatic session attachment, model turn
wake-up, or push notifications to Muse. Harnesses still need to consume their
inboxes; delivery does not mean execution. Existing sessions may need their
harness's supported MCP reload/resume flow. Exact released-harness and Muse
testing remain required before claiming those integrations work.

## 1. Build the server binary

Use a reviewed commit containing this deployment directory. For pre-merge testing,
use the feature branch in a staging environment; a pushed branch is not a release.
The build needs Go 1.27, but the deployed binary does not need Go or Node.

From the repository root on the build machine:

```sh
mkdir -p dist/hosted
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath \
  -o dist/hosted/october-bus ./cmd/agentintercom
```

Use `GOARCH=arm64` for an ARM server. Copy that binary and this directory to the
server. If building directly on Linux, the same command works. Install it:

```sh
sudo install -m 0755 dist/hosted/october-bus /usr/local/bin/october-bus
# Create this dedicated system account once, if it does not already exist.
sudo useradd --system --home-dir /var/lib/october-bus --shell /usr/sbin/nologin october-bus
sudo install -m 0644 deploy/hosted/october-bus.service /etc/systemd/system/
sudo install -m 0644 deploy/hosted/october-bus-gateway.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now october-bus.service
```

The service creates private state and runtime directories. Confirm readiness:

```sh
sudo -u october-bus env \
  OCTOBER_BUS_DATA_DIR=/var/lib/october-bus \
  OCTOBER_BUS_RUNTIME_DIR=/run/october-bus \
  /usr/local/bin/october-bus doctor --json
```

Do not open ports 4765 or 8787 in the firewall. Only your reverse proxy's HTTPS
port is public (and port 80 if its certificate/redirect configuration needs it).
This requires a server that can run persistent processes, not static hosting.

## 2. Provision an isolated pilot scope and connector key

Run these on the server. Scope creation saves its credential privately; redirect
its printed result so credentials do not enter terminal logs.

```sh
sudo -u october-bus env \
  OCTOBER_BUS_DATA_DIR=/var/lib/october-bus \
  OCTOBER_BUS_RUNTIME_DIR=/run/october-bus \
  /usr/local/bin/october-bus scope create muse-pilot >/dev/null

sudo -u october-bus env \
  OCTOBER_BUS_DATA_DIR=/var/lib/october-bus \
  OCTOBER_BUS_RUNTIME_DIR=/run/october-bus \
  /usr/local/bin/october-bus gateway init \
  --public-url https://bus.october.dev --scope muse-pilot --agent muse \
  --config /var/lib/october-bus/gateway.json \
  --key-file /var/lib/october-bus/muse.key

sudo systemctl enable --now october-bus-gateway.service
```

The init command refuses to overwrite files. It prints paths, never the API key.
Use a dedicated agent ID for the connector; do not register a laptop under `muse`.
The API key in `muse.key` grants the connector agent's authority only. Supply it
to Muse through its private credential mechanism as `Authorization: Bearer <key>`;
never put credentials in the endpoint URL, issue, PR, or application prose.

For additional trusted users, create separate scopes and append client entries
to the private JSON config, using `gateway key --output <new-private-file>` to
generate a key and its digest. Restart the gateway to apply configuration.
Keys and scope/agent pairs must be unique. Keep the config mode `0600`.

## 3. Add DNS and HTTPS

Point the DNS A record for `bus.october.dev` to this server's public IPv4 address.
Add an AAAA record only if IPv6 actually routes to the same HTTPS service.
Keep the existing website's DNS and routes intact.

For **Caddy**, add the site block in [Caddyfile](Caddyfile) to your existing
configuration, validate it, then reload. Do not replace the website configuration.
Caddy can obtain and renew the certificate once DNS and ports are correct.

```sh
sudo caddy validate --config /etc/caddy/Caddyfile
sudo systemctl reload caddy
```

For **nginx**, adapt [nginx.conf.example](nginx.conf.example), provision the
certificate with your existing ACME tooling, run `sudo nginx -t`, and reload
nginx. Use whichever proxy already runs on the server; do not run both on 443.

The gateway requires `Host: bus.october.dev` (including an explicit port if the
configured public URL includes one), so preserve it when proxying. Request bodies
are capped at 1 MiB. Proxy timeouts exceed the Bus's 25-second inbox wait. Exclude
this subdomain from caching and interactive browser challenges. Preserve
`Authorization`, MCP headers, and response streaming. Do not log authorization
headers or request/response bodies.

## 4. Connect agents from the laptop

Install a binary built from the same commit on the laptop. The new remote flags
are not available in older published packages.

The scope owner securely transfers the `muse-pilot` scope token to a private file
on the laptop. It is stored server-side at
`/var/lib/october-bus/scopes/<sha256-of-scope-id>.token`. Obtain the filename using
`printf '%s' muse-pilot | sha256sum` on Linux; this hashes the public scope ID, not
the secret. Transfer over SSH to a file, without displaying its contents:

```sh
umask 077
mkdir -p "$HOME/.config/october-bus"
ssh YOUR_SERVER 'sudo cat /var/lib/october-bus/scopes/SCOPE_HASH.token' \
  > "$HOME/.config/october-bus/muse-pilot.token"
chmod 600 "$HOME/.config/october-bus/muse-pilot.token"
```

Replace `YOUR_SERVER` and `SCOPE_HASH`. Treat the scope token as owner authority:
give it only to the person operating that scope, not to Muse or an unrelated user.
Keep the file outside repositories and model-accessible project directories.

Configure each supported harness to launch this MCP stdio server, substituting
absolute paths and a unique agent ID for each concurrent session:

```json
{
  "command": "/absolute/path/to/october-bus",
  "args": [
    "mcp", "stdio",
    "--remote", "https://bus.october.dev/bus",
    "--scope-token-file", "/absolute/private/path/muse-pilot.token",
    "--agent", "claude-website",
    "--name", "Claude website",
    "--connect-to", "muse"
  ]
}
```

Place this entry inside your harness's documented MCP configuration format; it
is not a complete configuration file. For a second session, use e.g.
`codex-backend` and `Codex backend`. The gateway must already be running because
`--connect-to muse` links to an existing registered identity. Add another
`--connect-to <peer-id>` for explicitly authorized agent-to-agent communication.

The bridge loads the credential, registers, heartbeats, and retires on close or
lease failure. It ignores inherited addresses; remote and local modes cannot be
mixed. Duplicate agent IDs replace the previous execution. No inbound laptop
ports or tunnel are required. The host keeps its tools, workspace permissions,
provider accounts, and responsibility for approvals.

Tell the receiving agent to call `check_inbox` between work steps (optionally
with `waitMs: 10000`), acknowledge work, and reply with the original request ID.
An idle model that is not consuming its inbox will not start work automatically.

## 5. Verify before entering the URL into Muse

```sh
curl --fail --silent --show-error -o /dev/null -w '%{http_code}\n' \
  https://bus.october.dev/health/ready
# Expected: 204
```

Copy `muse.key` privately to the machine used for checking, with mode `0600`.
The optional Node 20+ checker initializes MCP and lists tools without running
agent work or printing credentials:

```sh
node scripts/check-hosted-mcp.mjs https://bus.october.dev/mcp /private/path/muse.key
```

Then perform a real round trip in the target client: `list_peers`, `message_peer`
with `mode: request` and a fresh idempotency key, receiver inbox/ack/reply, and
connector inbox/ack/receipt. Test approvals, offline laptop behavior, reconnection,
and a gateway restart. A successful tools list alone is not session control or
Muse compatibility evidence.

Application fields after verification:

| Field | Value |
| --- | --- |
| Connection type | Existing MCP |
| Hosted MCP endpoint | `https://bus.october.dev/mcp` |
| API/MCP documentation | `https://github.com/october-dev/october-bus/blob/main/spec/0.1/mcp.md` after merge; link the reviewed commit before merge |
| Authentication | API keys, sent as Bearer credentials; no OAuth/PKCE claim |
| OpenAPI | Not needed for the MCP option |

Suggested access requirements:

> Operator-provisioned pilot access with a private connector API key. Users run
> an Agent Intercom bridge on their computer and explicitly connect supported agent
> sessions. Their computer and receiving agents must be online and consuming
> messages for work to execute. Existing coding-tool accounts, subscriptions, and
> approvals still apply. Each connector is limited to its provisioned scope and
> linked peers. The gateway admits requests from three bounded pools: 32 slots
> for credential checks and health probes, 32 for validated heartbeat and
> retirement, and 128 shared by validated bridge requests and connector requests
> (16 per connector). A request without a valid credential never occupies the
> heartbeat or shared pools, and occupies a credential-check slot only for one
> header-only loopback call that the client cannot prolong. These sizes are
> bounds, not isolation guarantees: heartbeats compete for credential checks
> with all unauthenticated traffic, and a holder of a valid credential can
> still exhaust the shared pool. Bus request, queue, and storage limits also
> apply. OAuth, self-service onboarding, and automatic session wake-up are not
> implemented by this gateway.
> Muse compatibility remains to be verified.

The exact authentication mechanisms Muse accepts for a given review still need
to be tested. The form's API-key option is not evidence of end-to-end compatibility.

## Operations, rotation, and recovery

- If the proxy service has no restart policy, install
  [caddy-restart.conf](caddy-restart.conf) as
  `/etc/systemd/system/caddy.service.d/50-october-restart.conf` and run
  `systemctl daemon-reload`. Check `systemctl show caddy --property=Restart`;
  applying this policy does not require restarting the running proxy.
- After verifying a separate public-key SSH connection, the optional
  [sshd-key-only.conf](sshd-key-only.conf) disables password and keyboard-interactive
  login. Back up existing configuration, validate with `sshd -t`, inspect the
  effective settings with `sshd -T`, reload `ssh`, and verify a new connection
  while retaining the original session. Authentication hardening is separate
  from provider firewall rules and off-host backups.
- Inspect `systemctl status october-bus october-bus-gateway` and their journals.
  Probe `/health/live` for process liveness and `/health/ready` for all connector
  sessions plus storage availability. A partial connector failure leaves other
  scopes serving but marks readiness unavailable until repaired/restarted.
- Rotate a connector key with `gateway key --output <new-private-path>`, replace
  only that client's `apiKeySha256` with the printed digest, and restart the
  gateway. The old key stops working after restart. Give the new key through the
  private credential channel, then remove the old plaintext key file. Removing
  a client entry and restarting revokes that connector's access. An already
  accepted task is durable; key revocation is not task cancellation.
- Rotate a leaked scope token with the existing `scope rotate-token --id` command
  as the service user using the two directory environment variables above.
  This revokes all current executions in that scope. Restart the gateway and
  update/restart authorized laptop bridges with the replacement token.
- Back up through the existing local `october-bus backup --output <new-path>`
  command with the same service-user environment; also back up private scope
  files and gateway configuration/key files through your encrypted backup policy.
  Do not copy a live SQLite main file without its WAL. See
  [operations](../../docs/operations.md) for database backup and recovery.
- Operate one daemon and one gateway process for this deployment. SQLite is the
  single writer; do not run replicas against independent copies. Monitor disk
  use and apply the existing explicit retention controls locally. No throughput
  or uptime guarantee is established by these request ceilings.
- Updating the binary requires stopping the gateway, stopping the daemon,
  installing the reviewed build, then starting the daemon and gateway. Take a
  backup first; database migrations can make an older binary unsuitable for
  rollback. Restart laptop bridges after a daemon restart or execution expiry.
- To disable remote access, stop the gateway and remove its proxy site. Keep
  the Bus state unless you intend to delete the durable coordination history.

The server operator can read stored shared messages; this is HTTPS transport
encryption, not end-to-end encryption that hides data from the server or Muse.
Only share the bounded context needed for each request.
