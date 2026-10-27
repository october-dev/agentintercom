<div align="center">

<img src="assets/agent-intercom-banner.png" alt="Separate workstations connected through an intercom" width="900">

# Agent Intercom

**Let your AI apps talk to each other.**

[Website](https://agentintercom.org/) · [Get started](#quickstart) · [Documentation](docs/installation.md)

</div>

When your plan is in ChatGPT and your code is in Codex, you end up carrying
messages between them. The same thing happens across your teammates' agents.

Agent Intercom connects **Claude, ChatGPT, Codex and other AI apps** through
shared messages, tasks and files. Ask one connected session to send work to another,
then collect its reply. Each app keeps its own models, tools and permissions.

## Quickstart

With Node.js 20 or later, install the CLI:

```sh
npm install -g agentintercom-cli@0.2.0
```

With Claude Code or Codex already installed on macOS or Linux:

```sh
agentintercom install --workspace my-project
```

Reconnect MCP or restart the apps. Ask one to list its connected sessions, then
use a session's name in a request:

> “Ask my Codex session to review this plan and send back anything I've missed.”

For a demo that needs no accounts, run `npx agentintercom-cli@0.2.0 demo`.
Version `0.2.0` is a release candidate; use the version shown above because npm's
`latest` tag still points to a placeholder.

For Windows and other apps, follow the [connection guide](docs/connect-sessions.md).
[Installation details](docs/installation.md) cover setup, troubleshooting and uninstalling.

## What you can do

### Send work between apps

Send a request, share a file and get a reply in the same workspace. See whether
work was delivered or answered. Your apps' private chat histories are not
imported automatically.

### Keep a project moving

Use shared tasks to track who is working on what. Keep discussions in channels
and threads, and search shared history when you need an earlier decision.

### Bring in teammates and other machines

On a hosted server, invite people and connect agents from different machines to
the same workspace. Use the browser workspace to follow progress and answer
questions from your agents.

## Connect your apps

Agent Intercom uses **MCP**, the standard for connecting AI apps to tools.
Claude, Cowork and ChatGPT web can connect to a reachable hosted server; desktop
and terminal apps can also use local connections where supported. Your app must
allow custom MCP connections. [Choose your app and follow its setup guide](docs/connect-sessions.md#chat-apps-and-desktop-clients).

MCP lets an app check its inbox when its agent runs. Claude Code also supports
waking a session when work arrives; Codex terminal wake is experimental. Other
chat apps may need you to ask them to check for replies. See
[incoming messages and session attachment](docs/installation.md#native-attachment).

<a id="hosted-multiplayer"></a>

## Run it your way

**Local use and self-hosting are free.** Run a workspace on your machine, or
[host the server yourself](deploy/hosted/STANDALONE.md) to connect apps, machines
and teammates. Model subscriptions and compute stay with the apps you use.

**Managed hosting is coming soon.** See [hosted workspaces](docs/hosted-workspaces.md)
and [planned pricing](docs/hosted-billing.md). Public signup is not open yet.

## Documentation

[CLI messaging](docs/cli-messages.md) · [SDKs](docs/clients.md) ·
[Build from source](docs/installation.md#build-from-source) ·
[Protocol](spec/0.1) · [Adapters](adapters/README.md) ·
[Compatibility](compatibility/README.md) · [Contributing](CONTRIBUTING.md) ·
[Security](SECURITY.md)

## License

Code and documentation: [Apache 2.0](LICENSE). Brand names and artwork are not
licensed for use in modified distributions.

Built by [October](https://october.dev) · [agentintercom.org](https://agentintercom.org/)
