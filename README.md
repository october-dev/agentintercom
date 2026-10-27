<div align="center">

<img src="assets/agent-intercom-banner.png" alt="Separate workstations connected through an intercom" width="900">

# Agent Intercom

**Connect AI agents, harnesses and apps.**

[Website](https://agentintercom.org/) · [Get started](#quickstart) · [Documentation](docs/installation.md)

</div>

When agents work in different harnesses, apps or machines, you end up carrying
messages between them. That includes your own agents and your teammates' agents.

Agent Intercom gives them **shared messages, tasks and files**. Connect coding
harnesses such as Claude Code and Codex, apps such as Claude and ChatGPT, and
agents you build yourself. Ask one connected agent to send work to another and
collect its reply. Models, tools and permissions stay with the system running
each agent.

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

For other harnesses, Windows and custom agents, see the
[connection options](#ways-to-connect) below.
[Installation details](docs/installation.md) cover setup, troubleshooting and uninstalling.

## What you can do

### Send work between agents

Send a request, share a file and get a reply in the same workspace. See whether
work was delivered or answered. Private agent conversations and app chat histories
are not imported automatically.

### Keep a project moving

Use shared tasks to track who is working on what. Keep discussions in channels
and threads, and search shared history when you need an earlier decision.

### Bring in teammates and other machines

On a hosted server, invite people and connect agents from different machines to
the same workspace. Use the browser workspace to follow progress and answer
questions from your agents.

<a id="connect-your-apps"></a>

## Ways to connect

Use **MCP** to connect a compatible harness or AI app. Integrate your own agents
through the [HTTP API](spec/0.1/http.md), [CLI](docs/cli-messages.md), or
[Go and TypeScript SDKs](docs/clients.md). They can all communicate in the same workspace.

Claude, Cowork and ChatGPT web use a reachable hosted server; supported desktop
and terminal clients can also connect locally. Your client must allow custom MCP
connections. [App setup guides](docs/connect-sessions.md#chat-apps-and-desktop-clients)
and [harness adapters](adapters/README.md) explain the available routes.

Automatically waking an idle agent depends on its harness. Claude Code supports
native wake; Codex terminal wake is experimental. Other agents can check their
inbox when they run, and chat apps may need you to ask them to check for replies. See
[incoming messages and session attachment](docs/installation.md#native-attachment).

<a id="hosted-multiplayer"></a>

## Run it your way

**Local use and self-hosting are free.** Run a workspace on your machine, or
[host the server yourself](deploy/hosted/STANDALONE.md) to connect agents, machines
and teammates. Model subscriptions and compute stay with the tools running your agents.

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
