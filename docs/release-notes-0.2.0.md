# Agent Intercom 0.2.0 — release candidate

The full CLI and TypeScript SDK are published as `agentintercom-cli@0.2.0` under
the `candidate` tag. Install with `npm install -g agentintercom-cli@0.2.0`, or try
`npx agentintercom-cli@0.2.0 demo`. Stable `latest` promotion, broader harness
qualification and public hosted signup remain separate steps.

## Connect the sessions you already use

Create a local workspace, generate an MCP connection, and select actual connected
sessions by name. Use the CLI to send, receive and acknowledge messages, inspect
receipts, work in shared channels and transfer private files. Channels support
subscriptions, threaded replies and search. The MCP surface has 23 tools, including
the existing message/task/question contract.

For hosts that can wake an idle conversation, native Claude Code and experimental
Codex launchers bind incoming messages to that session. They submit a notification
and retain durable delivery state; acknowledgement still means an agent explicitly
acknowledged the message. The launcher preserves normal host permissions. Other
MCP clients can read and send work through their supported tool lifecycle.

## Hosted workspaces

The same Go daemon supports PostgreSQL and an embedded browser workspace. Members
can connect machines, invite teammates, exchange messages, manage tasks, answer
agent questions, use channels and share files. Remote Streamable HTTP MCP supports
OAuth consent and per-app keys. Connection replacement preserves session inboxes
while revoking the old credential.

The public hosted product is Agent Intercom; technical command, module and package
names remain unchanged. Workspace hosting is planned at $19/month or $190/year
for five people, 50 concurrent sessions, 100,000 new deliveries per calendar month,
5 GiB of files and 25 GiB in each file-transfer direction. Models and agent compute
are supplied separately by the user. No automatic usage overage charges apply.
See [billing](hosted-billing.md) for trial allowances and entitlement behavior.

## Data and operations

Owners can pause sessions, export a portable archive, prune eligible completed
history and permanently delete a workspace. Deletion records its intent, revokes
connections and confirms billing cancellation before removing content. Shared
October account deletion is guarded until workspace membership is resolved.

Deployment source includes isolated database roles, migrations, systemd services,
encrypted database/file backups, recovery fencing and coexistence with the older
Bus gateway. Private file storage has disk reserves and bounded transfer work.
Archives over 64 MiB need an operator-assisted export. Existing Desktop remote
bridges remain supported; no Desktop store migration is required for this release.

## Installation and upgrade

The npm distribution carries the native CLI and TypeScript client, with
matching macOS/Linux/Windows packages for x64 and arm64. Keep optional dependencies
enabled; installation works with scripts disabled. The release workflow tests the
actual tarballs before candidate publication. Stable `latest` promotion uses those
same artifacts after qualification.

Back up local state before upgrading. Hosted operators run all checksum-pinned
migrations through 014 with the migration role, then apply runtime grants before
starting the matching daemon. October's shared Supabase identity requires the
separate [lifecycle integration](../deploy/hosted/supabase/README.md). Test a
quarantined restore before production use. Do not downgrade a migrated database
by starting an older executable against it.
