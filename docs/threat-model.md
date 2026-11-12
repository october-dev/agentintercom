# Local threat model

This document covers the draft 0.1 local runtime profile.

## Protected assets

- scope and agent credentials;
- private message and context content;
- task and escalation state;
- correct peer identity and delivery state;
- execution ownership and lease state;
- isolation between collaboration scopes.

## Trust boundaries

The admin token creates scopes and stops the daemon. A scope token registers agents, creates peer links, and resolves human escalations. An agent token can act only for one current execution inside one scope.

Adapters and harnesses remain responsible for their own model access, files, tools, and user approvals. A Bus message never grants permission to use them.

## Threats and controls

| Threat | Current control |
| --- | --- |
| Cross-scope access | Every persisted object and authenticated operation is scope-bound |
| Replaced or stale process | Agent authority is bound to the current execution and renewable lease |
| Peer-name spoofing | Programmatic routing uses byte-exact agent IDs before unique exact display names |
| Duplicate work from retries | Idempotency keys are permanent per sender and bound to the original content |
| External output reader gains Bus authority | Output credentials are bound to one stream and explicit read or publish permissions |
| Browser reads output from an untrusted site | Output CORS headers require an exact configured origin |
| Output publisher exhausts local storage | Streams enforce bounded retention, payload limits, and per-principal rate limits |
| Concurrent inbox consumers | Short reservations serialize delivery attempts and support redelivery |
| Message or task flood | Per-scope and per-agent limits return explicit backpressure |
| Agent resolves its own escalation | Only scope authority can resolve human escalation |
| Context expands permission | Context is bounded metadata or content and grants no file, URL, tool, or account access |
| Credential disclosure | Tokens stay out of process arguments, private paths reject symlinks, and responses use `Cache-Control: no-store` |
| Network exposure | The local profile binds to loopback by default |

## Local process risk

The runtime and data directories request owner-only permissions where the operating system supports them. A malicious process already running as the same operating-system user may still be able to access local files, inspect memory, or control sibling processes. Agent Intercom is not a sandbox against the local user account.

Do not place scope or admin credentials in model context. Give a harness only its execution-bound agent token when possible.

Configuration-only adapters keep both scope and execution tokens inside the Go bridge. Their protected local scope-token files are sensitive credentials, including after daemon shutdown. Creation/import/rotation/deletion through the local CLI maintain that cache; remote API operations do not. Hashing scope IDs makes portable filenames, not encryption. Unix modes are enforced on reads, but Windows deployments must check owner-only ACLs separately. Neither mode protects against a malicious process running as the same OS user.

Portable archives exclude reusable credentials and execution authority. They still contain collaboration content such as messages, context, tasks, escalation answers, and output values. Store and transfer them as sensitive project data.

## Untrusted content

Messages, task text, display names, file paths, and URLs are untrusted input. Adapters must treat them as data, not instructions that bypass the harness permission system. A file or URL reference does not prove that the resource is safe or accessible.

## Remote operation

Loopback authentication is not a remote security design. Cross-machine and hosted operation require TLS, server authentication, tenant isolation, revocation, replay controls, and network interruption tests. Those guarantees are not part of the current local profile.

## Logging and retention

The daemon logs startup information, not credentials or message content. Durable state remains in the local database after acknowledgement. Retention, portable export/import, backup and explicit deletion are available; operators still need an approved retention and recovery policy. See [operations](operations.md).
