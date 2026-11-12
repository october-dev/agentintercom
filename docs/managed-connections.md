# Host-managed MCP connections

October Desktop and other host controllers can run the native Bus helper against
their existing MCP authority. The helper does not start another daemon, create a
scope, or require a copy of the controller's administrator credential. One binary
provides the MCP bridge, the lifecycle hook and a setup check:

```sh
agentintercom mcp stdio --connection-file /absolute/private/connection.json
agentintercom hook [--connection-file <path>] <event> [<flavor>]
agentintercom mcp check [--connection-file <path>] [--json]
```

`--connection-file` may be omitted when `OCTOBER_BUS_CONNECTION_FILE` names the
file. A managed launcher exports that variable into the execution's environment
for harnesses whose configuration is shared between executions (Codex reads one
`hooks.json`/`config.toml`; Claude can take per-launch settings). The variable
carries a path, never a credential. Without either, `hook` exits silently and
`mcp stdio`/`mcp check` refuse; no empty tool list is served.

## Connection file

This mode is intended for managed Unix workers. The controller provisions a real
directory with mode `0700` and a regular file with mode `0600`. It writes a new
file in that directory and atomically renames it over the old one when renewing
the connection. The file has this shape (placeholders are not usable credentials):

```json
{
  "version": 1,
  "endpoint": "http://127.0.0.1:43120/mcp",
  "httpHost": "127.0.0.1:43121",
  "scopeId": "workspace-id",
  "agentId": "canvas-node-id",
  "executionId": "execution-id",
  "agentToken": "<execution MCP credential minted by the authority>",
  "hookToken": "<execution lifecycle credential minted by the controller>",
  "expiresAt": "<RFC3339 expiry chosen by the controller>"
}
```

| Field | Required | Meaning |
| --- | --- | --- |
| `version` | yes | Always `1`. Unknown fields refuse the file. |
| `endpoint` | yes | The authority's `/mcp` URL: HTTPS, or HTTP on a literal loopback IP. No userinfo, query or fragment. Redirects are refused. |
| `gateway` | no; present for Saturday cloud workers | The HTTPS origin bound to this connection, with no userinfo, query, fragment or path other than `/`. Its scheme and host (including any explicit port) must match `endpoint`. It never overrides the endpoint or selects a transport. |
| `httpHost` | no | Loopback-only HTTP `Host` override: same literal IP as `endpoint`, different port. A TCP/SSH forward does not rewrite `Host`, so this carries the authority's real port across a tunnel port. Never for HTTPS, never another name. |
| `scopeId`, `agentId`, `executionId` | yes | Pin the execution this file belongs to. For Desktop Core they are the canvas, node and terminal epoch (`launch`). They fence renewal; the authority still authenticates every request. |
| `agentToken` | yes | The execution's MCP credential: 32 random bytes, unpadded base64url (43 characters). Sent as `Authorization: Bearer`. |
| `hookToken` | no | The execution's lifecycle credential in the same format, used only by `agentintercom hook` as `X-October-Bus-Token`. When absent, hooks report nothing. Requires this helper version or newer; older helpers refuse a file containing it. |
| `expiresAt` | yes | Controller-chosen expiry. An expired file refuses every request until renewed. |

Both credentials leave the controller only as per-execution grants. Never write a
scope token, the controller's administrator credential or its process-lifetime
hook secret into this file or the agent's environment. Unix private files are not
a boundary against the same OS user. Windows file ACL qualification is not
implemented for this mode; existing local discovery and managed-environment modes
are unchanged.

Saturday's controller writes the same record with `gateway` set to its HTTPS
origin, `endpoint` set to
`https://<gateway>/api/compute/workers/<environment-id>/mcp`, and `executionId`
set to `managed:<environment-id>:<generation>`. Both execution tokens are present;
`httpHost` is omitted. The helper uses that existing HTTPS route. Hooks use the
same execution prefix with `/mcp` replaced by `/hook/<route>`. This requires no
WebSocket mode, scope credential or additional Bus authority. Arbitrary unknown
connection fields still fail validation.

## Renewal, retirement and one record per run

The bridge rereads the file before every HTTP request, including cancellation.
Only `agentToken`, `hookToken`, `expiresAt` and the loopback `httpHost` port may
change while a bridge lives. Changing `endpoint`, `gateway`, `scopeId`, `agentId` or
`executionId` retires the bridge: it refuses until the controller starts a new
bridge for the new execution. Removing the file withdraws the connection the
same way. Missing, expired, malformed, shared or symlinked files refuse requests
without falling back to an old token.

The controller keeps exactly one connection record per run. It must verify the
physical execution before renewing access, revoke its grant when that execution
ends, and never put a replacement execution's credentials under the old labels.
Public Bus execution registration semantics are unchanged: a newly registered
execution requires a new bridge. This does not add a same-execution token-rotation
API to the standalone daemon.

## Transport recovery without replay

A transport refusal makes the MCP SDK close its upstream session permanently.
The bridge establishes a fresh upstream session for the **next** tool call and
never resends the failed call: losing a response does not prove the mutation
failed to commit. The decision is made from what the HTTP layer observed:

- A tool-level error answered on a live session (HTTP 2xx) passes through; the
  session stays.
- Cancelling a call, for example a long human wait, sends the protocol
  cancellation and keeps the session for concurrent and later calls.
