# October Bus 0.1.0-rc.5 / npm 0.1.0-next.15

Native `v0.1.0-rc.5` is available on GitHub Releases and npm `0.1.0-next.15` on the
`next` channel. The npm release carries the same runtime implementation with a
follow-up publication metadata fix. The two version numbers identify separate
distribution channels. Protocol version remains `0.1`.

## Changes since native rc.4

- Managed remote MCP sessions use `mcp stdio --connection-file` and native
  lifecycle hooks. Credentials stay in the controller-managed connection file,
  refresh between requests and expire or retire with the execution. Hook failures
  report sanitized errors without replaying an uncertain write.
- `mcp check` checks the actual JSON-RPC response. `version --json` reports runtime
  and protocol versions for installers.
- Hosted HTTPS MCP connections validate their gateway origin and execution
  credentials. Controller integration checks cover renewal, hooks, human waits
  and retirement.
- npm distributes the TypeScript SDK and native CLI together for Linux, macOS
  and Windows on x64 and arm64. Exact-version native dependencies provide the
  executable; installing the CLI does not require Go.
- Managed SDK sessions require the runtime's `session-retirement` feature before
  registration. Upgrade the runtime together with the SDK; rc.4 does not support
  this contract.

## Installation

For local CLI use with Node 20 or newer:

```sh
npx @october-dev/october-bus@0.1.0-next.15 demo
npm install @october-dev/october-bus@0.1.0-next.15
```

Native users can install the appropriate `0.1.0-rc.5` archive from GitHub Releases
without Node. The release includes checksums, build attestations, SBOMs, a
standalone license and `remote-bus-runtime.json`. Desktop consumes the two Linux
archives and that manifest as a pinned bundle; publishing Bus does not update an
existing Desktop installation. See [the integration instructions](desktop-remote-integration.md)
for artifact verification and installation.

The npm version advances past the unpublished `next.14` development bundle so
that a deployed helper built from that older source cannot be confused with this
release. Existing `latest` remains unchanged; use the explicit version or `next`.

## Qualification boundary

The managed transport has automated Go race tests, actual Desktop Core transport
tests and a check using Saturday's connection-file writer. Those checks do not
establish live SSH/model qualification or a production cloud deployment. Issue
[#126](https://github.com/october-dev/october-bus/issues/126) still needs the consumer
artifact pin and the live-host checks after publication. Cloud WebSocket support
is not part of this release. This is not a stable 0.1 launch declaration.
