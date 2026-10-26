<div align="center">

<img src="assets/agent-intercom-banner.png" alt="Separate workstations connected through an intercom" width="900">

# Agent Intercom

### Independent agents. Shared work.

[![License](https://img.shields.io/badge/license-Apache%202.0-7C6CF0.svg)](LICENSE)
[![Protocol](https://img.shields.io/badge/protocol-agent%20interoperability-28B8D8.svg)](#protocol)
[![Runtime](https://img.shields.io/badge/runtime-local--first-3BAA6E.svg)](#security-and-trust)

</div>

**Agent Intercom connects your AI apps, agents, machines and teammates through shared messages and work.**

Connect the apps you already use, choose a session by name, and send a request.
Keep its reply, shared tasks, channels and files in one workspace. Each app keeps
its own models, tools and permissions. You do not need to assign roles or design
a workflow before connecting.

The npm package is **`agentintercom-cli`**; the terminal command is **`agentintercom`**.
[Quickstart](#quickstart) · [Connect your apps](docs/connect-sessions.md) ·
[Hosted workspaces](#hosted-multiplayer) · [Website](https://agentintercom.org)

> [!NOTE]
> **Prerelease:** Build from source to try the current implementation. The full
> CLI package and public hosted signup are not released yet. The existing
> `agentintercom-cli@0.0.1` npm package only prints project links.

## What you can do

Stop carrying messages between agent windows. Connect your apps to a workspace,
find a session by name, and ask it for help. Each app keeps its own models, tools,
private conversation and permissions.

| Capability | What it gives you |
| --- | --- |
| Find and connect sessions | See connected peers, names, capabilities and availability. Use the apps you already have open. |
| Send work and collect replies | Durable messages, requests and responses, with separate accepted, delivered, acknowledged and replied receipts. |
| Share tasks | Claims, dependencies, progress and completion make ownership visible. |
| Work in channels and threads | Keep discussions together and search shared channel history. |
| Share files | Upload workspace files and include them in a message or conversation. |
| Bring in a person | Agents ask questions; people answer from the browser workspace. Tool permissions still belong to the agent's app. |
| Add machines and teammates | Authorize another machine, invite people to a hosted workspace, and revoke individual connections. |
| Keep control of your data | Run locally, self-host, or use the forthcoming hosted service; export workspace data and manage retention. |

Connect through MCP, HTTP, the CLI, or the Go and TypeScript SDKs. The MCP server
exposes 23 tools for peers, messaging, tasks, human input, channels and files.
App transcripts are not automatically shared: the workspace contains the work
you and your agents choose to send.

### Existing sessions and incoming messages

The managed installer configures Claude Code or Codex and keeps a private backup
of the settings it changes. Reconnect MCP in the app, then attach an existing
supported session or start one through the CLI. You choose a session; there are
no required builder or reviewer roles.

Supported Claude Code and Codex terminal integrations can wake a session when
work arrives; Codex wake is experimental. Other MCP apps can read their inbox
when the app runs the agent. MCP alone does not guarantee that an idle chat will
resume. Wake submission, message delivery, acknowledgment and reply are
separate states.

The CLI also provides `inbox --follow` to monitor pending messages without
consuming them. See [installation and attachment](docs/installation.md),
[connection options](docs/connect-sessions.md), and [CLI messaging](docs/cli-messages.md).

## A concrete example

```text
Claude Code  -> list connected sessions
Codex        -> appears under its session name
Claude Code  -> request: Review the retry path in checkout.ts
Codex        -> reads the request and claims the review task
Codex        -> response: The retry drops the idempotency key
Codex        -> completes the review task
Claude Code  -> reads the reply and continues
```

Those app names are examples. A task records ownership; a response answers a
request. Neither operation grants access to another app's tools or credentials.
An agent in one repository can ask an agent in another for a focused change or
finding, while each keeps its own files and reasoning loop.

## How it fits together

```text
Your app or agent
       |
MCP / HTTP / Go or TypeScript SDK
       |
Agent Intercom runtime
  sessions | messages | channels | files | tasks | human input
       |
Other sessions | teammates | browser workspace
```

The same Go runtime uses SQLite locally or PostgreSQL for hosting. Accepted work
survives daemon restarts. The hosted service adds account membership, independent
app/device connections, OAuth, usage limits and billing around that runtime.
It does not choose models, approve tools or run agents on your behalf.

## Quickstart

The current implementation is available from source. With Go 1.27 installed:

```sh
git clone https://github.com/october-dev/agentintercom.git
cd agentintercom
go build -o ./bin/agentintercom ./cmd/agentintercom
./bin/agentintercom demo
```

The demo uses isolated local state to show a request, task and reply. It needs
no account or model subscription. Put the built binary on your `PATH` to use
`agentintercom` in the following commands. On macOS or Linux, in this terminal:

```sh
export PATH="$PWD/bin:$PATH"
agentintercom install --workspace my-project
```

The installer detects Claude Code and Codex, creates the workspace, starts a
user login service when available, and saves managed MCP configuration.
Reconnect MCP or restart the apps, then ask one to list its peers. Use
`--only claude` or `--only codex` to select an app. A machine without a user
service manager can run `agentintercom start` separately and use `--no-service`.

On Windows, or for another MCP app, use the manual connection flow below.
See [installation](docs/installation.md) for platform details, attaching existing
sessions, hosted profiles, and removing installed configuration.

For another MCP app, start the server in one terminal:

```sh
agentintercom start
```

In another terminal, create a workspace and connect your app:

```sh
agentintercom workspace create my-project
agentintercom connect --workspace my-project --name "My app"
```

Add the printed entry to your app's MCP settings. For Codex-style TOML, add
`--format toml`. Each bridge gets a separate session identity; inspect them with:

```sh
agentintercom sessions --workspace my-project
```

An app that shares a connector across chats appears as one connector, not every
private chat. `connect` prints configuration and does not overwrite app settings.
See the [connection guide](docs/connect-sessions.md) for configuration locations,
resumption, another machine and recovery. These commands are not in older
published packages.

For a supported terminal app with native wake, use either:

```sh
agentintercom session start claude --workspace my-project
agentintercom session start codex --workspace my-project
```

Keep the terminal open. These commands retain your normal app permissions and do
not choose a model or enable automatic tool approval. Native wake currently needs
macOS, Linux or WSL. Generic MCP is available independently of the native launchers.

### Install a published preview

Download a matching archive from [releases](https://github.com/october-dev/agentintercom/releases),
verify it against `checksums.txt`, and put `agentintercom` on your `PATH`. Try:

```sh
agentintercom version
agentintercom demo
```

The `agentintercom` distribution is prepared for macOS, Linux and Windows on x64
and arm64, with Node.js 20+ for npm installation. Follow the exact package version
in its release notes once published. Older releases under the October Bus name
do not contain this setup flow; a source checkout is not an npm publication.

### Build from source

The [quickstart](#quickstart) builds the current CLI. The module language floor
is Go 1.26; this checkout selects Go 1.27 and CI uses Go 1.27.x. Use Go 1.27 for
reproducible builds. No Node.js installation is required for the native Go binary.

Use an installed binary for saved MCP configurations, not a temporary `go run`
executable. Check or stop the local server with `agentintercom status`,
`agentintercom doctor` and `agentintercom stop`.

### Use the SDKs

Import `github.com/october-dev/agentintercom/bus` for Go. The TypeScript SDK supports
Node.js 20+. See [client SDKs](docs/clients.md) for examples and the runtime/SDK
compatibility matrix. Select a published version whose release notes include the
new module/package name; earlier tags still declare the old module path.
A source checkout or passing CI run is not publication.
Older stable-identity harness configurations remain documented in
[adapter setup](adapters/README.md); do not share one identity between concurrent
sessions, because replacement intentionally fences the older execution.

<a id="hosted-multiplayer"></a>

## Local, self-hosted or hosted

Local use and self-hosting are free under Apache 2.0. The same runtime uses
SQLite locally or PostgreSQL for hosting. The optional hosted plan runs your
coordination workspace; your AI apps still run on your machines or compute
providers, with their own model subscriptions.

| Mode | Where it runs | Getting connected |
| --- | --- | --- |
| Local | Your machine, with SQLite | Create a workspace and configure your apps. No cloud account. |
| Self-hosted | Your server, with PostgreSQL and a configured identity provider | Deploy the included service and connect through its HTTPS endpoint. |
| Hosted · coming soon | Operated by October | Sign in, create a workspace, and connect apps, machines and teammates. |

### The browser workspace

The hosted browser UI is implemented in this repository. When public hosting
opens, the flow will be:

1. Sign in and create a workspace.
2. Connect a compatible remote MCP app with OAuth or its own key, or approve a
   terminal device's sign-in code.
3. Choose a connected session by name and send a request.
4. Read replies and receipts, share files, and track tasks or channel threads.
5. Answer agent questions and invite teammates with their own accounts.

The workspace UI is served by the Go service at `/app`. It works independently
of October Desktop. Session connection status and message receipts are shown
separately, so an online connector is not mistaken for completed work.

### Planned hosted pricing

| Plan | Price | Availability |
| --- | ---: | --- |
| Monthly | $19 per workspace per month | Coming soon |
| Annual | $190 per workspace per year | Coming soon |

Each paid workspace includes:

- Five people and 50 concurrent sessions.
- 100,000 new recipient deliveries per UTC calendar month.
- 5 GiB of private files, 16 MiB per file, and 25 GiB each of monthly uploads/downloads.
- Conversations, channels, threads, tasks, questions and a browser workspace.

One message sent to three recipients counts as three deliveries. Retries, polls,
acknowledgments and replies to accepted requests do not count as new deliveries.
The account's first workspace starts a 14-day trial shared by subsequent
workspaces: 2,000 new deliveries per workspace and 256 MiB of files, with no card
required. Completed history is eligible for cleanup after 90 days; new work pauses
at a 5 GiB retained coordination-data estimate. Annual billing saves $38.

The planned hosted workspace address is **`https://bus.october.dev/app`**.
Public signup and checkout are not open yet.

Connect a remote MCP app through OAuth or its own bearer key. On another machine,
run `agentintercom hosted login`, approve its code in the browser, then use
`agentintercom connect --hosted default`. Invite teammates with their own accounts.
Lost credentials can be replaced without discarding the existing session inboxes.
No October Desktop installation is required.

The hosted implementation and deployment source are included here; the paid plan
covers operating the service. See [hosted workspaces](docs/hosted-workspaces.md),
[MCP authorization](docs/hosted-mcp.md), [billing](docs/hosted-billing.md),
[data and recovery](docs/workspace-data.md), and [deployment](deploy/hosted/STANDALONE.md).
Portable self-service exports currently have a 64 MiB limit; larger workspaces
require an operator database/file export.

## Protocol

The draft specification, HTTP contract, MCP mapping, adapter contract and JSON
Schemas live in [`spec/0.1`](spec/0.1). Protocol versions are independent of runtime
and SDK versions.

| Primitive | Meaning |
| --- | --- |
| Identity | A stable agent identity with authority bound to its current execution |
| Discovery | Linked peers and their declared capabilities |
| Presence | Existence, readiness, reachability and lifecycle are separate facts |
| Messaging | Durable notifications, requests, responses, inboxes and receipts |
| Shared tasks | Claims, progress, release, completion and dependencies |
| Context | Explicit bounded sharing, channels and private workspace files |
| Human input | Questions and answers without inventing authority |
| Storage controls | Inspect growth and prune completed history while preserving obligations |

A send is accepted only after the runtime persists it. Retrying the same
idempotency key and content returns the original receipt while retained; changed
content is rejected. Pruning removes the key binding, so retain records longer
than the retry window you support.

A request opens one reply obligation. Its response names the delivered request.
Expiry stops further delivery attempts; a request already delivered may still
receive one reply. Receipts distinguish acceptance, delivery, acknowledgment,
reply and expiry. None of these grant permission to run a tool.

A logical agent ID alone cannot act. Execution tokens and leases fence old
processes. Re-registering replaces execution authority, and task claims belong to
that execution. Managed sessions heartbeat and retire on close. Temporary offline
presence is a separate signal. IDs are exact and case-sensitive; MCP may also
resolve a unique exact display name.

Scope owners can follow ordered events. If retention removed their cursor,
clients rebuild their view from the resource APIs. See the
[event contract](spec/0.1/README.md#scope-events) and [lifecycle documentation](docs/clients.md).

## Harness integrations

| Connection | Suitable for |
| --- | --- |
| Local stdio MCP | Apps that can launch a local MCP process |
| Remote Streamable HTTP MCP | Desktop or cloud apps that support remote MCP and a supported authentication method |
| Managed Claude Code / Codex setup | Terminal sessions with configuration and optional native wake support |
| HTTP, CLI and SDKs | Custom agents, scripts and integrations |

Use local stdio MCP, remote Streamable HTTP MCP, HTTP, or the Go/TypeScript SDKs.
OAuth and manual bearer keys each represent a revocable hosted connector. Your
app controls which chats may use it and when its model runs. Native wake is a
separate capability, not a property of MCP.

The repository includes candidate setup for 28 MCP hosts, a native Pi extension,
and an October Harness managed-launch path. Start with the
[adapter list](adapters/README.md) and [connection guide](docs/connect-sessions.md).
Names and setup examples are not certification of every version. Record actual
client/version results in [compatibility](compatibility/README.md).

For a new integration, register an identity, bind short-lived execution authority,
expose discovery/messages/tasks/human input, report only lifecycle facts the host
can prove, and retire authority on exit. Keep credentials and heartbeat management
outside the model loop where possible. A host without native wake can still offer
inbox checks. See the [verification runbook](compatibility/RUNBOOK.md).

## Compatibility checks

Run the isolated runtime conformance profile:

```sh
agentintercom-conformance --start-runtime --format text
```

Or drive an MCP adapter over stdio:

```sh
agentintercom-conformance \
  --profile mcp-adapter --start-runtime \
  --adapter-command agentintercom \
  --adapter-arg mcp --adapter-arg stdio --format text
```

Failed checks exit nonzero. Passing an adapter profile does not certify a harness;
a released client must also pass its applicable qualification. See
[conformance profiles](docs/conformance.md) and [contributing](CONTRIBUTING.md).

## Security and trust

- Knowing a session exists does not grant access to its process, tools or files.
- Authority belongs to one execution; replacement retires the previous token.
- Peer requests cannot expand scope or bypass the app's permission system.
- Shared workspace content is visible to its members and authorized sessions.
- People approve connections and can revoke them independently.
- Models, compute and permissions remain with the apps that run the agents.

Local mode listens on loopback and needs no cloud account. Hosted mode uses
HTTPS, verified account identity and private PostgreSQL access. Operators manage
storage and recovery; see the [deployment guide](deploy/hosted/STANDALONE.md).
Native wake uses provider mechanisms instead of terminal screen scraping. Report
vulnerabilities through [SECURITY.md](SECURITY.md).

## Open-source boundary

This repository includes the coordination runtime, local and hosted storage,
account and connection APIs, browser workspace, billing integration, deployment
source, protocol, SDKs and adapters. You may run them yourself under Apache 2.0.

October Desktop's canvas and its own current Bus implementation are separate.
Automatic staffing, model selection, routing and supervision are outside this
runtime. Agent Intercom can carry work between agents without deciding how an entire
operation should be run. See [ROADMAP.md](ROADMAP.md) for implementation status.

## Contributing

Adapters, protocol improvements, SDKs, examples and compatibility fixes are
welcome. Keep contributions focused on communication and coordination, preserve
execution authority and harness permissions, and keep private data out of commits.
See [CONTRIBUTING.md](CONTRIBUTING.md) for development and review requirements.

## License and trademark

The code and documentation are licensed under [Apache 2.0](LICENSE). The license
does not grant rights to October's names, logos or other brand assets. Do not
imply endorsement. Use distinct branding for modified distributions unless you
have written permission. “Agent Intercom compatible” describes implementations
that pass the applicable conformance profile.

---

Built in the open by [October](https://october.dev) · [GitHub](https://github.com/october-dev)
