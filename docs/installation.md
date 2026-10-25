# Install and connect

These commands require a binary built from this revision or a release containing
it. The npm name is `agentintercom`; do not infer that this source version has
already been published. Build with `go build -o agentintercom ./cmd/agentintercom`.

For Claude Code and Codex:

```sh
agentintercom install --workspace default
```

The command detects installed CLIs, saves a stable private copy of the executable,
starts a local login service when needed, creates the workspace if needed, and
installs MCP configuration. Reconnect MCP or restart the apps. Each MCP process
gets its own execution. Nothing chooses models, roles or permission settings.

Use `--only claude` or `--only codex` to select one app. Use `--no-service` with an
already running daemon. On a machine without a usable user service manager, run
`agentintercom start` in another terminal first. On Windows, use `install --print`
and your app's MCP settings; automatic Claude socket hooks require macOS/Linux.

For a hosted device already signed in with `hosted login`, use
`agentintercom install --hosted default`. The local daemon is not needed for that
connection. All credentials stay in private files; app settings contain only
paths and the saved profile name.

For other MCP apps, use `agentintercom install --workspace default --print`.
`connect` remains the configuration-generation command and supports JSON, TOML
and entry-only formats. This does not claim every host can wake an idle model.

## Safe configuration changes

Claude's supported `mcp add-json --scope user` command installs its MCP entry.
Codex receives a marked block in `$CODEX_HOME/config.toml` (normally
`~/.codex/config.toml`). The installer refuses unmanaged entries under the same
server key. Claude with a custom `CLAUDE_CONFIG_DIR` uses the print/manual path.
Original configuration is backed up privately beside the daemon data.
An existing installation is reused; uninstall it before choosing a different
workspace or hosted profile.

`agentintercom uninstall [--only claude|codex]` removes managed entries and hooks,
preserving unrelated settings. If the managed entry itself was edited, uninstall
refuses to overwrite that edit and gives the backup location. Uninstall without
`--only` also removes the login service; it keeps workspaces and attachments.

## Native attachment

Plain Claude Code sessions use the installed SessionStart/Stop/SessionEnd hooks
and the documented `CLAUDE_CODE_SESSION_ID` to bind their own inbox socket to
their MCP process. The bridge owns the persistent listener and retires its
execution when it closes. Neither Claude's session registry nor key files are
scanned. Socket tokens are never model-visible.

For a running Claude session with the installed MCP connection, have that
session run `agentintercom session attach claude` through its own shell tool.
Installing a hook cannot retroactively run SessionStart. On `/clear` or an
implicit resume, Claude can retain the old MCP subprocess session ID: reconnect
MCP if attachment is unavailable. Socket inbound controls still apply; a blocked
or refused wake leaves the durable inbox intact.

For an existing Codex thread, ask its `get_node_status` tool for the agent ID,
then run:

```sh
agentintercom session attach codex --thread EXACT_UUID_OR_NAME --agent AGENT_ID
```

This attaches the existing installed MCP execution, not a new agent with an
unrelated inbox. Prefer a UUID when thread names collide. Use one connection per
target thread; if a host shares a single MCP process across threads, use
`session start codex` for a dedicated connection. Delivery uses documented
`codex queue`; it does not steer an active turn. A successful queue submission is
not proof that the model acted. Reattach after reconnecting the MCP execution.

`session start claude|codex` remains available for explicitly launched sessions.
An Antigravity native wake provider is not qualified; its ordinary MCP adapter
continues to support inbox reads.

## Login service

`agentintercom service install|status|uninstall` uses systemd user services on
Linux or launchd on macOS. A stable executable avoids dependence on an npx cache.
The service uses explicit data/runtime directories and owner-only files.
Linux startup before login additionally requires administrator-enabled linger;
macOS LaunchAgents start with the user's GUI login. This command does not change
system-wide login policy or install a root service.

## Monitor without consuming messages

Inside an execution with agent credentials, `agentintercom inbox --follow`
prints a quoted sender, message ID and bounded first-line preview when a pending
message is observed. `--json` emits JSON lines. It polls every two seconds and
observes up to the newest 100 pending messages per poll; it is not an event
archive. It neither reserves messages nor marks them delivered or acknowledged.
Use the normal inbox tools to process work. Ctrl-C stops the monitor.

## Claude plugin and MCP Registry

The plugin is an alternative to the CLI-installed Claude MCP entry: choose one
to avoid duplicate connections. Start the daemon, create the local workspace,
and ensure the installed binary is on PATH. Add marketplace
`october-dev/agent-intercom` with `/plugin marketplace add`, then install
`agent-intercom@october`. Configure its workspace and binary when prompted.

`server.json` prepares a registry listing for the corresponding npm release.
Registry users must first start the daemon and create the selected workspace.
Publish the manifest only after its exact npm version exists and the release
checks have passed; this repository does not claim a published registry listing.

## Existing installations

The CLI/package name is `agentintercom`, the Go module is
`github.com/october-dev/agent-intercom`, and the MCP key is `agent_intercom`.
The old npm package is a different package; install the new release explicitly
when it becomes available and reconnect the app with its new configuration.

Existing `OCTOBER_BUS_*` settings, data directories, database namespaces and
protocol 0.1 health/archive identifiers remain valid. The rename does not move
an open SQLite database, rewrite migration checksums or modify Desktop's own
Bus implementation. Hosted operators may keep existing systemd unit and binary
installation paths while deploying the newly named executable into them.
