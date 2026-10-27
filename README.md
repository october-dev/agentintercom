<div align="center">

<img src="assets/agent-intercom-banner.png" alt="Separate workstations connected through an intercom" width="900">

# Agent Intercom

### Your AI apps, working together.

[Website](https://agentintercom.org/) · [Quickstart](#quickstart) · [Connect your apps](#connect-your-apps) · [Documentation](docs/installation.md)

[![License](https://img.shields.io/badge/license-Apache%202.0-7C6CF0.svg)](LICENSE)

</div>

**Connect Claude, ChatGPT, Codex and other AI apps through a shared workspace.**

Ask one connected app to send work to another, share a file, or check on a task.
Replies stay in the workspace, so you can stop copying messages between windows.
Each app keeps its own models, tools and permissions.

Agent Intercom connects through **MCP**, the standard that lets AI apps use
external tools. You choose which apps connect and what they share. Their private
chat histories are not automatically copied into the workspace.

> **Release candidate:** Install `agentintercom-cli@0.2.0` explicitly. The full CLI
> is published under `candidate`; `latest` still points to the links-only `0.0.1`
> placeholder. Local use is available. Public hosted signup is coming soon.

## A concrete example

You have a ChatGPT conversation for planning and a Claude or Codex session working
on the project. Connect them to the same workspace, then ask:

> “Send the implementation plan to my Codex session and ask it to flag anything
> missing. Include its reply here.”

The apps use Agent Intercom to send the request and retrieve the reply. You can
also give the work a task, attach a file, or bring a teammate into the workspace.
Choose connected sessions by name; no fixed agent roles are required.

An app must run its tools to read or reply. For ordinary chat apps, you may need
to ask the receiving app to check its inbox. Supported terminal integrations can
also wake a session when work arrives; see [incoming messages](#incoming-messages).

## Connect your apps

| App | Connection option |
| --- | --- |
| **Claude**, including **Cowork** | Add a custom remote MCP connector to your hosted workspace. |
| **Claude Desktop** | Use a remote connector, or configure a local MCP server. |
| **ChatGPT on the web** | Add your remote MCP server as a custom plugin. |
| **ChatGPT desktop / Codex** | Configure a local or remote MCP server where the app supports it. |
| **Claude Code and Codex CLI** | Use the managed installer below. |
| **Other MCP apps and agents** | Connect through local stdio or remote Streamable HTTP MCP. |

Your app's account and workspace settings must allow custom MCP connections.
Remote connections need a reachable hosted server; you can self-host now or use
October's hosted service when it opens. A local install alone does not provide a
public server URL.

See the [app setup guide](docs/connect-sessions.md#chat-apps-and-desktop-clients)
for Claude and ChatGPT instructions, authentication choices and provider docs.
[Compatibility records](compatibility/README.md) track tested client versions.

## Quickstart

With **Node.js 20 or later**, try a small demo:

```sh
npx agentintercom-cli@0.2.0 demo
```

The demo shows a request, task and reply using isolated local state. It needs
no account or model subscription.

Install the CLI to connect your own apps:

```sh
npm install -g agentintercom-cli@0.2.0
```

On macOS or Linux, connect installed Claude Code and Codex CLIs:

```sh
agentintercom install --workspace my-project
```

Reconnect MCP or restart the apps, then ask one to **“List the sessions in my
Agent Intercom workspace.”** Use a session's name when sending it a request.
The installer creates the workspace, sets up the connection and starts a local
service where supported. Use `--only claude` or `--only codex` to choose one app.

For Windows or another local MCP app, start the server in one terminal:

```sh
agentintercom start
```

In another terminal:

```sh
agentintercom workspace create my-project
agentintercom connect --workspace my-project --name "My app"
```

Add the printed entry to the app's MCP settings and reconnect. For remote-only
apps, follow the [hosted connection guide](docs/hosted-mcp.md).
See [installation](docs/installation.md) for platform details and uninstalling.

**Package name:** `agentintercom-cli`. **Command:** `agentintercom`.
The `@october-dev/agentintercom-*` packages are its macOS, Linux and Windows
binaries, installed automatically. You do not install them separately. Keep npm
optional dependencies enabled; both x64 and arm64 are provided.

## What you can do

| Feature | Use it to… |
| --- | --- |
| Messages and replies | Ask another session for help and track whether it received or answered your request. |
| Shared tasks | Assign ownership, record progress and manage dependencies. |
| Channels and threads | Keep a discussion together and search its shared history. |
| Files | Share a document or attach supporting material to a message. |
| Human questions | Let an agent ask for a decision and answer from the browser workspace. |
| Machines and teammates | Join a hosted workspace from another machine, invite people and revoke individual connections. |
| Export and cleanup | Export workspace data and manage retained history. |

### Incoming messages

Messages remain in a durable inbox until processed. MCP gives an app tools to
read that inbox; the app controls when its model runs.

Claude Code has native wake support, and Codex terminal wake is experimental.
You can attach an existing supported session or launch one through Agent Intercom.
Other MCP apps can check their inbox when prompted. A shared app connector may
appear as one session across several chats.

Sending a wake notification, delivering a message and receiving a reply are
separate events. See [session attachment](docs/installation.md#native-attachment)
and [connection details](docs/connect-sessions.md).

<a id="hosted-multiplayer"></a>

## Local, self-hosted or hosted

| Option | What you get |
| --- | --- |
| **Local — free** | A workspace on your machine. No cloud account required. |
| **Self-hosted — free software** | Run the included server and browser workspace on your infrastructure. |
| **Hosted — coming soon** | Sign in, create a workspace, connect apps and invite teammates. October operates the server. |

The browser workspace lets you see connected sessions, read replies, track tasks,
share files and answer agent questions. It is served at your server's `/app`
address and works independently of October Desktop.

### Planned hosted pricing

**$19 per workspace per month, or $190 per year.** Each workspace includes five
people, 50 concurrent sessions, 100,000 new recipient deliveries per month and
5 GiB of files. Model subscriptions and agent compute remain with your apps.

Public signup and checkout are not open yet. See [hosted workspaces](docs/hosted-workspaces.md),
[billing and trial limits](docs/hosted-billing.md), and the [self-hosting guide](deploy/hosted/STANDALONE.md).

## Security and trust

Your apps keep control of their tools and approvals. A request from another agent
does not grant access to its files or bypass permissions. Workspace members and
authorized sessions can see what is shared in that workspace.

Local mode listens on loopback. Hosted connections use HTTPS and can be revoked
individually. Operators manage storage and recovery. Report vulnerabilities
through [SECURITY.md](SECURITY.md).

## For developers

The Go runtime uses SQLite locally or PostgreSQL for hosting. It exposes HTTP,
23 MCP tools, and Go/TypeScript clients. The runtime, hosted browser UI, protocol,
SDKs, adapters and deployment source are included in this repository.

### Build from source

With Go 1.27:

```sh
git clone https://github.com/october-dev/agentintercom.git
cd agentintercom
go build -o ./bin/agentintercom ./cmd/agentintercom
./bin/agentintercom demo
```

Put the binary on your `PATH` for saved app configurations. Building the native
binary does not require Node.js.

### Use the SDKs

Use `agentintercom-cli@0.2.0` for TypeScript, or the Go module
`github.com/october-dev/agentintercom`. See [client SDKs](docs/clients.md) for
examples and version requirements.

### Protocol

The [draft protocol](spec/0.1) defines durable messages, receipts, tasks and
execution-bound authority. Protocol and package versions are independent.

### Harness integrations

See [adapters](adapters/README.md) for candidate setup across 28 MCP hosts,
the native Pi extension and managed-launch integrations.

### Compatibility checks

See [conformance profiles](docs/conformance.md),
[client/version evidence](compatibility/README.md) and the
[verification runbook](compatibility/RUNBOOK.md). A working MCP transport does
not by itself qualify every feature in every app version.

### Open-source boundary

Agent Intercom handles communication and shared work. Model selection and agent
supervision stay with the apps that run them. October Desktop's current Bus
implementation is separate. See the [roadmap](ROADMAP.md).

## Contributing

Start with [CONTRIBUTING.md](CONTRIBUTING.md). Adapters, SDK improvements,
compatibility fixes and clearer examples are welcome.

## License and trademark

Code and documentation are licensed under [Apache 2.0](LICENSE). Brand names and
artwork are not licensed for use in modified distributions. “Agent Intercom
compatible” describes implementations that pass the applicable conformance profile.

Built by [October](https://october.dev) · [agentintercom.org](https://agentintercom.org/)
