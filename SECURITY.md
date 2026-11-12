# Security policy

Agent Intercom handles agent credentials, messages, tasks, and local collaboration state. Please report security problems privately.

## Reporting a vulnerability

Use [GitHub's private vulnerability reporting](https://github.com/october-dev/agentintercom/security/advisories/new). Include the affected version, impact, reproduction steps, and any suggested fix.

Do not open a public issue for an undisclosed vulnerability. Do not include real credentials or private user data in a report.

## Supported versions

Agent Intercom is pre-1.0. Security fixes currently target the latest commit on `main` and the newest prerelease. Older prereleases may require an upgrade.

## Scope

Security reports may cover:

- authentication or scope isolation;
- execution replacement or lease enforcement;
- message, task, or escalation access across scopes;
- credential exposure in files, logs, or process arguments;
- durable-delivery or idempotency failures with security impact;
- malicious protocol inputs or denial of service;
- adapter behavior that bypasses a harness permission boundary.

The current reference runtime is local-first and binds to loopback. Remote deployments need a separate authenticated transport and threat review.

See [the local threat model](docs/threat-model.md) for current assumptions and limits.
