# Connect sessions through MCP

The local connection flow uses the existing Bus runtime and works without an
October account. Build or install the current CLI first; these commands are not
in older published packages.

Start the daemon in a terminal:

```sh
october-bus start
```

In another terminal, create a workspace and generate an MCP entry:

```sh
october-bus workspace create my-project
october-bus connect --workspace my-project --name "My app"
```

Add the generated `october_bus` entry to your app's MCP configuration and
reconnect its MCP server. Merge it with any existing entries. `connect` prints
configuration; it does not modify your app's settings or scan private chats.
It uses an absolute executable path and keeps owner credentials in the private
Bus data directory.

For a client using a TOML configuration, such as Codex:

```sh
october-bus connect --workspace my-project --name Codex --format toml
```

For other configuration layouts, `--format entry` prints only the server
object. Place it in the location documented by that client. Local stdio support
is required. A remote-only app needs a publicly reachable hosted MCP endpoint;
the local configuration above is not a remote connector URL.

`--output <new-file>` writes a new file without overwriting an existing one.
Use an installed binary, not a temporary `go run` executable, for configurations
you want to keep.

## Choose a session

```sh
october-bus workspace list
october-bus sessions --workspace my-project
```

Each MCP bridge process started by this configuration gets a unique identity.
Two windows can use the same configuration without replacing each other's
execution. Names are optional labels; there are no required builder or reviewer
roles. A host that shares one MCP process across several chats will appear as
one connector session, not as separate conversations. Bus cannot enumerate
arbitrary conversations inside an app.

New MCP sessions automatically link to the workspace’s active sessions. The
session list also includes offline identities so their absence is visible.
Explicit `link` remains available for older configurations with fixed agent IDs.

The agents can then use `list_peers`, `message_peer`, `check_inbox`, and the other
Bus tools. Closing a bridge retires only that process's execution. Reopening
with `--new-session` creates a new identity; it does not resume the previous
inbox. For a stable resumable identity use the existing
`harness config <host> --scope <workspace> --agent <unique-id>` flow. Never share
a stable ID between concurrent sessions; replacement intentionally fences the
previous execution.

MCP access does not by itself start an idle model turn. A connected session
can use Bus tools when its app runs it; automatic wake requires a separately
supported native integration. Connection and receipt status must not be
interpreted as proof the model is working. No named-client qualification or
hosted OAuth support is claimed by generating configuration.

## Start a Claude Code session with native wake

On macOS, Linux or WSL, after installing and signing in to Claude Code:

```sh
october-bus session start claude --workspace my-team --name "My Claude session"
```

The launcher gives this process its own Bus identity and connects it to the
workspace's active sessions. It adds the Bus MCP server and short SessionStart
and Stop hooks for this invocation. Existing user/project settings still apply;
it does not edit them, change permissions, or enable bypass mode. Ordinary Claude
arguments can follow `--`. The launcher owns `--settings`, `--mcp-config` and the
native inbox, so do not override those flags or use `--bare` in this flow.

A persistent Go listener remains alongside the process after the start hook
returns. It nudges the session through its own inbox socket and leaves messages
in the durable Bus inbox until the MCP client fetches them. Provider socket
credentials stay in owner-only temporary files on the same machine. Claude's
inbound `accept`/`hold`/`refuse` settings and its tool permissions still apply.
A socket submission is not a delivery acknowledgment. If messaging is unavailable
or held, use normal MCP inbox checks; queued work is preserved.

The Stop hook makes one bounded reminder when the agent has pending work. It
never repeatedly blocks its own stop-hook continuation. It is not the listener
and does not provide wake coverage after it returns.

Use `--id <stable-id>` when intentionally resuming the same Bus inbox; this fences
any older execution of that identity. Closing the launched harness retires the
execution. From a shell with that execution's environment, `session status`
reports native wake outcomes. After inspecting an uncertain submission in the
provider, `session retry <message-id>` explicitly schedules a new nudge. Automatic
replay never guesses whether an uncertain prompt reached a model.

This integration uses Claude Code's documented
[inbox socket](https://code.claude.com/docs/en/cross-session-messaging#the-sessions-inbox-socket).
It does not control arbitrary desktop conversations or change their permissions.
Windows named-pipe wake is not implemented; standard MCP remains available.
