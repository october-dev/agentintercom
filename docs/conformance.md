# Conformance profiles

The conformance runner checks Agent Intercom implementations and adapters through public interfaces. It emits JSON by default and exits nonzero after recording the first failed check. Use `--format text` for terminal output.

## Local runtime

Run the local runtime profile against the current daemon:

```bash
agentintercom-conformance --format text
```

Use `--address` with `OCTOBER_BUS_ADMIN_TOKEN` to target another daemon. Use `--admin-token-env` when the admin credential is stored under a different environment variable.

For an isolated run, let the runner start a daemon on a free loopback port:

```bash
agentintercom-conformance --start-runtime --format text
```

The runner uses a temporary data directory and removes it after stopping the daemon.

## MCP adapter

The MCP adapter profile starts an executable directly, without a shell. Each adapter argument is passed separately:

```bash
agentintercom-conformance \
  --profile mcp-adapter \
  --start-runtime \
  --adapter-command agentintercom \
  --adapter-arg mcp \
  --adapter-arg stdio \
  --format text
```

The runner gives the adapter an execution-bound agent credential and keeps registration and heartbeat outside the MCP process. Admin and scope credentials are removed from the adapter environment. The local runtime profile checks portable scope archives, identity, heartbeat, exact peer discovery, durable messaging, acknowledgements, requests, replies, idempotency, bounded context, shared tasks, human escalation, execution replacement, clean shutdown, and lease-expiry recovery.

The lease-expiry check takes about 30 seconds. Increase `--timeout` if the host is slow.

Passing this automated profile verifies the adapter command. It does not verify a named AI harness. Use the [harness verification runbook](../compatibility/RUNBOOK.md) with a released harness before adding compatibility evidence to the public registry.
