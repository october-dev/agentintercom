# Pi native extension

Experimental adapter 0.2.1 for [earendil-works/pi](https://github.com/earendil-works/pi). Requires the documented dynamic `registerTool`, `session_start` and `session_shutdown` APIs. This is not built-in Pi MCP support and has not been certified against a released Pi version.

After [creating a local scope](../README.md), generate a token-free configuration:

```sh
agentintercom harness config pi --scope my-project --agent pi-builder --output pi-bus.json
export OCTOBER_BUS_PI_CONFIG=/absolute/path/to/pi-bus.json
pi -e /absolute/path/to/agentintercom/adapters/pi/index.mjs
```

Keep `index.mjs` and `bridge.mjs` together. No extra npm dependencies are required. Treat the configuration as executable code: review the binary path and arguments before loading it. The extension strips inherited Bus credentials from its child environment.

The staged npm distribution also bundles both files and declares Pi's package entry. Once that exact Bus version has actually been published, `pi -e npm:agentintercom@0.2.0` can load it using Pi's [package mechanism](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/packages.md). Keep `OCTOBER_BUS_PI_CONFIG` set to your generated configuration. Preparing a tarball is not publication or a successful Pi installation test.

The native tool API receives the daemon's actual schemas, with names prefixed `agent_intercom_`. Session hooks start/retire the Go bridge; model-turn cancellation cancels only the tool call. No background inbox injection, automatic model turns, or fabricated idle status. Reload/replacement serializes cleanup before reconnecting. Use unique agent IDs per concurrent window.

Use `waitMs: 10000`. Link peers with `agentintercom link --scope my-project pi-builder reviewer`. Run the [verification runbook](../../compatibility/RUNBOOK.md), including reload, new/resumed sessions, cancellation, duplicate IDs and host permission denial. Mock hook tests and real-bridge transport tests do not certify Pi itself.

The implementation follows [public extension hooks](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/extensions.md), reviewed 2026-09-10. Remove the extension/configuration to disconnect; keep the scope if its durable work is still needed.
