# Native, remote and unresolved harness boundaries

The [adapter directory](../adapters/README.md) contains implemented experimental candidates. [Issue #62](https://github.com/october-dev/october-bus/issues/62) remains the authoritative target tracker; this document describes boundaries, not another compatibility catalog.

## Native integration

Pi's public extension hooks now wrap the same token-free Go bridge. The extension forwards the daemon's actual tool schemas instead of maintaining a second SDK-to-tool mapping. Registration, heartbeat, replacement and retirement remain in the Go session implementation. Tool cancellation is separate from session shutdown. No wake/idle claims are made; PR #90 is not required for these semantics.

Real Pi runs must still establish exact supported extension versions, permission behavior, reload/new/resume/fork cleanup and cancellation. OMP has its own [documented MCP configuration](https://github.com/can1357/oh-my-pi/blob/main/docs/mcp-config.md); its template does not imply Pi certification or an independent lineage count.

[October Harness](../adapters/october-harness) already implements the public native Bus contract upstream. Its candidate uses `agent run --scope`, not another Pi extension. The source includes lifecycle and delivery hooks beyond its older README's pull-only description; exact released behavior and interactions with launcher heartbeats require verification.

## Additional discovered MCP surfaces

The audit's unknown-product list was too broad. [Prime Agent](../adapters/prime-agent) exposes generic MCP through its Python kernel; [Kimchi](../adapters/kimchi) documents its own MCP configuration and planning restrictions; [Freebuff CLI](../adapters/freebuff) loads MCP configuration into base agents through its public SDK. They now have experimental templates, not transferred Pi/Codebuff certification.

Freebuff's inspected loader gives later home entries precedence despite a contradictory comment, and its client drops MCP `isError` while mapping content. These are concrete upstream risks to verify or resolve, not reasons to relax Bus authorization. Prime Agent's lazy kernel connection must start before peer linking. Kimchi's imported configurations must not duplicate an execution ID. See the adapter notes and primary-source links for each.

[Grok](../adapters/grok) (#30) and [DeepSeek](../adapters/deepseek) (#41) now have concrete configuration candidates through the Grok Build CLI's MCP client and the DeepSeek Harness (`dsh`) MCP client plugin, respectively. Both remain experimental and require released-host evidence; DeepSeek Harness is a developer preview with announced compatibility-breaking changes.

## Remote-host boundary

The Devin candidate uses its [documented STDIO surface](https://docs.devin.ai/work-with-devin/mcp), with the daemon and bridge **inside the same managed sandbox**. An operator must install the reviewed Bus binary there, provision private data/runtime directories, start the daemon and create its scope, then generate the configuration in that environment. Do not generate laptop paths and expect the cloud host to reach them. Devin's isolated tool-listing test may not share a running task sandbox; prove both environments explicitly.

Use distinct agent IDs per execution and scope-level admission controlled by the sandbox operator. Restart/recreated sandboxes must not silently reuse an unreviewed old credential cache. Test host termination, duplicate execution, revoked scope, lost network and storage restoration. Stop all bridges before retiring the sandbox, and preserve or deliberately delete collaboration data according to operator policy.

This supports colocated collaboration only. Connecting a remote host to a laptop or another tenant needs separately reviewed transport/server authentication, authorization, encryption, revocation and partition behavior. No public bind address, reverse proxy, credential-sharing workaround or new hosted service is enabled by this implementation.

Host controllers can now supply a private, renewable execution connection to the
[shared native MCP bridge](managed-connections.md), and the same binary reports
Claude/Codex lifecycle events to that controller with a per-execution hook
credential. This is transport and lifecycle support for an already authorized
endpoint, not automatic remote enrollment or a second daemon. The host/controller
still owns physical execution evidence, safe input delivery and the network route;
the [Desktop integration contract](desktop-remote-integration.md) lists what
remains on that side. The [portable contract verification](portable-contract-verification-2026-09.md)
maps offline and remote-forwarding requirements to this boundary.

## Targets that cannot yet be implemented honestly

| Target | Missing boundary / next evidence |
| --- | --- |
| Aider (#47) | [Scripting documentation](https://aider.chat/docs/scripting.html) establishes programmatic prompting, not a demonstrated full external-tool lifecycle. A prompt/terminal-text wrapper does not satisfy the Bus adapter contract. |
| Muse Code (#32) | Meta's [developer portal](https://dev.meta.ai/) requires login. Obtain accessible vendor documentation for external tools and session ownership; a skills folder or similarly named community MCP server does not establish that boundary. |

For each unresolved target, supply the exact product/version, public tool/session hook, account/permission requirements and maintainer. Then implement the smallest shared-bridge or native shim and run the same independent-host evidence process. These two targets remain blocked, not silently counted as supported or marked complete. All 30 existing candidate paths still require released-host evidence and maintenance ownership.
