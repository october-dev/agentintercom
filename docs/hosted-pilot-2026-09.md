# Hosted MCP connector pilot report (September 2026)

Status record for [issue #122](https://github.com/october-dev/october-bus/issues/122): deploying and verifying the hosted October Bus MCP connector for the Muse Connector Platform. It records what was deployed, what was proven, what was found, and what remains. It is not a claim of Muse approval; Meta's review is tracked separately.

## Outcome

`https://bus.october.dev/mcp` is live on a single Linux server. Verified on production: the public endpoint, authentication, admin-route and traversal hiding, the MCP protocol, useful work (a Muse custom-connector round trip with a real coding harness on the receiving side), the Muse-specific gate, and the restart, reboot, and backup drills. This report accepts the dated staging results for two-scope isolation and the listed recovery subchecks; those checks were not repeated on production. The connector is an operator-provisioned pilot: one scope, one connector key, explicitly linked agents, no self-service, no OAuth, no automatic wake-up.

## Code

| Item | Value |
| --- | --- |
| Gateway implementation | [PR #120](https://github.com/october-dev/october-bus/pull/120) `feat/hosted-mcp-gateway` @ `dba3ece` |
| Fixes found by this deployment | [PR #125](https://github.com/october-dev/october-bus/pull/125) `fix/hosted-gateway-review` @ `7fc501a`, stacked on #120 |
| Deployed build | `october-bus 0.1.0-pr120fix.7fc501ac58a1` (static linux/amd64, `-X bus.Version` stamped) |
| Verification | `go test -race ./...`, `go vet`, `gofmt -l` clean; nine CI checks green on #125 |

Defects in #120 found and fixed in #125: gateway start racing the daemon after reboot and tripping systemd's start limit; `connectTo` in gateway config failing on a fresh deployment; shared request budget taken before authentication on `/mcp` (at `7fc501a` the `/bus` bridge path still takes the shared budget before credential validation; see the [#125 audit](https://github.com/october-dev/october-bus/pull/125#issuecomment-5811395335)); `GET`/`DELETE /mcp` allowlisted although the daemon is stateless; connector and bridge executions never leaving `starting`; forwarded-header hygiene; missing 429/502 tests.

## Deployment

| Item | Value |
| --- | --- |
| Host | Hetzner Cloud, US East, Ubuntu 26.04, 2 vCPU / 2 GB / 38 GB |
| DNS | `A bus.october.dev` → server IPv4; no AAAA |
| Services | `october-bus.service` (daemon, `127.0.0.1:4765`) and `october-bus-gateway.service` (gateway, `127.0.0.1:8787`), user `october-bus`, state `/var/lib/october-bus`, runtime `/run/october-bus` |
| HTTPS | Caddy, Let's Encrypt certificate for `bus.october.dev`, HSTS; only 22/80/443 open |
| Provisioning | `scope create muse-pilot`; `gateway init --public-url https://bus.october.dev --scope muse-pilot --agent muse`; key stored as digest only; no `connectTo` |
| Backup | `/etc/cron.daily/october-bus-backup`: `october-bus backup` snapshot plus private credential files, 0700 directory, 14-day retention; restore tested into a scratch daemon |
| Drills | gateway restart 3 s to ready; daemon restart 3 s (gateway waits for readiness); two full reboots recovered in 22 s and 24 s |

Incident during deployment: Ubuntu's packaged Caddy 2.6.2 panicked on `systemctl reload caddy` and its unit has no `Restart=`, leaving port 443 down until restarted. Mitigation: a drop-in with `Restart=on-failure`; use `systemctl restart caddy` on this build, or install current Caddy from the official repository.

## Verification gates (issue #122 §5)

| Gate | Environment | Result |
| --- | --- | --- |
| Public endpoint | production | `/health/ready` 204 over public HTTPS from outside; valid certificate; no interactive proxy challenge |
| Authentication | production | missing key 401 with `WWW-Authenticate: Bearer`; invalid key 401; valid key works; retired keys non-functional |
| Scope boundaries | production | admin routes and traversal 404 |
| Scope boundaries | staging | encoded paths 404; two-scope isolation (no peers, no messages, no receipts across scopes) |
| MCP protocol | production | `scripts/check-hosted-mcp.mjs` passes; 15 tools; stateless JSON responses; `GET /mcp` 405 |
| Useful work | production | Muse request → gateway → `mcp stdio --remote` bridge → Claude Code read a file on the laptop and replied → Muse displayed the reply; both receipts `acknowledged`; about 15 s end to end |
| Recovery | production | gateway restart 3 s to ready; daemon restart 3 s; two reboots 22 s and 24 s; backup restored into a scratch daemon |
| Recovery | staging | idempotent replay returns the original message id and a mismatched body gets 409 `CONFLICT`; a request queued while the agent was offline was delivered after reconnect in the observed run; gateway restart preserves queued work; daemon stop → 502 then clean exit; host suspend ends leases, daemon and data intact |
| Muse-specific | production | Muse custom connector on the production URL: secure credential entry, Bearer header, tool discovery, approval prompt before each tool call, `message_peer`, same-turn `check_inbox` retrieval, `acknowledge_messages` |

Evidence record: the [staging update of 22 Sep 2026](https://github.com/october-dev/october-bus/issues/122#issuecomment-5778836003) on issue #122 ran the same build `7fc501a` behind an HTTPS tunnel stand-in for `bus.october.dev`; the [production update of 23 Sep 2026](https://github.com/october-dev/october-bus/issues/122#issuecomment-5786008817) records the results on the Hetzner host.

Clients exercised: Muse as connector client; Claude Code 2.1.278 as connector client over HTTP MCP with a Bearer key, and as receiving harness through `mcp stdio --remote`. No other product names were tested.

## Findings for documentation

- In the tested runs, LLM connector clients polled `check_inbox` on their own after `message_peer` when the receiving agent answered quickly, with or without a "wait for the reply" instruction. Product copy may describe same-turn results for online agents but must still say results are collected later when the agent is slow or offline.
- Delivered but unacknowledged messages can be returned by later `check_inbox` calls, and reconnect does not guarantee exactly-once processing; clients should acknowledge only after processing succeeds ([delivery states](https://github.com/october-dev/october-bus/blob/7fc501ac58a1e1cda8195a70ba02f2e9a9ba8ad1/spec/0.1/README.md#L81-L92), [`check_inbox`](https://github.com/october-dev/october-bus/blob/7fc501ac58a1e1cda8195a70ba02f2e9a9ba8ad1/spec/0.1/mcp.md#L44)). On staging, Muse did not acknowledge replies; on production it did. Docs should tell connector clients to call `acknowledge_messages` after processing, and must not promise automatic polling, automatic acknowledgment, or exactly-once side effects.
- One connector client omitted the optional `idempotencyKey`. The `message_peer` description should say: supply a fresh key for every new logical send; a retry of the same send reuses the same key and identical content. Deduplication is by scope, sender, and key ([§Idempotency](https://github.com/october-dev/october-bus/blob/7fc501ac58a1e1cda8195a70ba02f2e9a9ba8ad1/spec/0.1/README.md#L94-L98)), so a retry with a fresh key creates a second message, and the same key with different content returns `CONFLICT`.
- The connector agent id (default `muse`) is reserved for the gateway; a bridge registering the same id displaces the connector execution.
- A laptop that suspends loses its execution lease; its bridge must be restarted. The daemon and all stored messages are unaffected.

## Remaining items (owner)

- Product page update at `www.october.dev/october-bus` (website repository).
- 512 × 512 PNG logo under 256 KiB, validated before upload.
- Operator contact and a private credential-delivery path for reviewers.
- Submission at the Muse Connector Platform and tracking of Meta's review.
- Merge order: #120, then #125 retargeted to `main`.