- Any HTTP-level failure drops the session. That includes a JSON-RPC error body
  delivered on HTTP 400, which the SDK treats as a connection failure; keeping
  that session would strand the worker.

## Diagnostics

Stdout stays reserved for MCP (`mcp stdio`), the hook's context output (`hook`)
or the requested report (`mcp check`). Failures print one stderr line per class
transition, then one line on recovery. The classes are:

| Class | Meaning |
| --- | --- |
| `invalid-configuration` | Missing path at startup, shared or symlinked file, malformed fields, unsupported endpoint or hook event. |
| `expired-credential` | `expiresAt` has passed; the controller must renew. |
| `retired-execution` | The file was withdrawn or its labels changed, or the authority ended the execution while a request was pending. |
| `unreachable` | The endpoint did not answer (dial, TLS, timeout). |
| `refused-credential` | HTTP 401/403, or an `UNAUTHENTICATED`/`PERMISSION_DENIED` envelope. |
| `authority-unavailable` | The route answered but the authority is not ready (5xx, 429, `CORE_NOT_READY`). |
| `session-lost` | The upstream MCP session ended (HTTP 404); the next call reconnects. |
| `protocol` | Any other HTTP failure, including a redirect. |

Messages are fixed sentences. Credentials, request bodies and authority responses
are never printed.

`mcp check` reads the file and sends one JSON-RPC `ping` with the execution
credential. A ping is not an MCP `initialize`, so it cannot count as the harness
connecting or mark an agent ready, and it never reserves or drains inbox messages.
`ok` requires a successful JSON-RPC ping result with the matching request ID.
HTTP 400, an error envelope or an HTML page cannot establish admission; an
unrecognized refusal reports `protocol` without guessing from the response text.
`ok` means the route answered and the credential was accepted; agent readiness
remains the controller's own process evidence. `--json` prints the class,
endpoint, labels, expiry and whether a hook credential is present, never tokens.

## Lifecycle hook

`agentintercom hook <event> [<flavor>]` is a Node-free port of Desktop's
`bus-hook.mjs` for the `claude` (default) and `codex` flavors. It reads the
harness event JSON on stdin and reports to the controller's `/hook/*` routes on
the endpoint's origin, with `X-October-Bus-Token: <hookToken>`,
`X-October-MCP-Capability: <agentToken>` and the optional `httpHost` override.
Neither MCP nor hook requests send `Origin` or `X-October-Caller-Pid`: a remote
process ID is not a local Desktop identity, and its remote listener rejects it.
Bodies carry `canvas`=`scopeId`, `node`=`agentId` and
`launch`=`executionId`. The mapping is Desktop-specific by design; it does not
redefine the public Bus protocol, whose `/v1` and `/mcp` surfaces are unchanged.

| Event | Flavors | Routes | Stdout |
| --- | --- | --- | --- |
| `session-start`, `session-end` | claude, codex | `POST /hook/session` (`live`/`offline`, session id, transcript, agent, cwd). Claude without a flavor also pulls `GET /hook/pre-prompt?event=session-start`. | Claude: `{"hookSpecificOutput":{"hookEventName":"SessionStart","additionalContext":…}}` when context was staged. |
| `pre-prompt` | claude, codex | `POST /hook/notify` (turn boundary, `requestId`, `providerTurnId`), then `GET /hook/pre-prompt`; after stdout accepted the bytes, `POST /hook/inbox-ack` and `POST /hook/context-ack`. | Claude: raw text. Codex: `UserPromptSubmit` envelope. |
| `stop`, `stop-failure` | claude, codex | `POST /hook/stop` with the last assistant excerpt, `outcome: failed` for failures, `providerTurnId`. Subagent and sidechain stops are ignored. | none |
| `notify`, `permission-request`, `post-tool-use`, `post-tool-use-failure`, `ask-user-question`, `ask-user-question-resolved` | claude (codex uses `notify` for PermissionRequest) | `POST /hook/notify` with the same `notificationType`, `needsInput`, `toolName`, `toolUseId` and messages as the Node hook. | none |

Other flavors and events are refused on stderr without contacting any authority.
A hook never fails the harness: it exits 0, finishes within four seconds, and
acknowledges a pulled receipt only after stdout accepted it. That proves native
handoff, not model comprehension. Hook traffic runs on the endpoint's origin
with `/mcp` replaced by `/hook/...`. Desktop Core and Saturday's compute gateway
serve these execution routes; the standalone hosted Bus gateway does not.

## Authority behavior

Desktop Core accepts a standard execution Bearer on `/mcp` and derives the canvas
and node from its private registry. Conflicting legacy routing headers refuse.
Core administrator/forwarding credentials cannot impersonate an execution. Core
answers refusals with HTTP 400 and a JSON envelope; the helper classifies on the
envelope's error code. This uses the existing Desktop ledger and tools; the
standalone Bus `/v1` service is not installed as a second authority. Local
manual-shell admission and physical readiness checks still apply.

The standalone Bus daemon and the [hosted gateway](../deploy/hosted/README.md)
accept the same bridge for their own executions; for those, the controller is
whichever operator process registered the execution and minted `agentToken`.

This connection support alone is not a complete remote-host adapter. SSH reach
supervision, host process evidence, safe input delivery, reconnect ownership,
installer qualification and the hosted cloud controller remain integration work
described in [desktop-remote-integration.md](desktop-remote-integration.md).
