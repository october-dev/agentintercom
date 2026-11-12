# October Harness

Experimental native integration record, not a new implementation of the harness. The canonical public project is [october-dev/october-harness](https://github.com/october-dev/october-harness); its built-in [Bus extension](https://github.com/october-dev/october-harness/tree/main/packages/coding-agent/src/extensions/october/bus) consumes public MCP tools and launcher execution credentials. No October Desktop or private service is required for the Bus transport.

Install and authenticate a reviewed, version-pinned October Harness separately. With the local daemon running and a scope created:

```sh
agentintercom agent run --scope my-project --id october-builder --name "October Builder" -- october
```

The launcher reads the protected local scope cache, supplies only execution-scoped Bus authority to the child, maintains the lease and retires when the process exits. Do not combine `--scope` with `--address` or inherited scope/agent credentials. After the independent peer has registered, run `agentintercom link --scope my-project october-builder <peer-id>` from another terminal. Use a different ID for each concurrent process.

There is deliberately no `harness config october-harness` snippet: the public native transport already discovers tools. Do not load the Pi extension or start another self-registering bridge for this identity. Check `october --version`, `agentintercom doctor --json`, and the host's `/bus status`; the generic `doctor --harness` probe is for configuration-backed adapters.

The upstream README and current native source disagree about idle delivery: the inspected source includes its own polling, readiness and delivery handling. Pin the release and test its actual behavior; this manifest certifies neither active delivery nor readiness. In particular, verify process exit versus chat/session replacement, competing heartbeat state, approval pauses, interrupted acknowledgement, cancellation and duplicate execution fencing. Do not infer native behavior from generic MCP conformance or shared ownership.

Complete the [runbook](../../compatibility/RUNBOOK.md) with a distinct harness family, current candidate, sanitized public artifacts and independent review before changing status. Close the launcher to retire; removing a harness installation is not scope deletion. See [operations](../../docs/operations.md) for recovery.
