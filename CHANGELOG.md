# Changelog

## Unreleased

Changes on `main` after the `0.2.0` release candidate. They are not in the
published `agentintercom-cli@0.2.0` package; build from source to use them
before the next release.

- Add `task claim`, `task release`, `task complete` and `task progress` CLI
  commands for shared tasks (#196).
- Add `question list` and `question answer` to answer pending agent questions
  from a terminal (#195).
- Let `agent run` read the scope token from `--scope-token-file` or
  `--scope-token-fd` (#155).
- Add `downloadAttachment` to the TypeScript client, with integrity checks (#197).
- Run the Cursor adapter under `agent run` and let `doctor` find `cursor-agent`
  (#209).
- Exit with status 0 for subcommand `--help` (#191).
- Preserve the HTTP status when a Go client error body is not valid JSON (#190).
- Report failed `attachment upload` requests instead of printing `null`.
- Exit `agent run` and `session start` with the managed command's exit status,
  and with a failure status after an interrupt.
- Return `NOT_FOUND` when completing or releasing an unknown task, accept
  escalation answers when the message backlog is full, and expire messages
  before releasing an expired inbox reservation.
- Refuse a runtime directory or discovery file owned by another local user.
- Hosted: recover OAuth client registration capacity from abandoned clients,
  send the RFC 9207 `iss` authorization response parameter, and limit admins
  to revoking member invitations.

## 0.2.0 (release candidate)

- Use the Agent Intercom product name, `agentintercom` command and
  `agentintercom-cli` npm package.
  Move the repository and Go module to `october-dev/agentintercom`. The
  TypeScript client classes now use the `AgentIntercom` prefix.
- Add local and hosted workspaces, PostgreSQL storage, account membership,
  remote MCP authorization, a browser workspace, channels, files and billing
  integration. Hosted launch and payment qualification are tracked separately.
- Add managed Claude Code/Codex installation, user login services, native session
  attachment, non-consuming inbox monitoring and MCP tool metadata.
- Preserve existing protocol 0.1 health/archive identifiers, local data paths,
  `OCTOBER_BUS_*` configuration, applied migrations and hosted deployment paths.

`0.2.0` is published to npm under the `candidate` tag; it is not a hosted launch.
See [releases](https://github.com/october-dev/agentintercom/releases) for published
versions and [compatibility](compatibility/registry.json) for qualified adapters.
