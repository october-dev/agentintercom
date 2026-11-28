# Changelog

## Unreleased

- Use the Agent Intercom product name, `agentintercom` command and
  `agentintercom-cli` npm package.
  Move the repository and Go module to `october-dev/agentintercom`. The
  TypeScript client classes now use the `AgentIntercom` prefix.
- Add local and hosted workspaces, PostgreSQL storage, account membership,
  remote MCP authorization, a browser workspace, channels, files and billing
  integration. Hosted launch and payment qualification are tracked separately.
- Add managed Claude Code/Codex installation, user login services, native session
  attachment, non-consuming inbox monitoring and MCP tool metadata.
- Re-running install after an upgrade now restarts an out-of-date managed
  daemon and verifies the replacement process before reporting success.
  A pending-activation record makes retries reactivate after a failed or
  interrupted attempt (including same-version rebuilds) instead of falsely
  reporting "already current", and an unhealthy managed daemon is restarted
  and verified rather than silently reused. Unchanged reinstalls still skip
  the restart, and manually started daemons keep the existing reuse behavior.
- Preserve existing protocol 0.1 health/archive identifiers, local data paths,
  `OCTOBER_BUS_*` configuration, applied migrations and hosted deployment paths.

These changes are not a claim that an npm release or hosted launch has occurred.
See [releases](https://github.com/october-dev/agentintercom/releases) for published
versions and [compatibility](compatibility/registry.json) for qualified adapters.
