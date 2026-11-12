# Portable contract verification (September 2026)

Provisional evidence report for [issue #142](https://github.com/october-dev/october-bus/issues/142) (related: [#126](https://github.com/october-dev/october-bus/issues/126)). It records which portable protocol, helper and client behavior October's cloud v1 plan relies on, what was proven and against which artifact, and what is still pending.

**Status:** No portable gap demonstrated so far. B02 and B03 are not filed. Whether they are needed stays pending the hosted-fixture comparison, which blocks closing #142.

This report changes no protocol, runtime, helper, SDK, schema, migration or release. It unlocks no product or launch flag and does not qualify laptop-off (v2) autonomy.

## Scope

Fixed by #142 and not revisited here:

- Local communication and task work stays available offline. Remote messages queue durably, and disconnected task attempts may run independently.
- October Desktop owns its local router and UI. October's hosted service owns tenancy, the cloud inbox, compute and authorization. This repository owns portable protocol, client and helper behavior.
- Autonomous SSH collaboration while the laptop is closed is v2. Disconnection does not prove the laptop is asleep.
- A helper release follows only a demonstrated portable gap, and never enables a product flag by itself.

Out of scope: hosted tenancy, accounts, the managed cloud service, provider selection, a laptop-independent SSH relay, NATS, and any daemon redesign.

## Artifacts

| Artifact | Identity | Status in this report |
| --- | --- | --- |
| Release `v0.1.0-rc.5`, Linux amd64 archive | `october-bus_0.1.0-rc.5_linux_amd64.tar.gz`, SHA-256 `046dc468a07e630d370804ab8a832222d66ffafe81b2dccde532cfd6f5ded620`; source `8357265c1113b6c7f87fb11d87924ee122e97156` | Executed (see [Released-artifact evidence](#released-artifact-evidence)) |
| `october-bus` in that archive | SHA-256 `14ff928dc99de16752bda64e2f7ef6099cb41a76b710b686d27225e074163338`, equal to `binarySha256` in `remote-bus-runtime.json`; `version --json` reports runtime `0.1.0-rc.5`, protocol `0.1` | Executed as daemon and as ordinary `mcp stdio` helper |
| `october-bus-conformance` in that archive | SHA-256 `3e3defe93798801ec86c1e82bc0adc59b2b852a7f5c7ea83d4214d8c4e2ca922` | Used as the test runner |
| `remote-bus-runtime.json` (rc.5) | SHA-256 `5300cce8738f1c0ca5eed51ebb87211971428bfc275e51f28a5acc3bd7220e7e`; `qualification: release`, `sourceModifiedAtBuild: false` | Checked against the extracted binary |
| Source | `83f7a635e18db4e6ab81b0a2e3a8dfcf153d818e` (`v0.1.0-rc.5-8-g83f7a63`) plus the four tests below | Executed (Go source tests) |
| Protocol | `spec/0.1` at `83f7a63` | Reference for field mapping |
| npm `@october-dev/october-bus@0.1.0-next.15` | Not downloaded | **Unqualified by this report.** Registry integrity, provenance and a matching qualification run are not recorded here. |
| Other rc.5 platforms (darwin, windows, linux/arm64) | Not downloaded | Not evaluated |
| Prior cross-repo evidence | Desktop `d5f7a0e2dbeb72cdd69b503e0f50367b79bfde88` native bridge test; Saturday PR #63 `6c867ee3eae760f7616b7b6b8d507b5e509ef46b` connection writer ([integration follow-up](desktop-remote-integration.md#integration-follow-up-2026-09-24)) | Prior; not rerun |
| Hosted versioned contract fixtures | None attached to #142 (no comments as of 2026-09-28) | **Pending** |

### Source equivalence between rc.5 and `83f7a63`

`git diff v0.1.0-rc.5 83f7a63 -- bus cmd sdk/typescript/src` touches only `bus/store.go` and `bus/runtime_test.go`: #139 makes resolving an unknown escalation return `NOT_FOUND`. No matrix row depends on that path. Other commits in the range change `conformance/` (#134: the runner waits for heartbeat renewal instead of a fixed sleep), `spec/` (#136, #137: state machines and payload schemas, documentation and schema tests only), docs and release tooling.

That supports one narrow inference: the managed `--connection-file` and `hook` paths of the released binary behave as the source tests at `83f7a63` show. It is labelled **source-equivalence inference** below and is not an executed binary test. It establishes nothing about npm provenance, and the spec revision shipped in the archive is older than `83f7a63`.

## Helper transport versus semantic compatibility

In managed mode, `mcp stdio --connection-file` lists the upstream authority's tools once at startup and forwards their definitions, arguments and results (the tool-listing loop in `runMCPStdio`, which registers each upstream tool with `structuredArgumentTypes` and forwards calls through `coerceStructuredArguments`). The only change it makes is structured-argument coercion: a top-level string holding JSON becomes an array or object where the tool schema requires one. The upstream authority is Desktop Core or the hosted `/api/compute/workers/<env>/mcp` endpoint. The Go daemon and SQLite store are the reference implementation of protocol 0.1, which has closed schemas (`spec/0.1/schemas/protocol.schema.json`) and specific persisted representations (`bus/protocol.go`).

Classification rule used below:

> The helper can transport authority-defined tool fields present in the startup schema, subject to its existing structured-argument coercion. A naming or envelope difference is adapter-only when the concrete mapping preserves the required identity, authorization, durability, deduplication and result semantics. A new semantic requirement remains pending fixture comparison even if its bytes pass through the helper.

`TestMCPStdioManagedConnectionForwardsAuthorityDefinedFields` proves only the transport half of this rule. It certifies no hosted fixture.

## Task attempts and results

One execution can claim, release and claim the same task again (`Store.ClaimTask`, `Store.ReleaseTask` and `Store.CompleteTask`), and two independent authorities can each hold their own task records. So `(taskId, executionId)` attributes work to an execution; it is **not** a distinct attempt identity. Tasks, execution-attributed progress and events, and completion notes give local ownership and result information. They do not give every disconnected attempt a general unique identity or a retained result.

- The hosted fixture must define the attempt boundary and the authority scope.
- Its adapter must show stable, distinct attempt/result correlation, including release and reclaim within one execution.
- Portable sufficiency is **pending** that comparison.
- Events are evidence, not an indefinitely available substitute for a missing field: cursors can require resync, and progress need not exist for every claim.
- Local single-winner arbitration must stay intact. No attempt API is designed here.

## Capability matrix

Evidence kinds: **source test** (Go test at `83f7a63` plus this change, run locally with go1.27.0), **mocked fault injection** (a source test with a synthetic proxy, authority or writer), **released artifact** (rc.5 linux/amd64 binary executed), **source-equivalence inference** (see above), **code inference** (read from source, not executed), **prior cross-repo**, **live** (none performed).

| Requirement | Classification | Evidence | Evidence kind |
| --- | --- | --- | --- |
| Local send, receive and task work independent of any remote | proved | Local daemon with SQLite; `TestSQLitePreservesAcceptedWorkAcrossRestart` (`bus/runtime_test.go`: an agent token issued before the restart still reserves the message accepted before it), `TestAcceptedWriteAndLockRecoverAfterProcessDeath` (`bus/fault_recovery_test.go`); rc.5 `local-runtime` profile, all 23 checks, against the extracted daemon listening only on loopback | source test; released artifact |
| Durable IDs and receipts | proved | `Message.id`, `DeliveryReceipt` (`bus/protocol.go`); `TestDurableRequestRedeliveryAcknowledgementAndReply`, `TestInspectReceiptEndToEnd`; rc.5 checks `durable-request-and-idempotency`, `reservation-delivery-and-acknowledgement`, `durable-messaging-and-acknowledgement` | source test; released artifact |
| Retry deduplication | proved | The `messages_idempotency` unique index on `(scope_id, from_kind, from_id, idempotency_key)` (`bus/schema.go`); `TestMessageIdempotencyRejectsPayloadChanges`; **new** `TestColdRestartPreservesIdempotencyAndRedelivery`, **extended** `TestMCPStdioManagedConnectionDoesNotReplayLostMutation`; rc.5 check `idempotent-send`. **Adapter obligation (Desktop and hosted):** send one key per logical send. The Go SDK never generates one, and the TypeScript SDK exports `newIdempotencyKey()` but does not apply it to sends, although `spec/0.1/README.md` says SDKs SHOULD. | source test; mocked fault injection; released artifact |
| Changed payload on the same key | proved (`CONFLICT`) | `TestMessageIdempotencyRejectsPayloadChanges`; **new** `TestColdRestartPreservesIdempotencyAndRedelivery` (after restart) | source test |
| Cold local restart | proved | **New** `TestColdRestartPreservesIdempotencyAndRedelivery`: file-backed store closed and reopened, both agents re-registered as new executions, same `messageId` and `acceptedAt` for the retried key; `TestSQLitePreservesAcceptedWorkAcrossRestart`: without re-registering, the reviewer's pre-restart agent token still reserves the message accepted before the store was closed and reopened; `TestTaskProgressSurvivesRestart` | source test |
| Lost acknowledgement and replay | proved (at-least-once handoff only); committed acknowledgement with a lost response: code inference | **Executed:** **new** `TestColdRestartPreservesIdempotencyAndRedelivery`: a delivered, unacknowledged message is reserved again after restart, exactly once, keeping its first `deliveredAt`; the acknowledgement counts 1, a repeated acknowledgement counts 0 without error, and the sender's receipt for that retained message ends `acknowledged`. **Code inference, not executed:** `Server.acknowledgeMessages` reaches `Store.AcknowledgeMessages` through `Runtime.AcknowledgeMessages`; the store moves the caller's `delivered` rows to `acknowledged` in one transaction before any response is written. If that response is lost after commit, the durable state stays `acknowledged`, and an explicit repeat under valid current authority counts 0 while the message is retained. A count of 0 alone does not prove prior success: an ID with no eligible delivered row addressed to the caller also counts 0 without error, so the caller reconciles through `Store.Receipt`, which either participating agent may read (`TestMCPStdioSelfRegisteredLifecycle` reads it as the recipient through the bridge). No acknowledgement-response-loss transport test ran: the proxy in `TestMCPStdioManagedConnectionDoesNotReplayLostMutation` drops a `message_peer` response, not `acknowledge_messages` or a hook route. Hook/controller acknowledgement uncertainty stays pending (see Uncertain injection). | source test; code inference |
| Lost response / uncertain mutation outcome | proved: the call fails and the outcome stays unknown until reconciled | **Extended** `TestMCPStdioManagedConnectionDoesNotReplayLostMutation`: the proxy drops the response after the daemon commits; the call fails; reconnecting does not replay it; an explicit retry with the same `idempotencyKey` returns a `messageId`, and the receiver holds exactly one message with that ID. Streamable client retries disabled (`MaxRetries: -1` on the `StreamableClientTransport` built by the `connect` function in `runMCPStdio`, which managed mode uses for its initial connection and every reconnect). | mocked fault injection |
| Actor and execution scoping | proved | `executionId` per registration, `claimed_execution_id`; `TestEveryProtectedAgentMutationFencesReplacedAndExpiredExecution` (`bus/hardening_test.go`), `TestAuditReplacementMustFenceMutations` (`bus/production_regression_test.go`), `TestExecutionReplacementRetiresPreviousToken`; rc.5 checks `execution-replacement-and-stale-claim-recovery`, `execution-replacement` | source test; released artifact |
| Stable recipient and correlation IDs | proved in protocol; mapping any fixture is pending | `to`/`toKind`, `responseTo`, `responseMessageId`; rc.5 checks `correlated-response`, `correlated-request-and-response` | released artifact |
| Originating principal | proved for existing kinds (`agent`, `a2aPrincipal`); richer hosted identity pending | Credential-derived `from`/`fromKind`; `TestHTTPAndMCPUseTheSameAgentAuthority`; rc.5 checks `credential-isolation`, `scoped-a2a-principal-credentials`. A payload-supplied origin is never authority. | source test; released artifact |
| Helper transport of authority-defined fields | proved (transport only) | `TestMCPStdioBridgeForwardsDaemonTools` (daemon tool set, ordinary mode); **new** `TestMCPStdioManagedConnectionForwardsAuthorityDefinedFields`: a synthetic go-sdk authority's tool definition, the arguments it received (nested object, array, null and number under `metadata`, plus a top-level `encoded` field whose schema type is `["string","object"]` holding a JSON-looking string, which arrives as a string rather than being coerced to an object; see [Follow-up (2026-09-30)](#follow-up-2026-09-30)) and its structured result are semantically equal on both sides of managed mode | mocked fault injection |
| Distinct task-attempt identity and results | **pending fixture** | See [Task attempts and results](#task-attempts-and-results) | — |
| Local arbitration and receipt safety preserved | proved | `TestTaskClaimsRespectDependenciesAndOwnership`, `TestTaskReleaseAndExecutionReplacementRecoverClaims`; rc.5 checks `task-dependencies-release-and-completion`, `execution-replacement-and-stale-claim-recovery`, `shared-task-lifecycle` | source test; released artifact |
| Reconnect and backoff | helper: proved. Backoff: Desktop/hosted adapter. | Reconnect is demand-driven on the next call and never replays (`cmd/october-bus/mcp_reconnect.go`); `TestManagedMCPBridgeRecoversAfterRefusalWithoutReplay`, `TestManagedMCPBridgeReconnectsAfterAuthorityRestart`, `TestManagedMCPBridgeRenewsExpiredCredentialWithoutRestart`, `TestManagedMCPCloudHTTPSConnection`. The helper has no background reconnect loop, so it has nothing to back off. Backoff before starting a new call belongs to Desktop or the hosted service, and the helper never automatically retries an ambiguous mutation. Released managed mode was not executed. | source test; mocked fault injection; source-equivalence inference (rc.5) |
| Explicit retirement and replaced execution | proved | `POST /v1/me/retire` and the `session-retirement` health feature; `TestManagedMCPConnectionRefreshAndRetirement`, `TestAuditSessionCloseMustRetireAuthorityAndClaims`, `TestSessionContextCancellationRetiresAuthority`; rc.5 check `execution-replacement` | source test; released artifact |
| Disconnected is not asleep | proved: reports disconnected or unknown | `reachable` means the lease is current and the lifecycle is not offline (the `Reachable` computation in `scanAgent`). Neither the protocol nor the helper failure classes (the `connectionFailure` constants in `cmd/october-bus/mcp_connection.go`) have a sleep state. `TestOfflineHeartbeatCannotClaimReadiness`; rc.5 checks `clean-offline-lifecycle`, `clean-and-expired-lifecycle` (a stopped heartbeat leads to lease expiry, unreachable status and a recovered claim). Telling sleep from a crash is Desktop/host-owned. | source test; released artifact |
| Uncertain injection | portable side proved; controller-side uncertainty pending | **New** `TestHookDoesNotAcknowledgeWhenStdoutRejectsInjection`: when stdout takes a partial write and fails, the hook neither posts `/hook/inbox-ack` nor `/hook/context-ack` (Claude and Codex flavors). `TestHookClaudePrePromptPullsAndAcknowledges` covers the success path. Stdout accepting the bytes proves native handoff, not model consumption. The tmux foreground proof is Desktop-owned. Uncertainty after a lost acknowledgement response is pending fixture/adapter. | source test |
| Hosted versioned fixture comparison | **pending; blocks closure** | None attached | — |
| Tenancy, accounts, cloud inbox, compute, authorization, provider selection, laptop-off SSH | hosted/Desktop; excluded | #142 item 6 | — |

No row is classified as missing portable.

## Observations

These are recorded with code evidence only. None is a demonstrated gap; revisit one only if a required hosted fixture fails because of it. Contract shapes belong in B02, designed against that failing fixture.

- `add_task` has no idempotency key (`Store.AddTask`). A caller retry after a lost response creates a second task. Task creation accepts either scope or agent authority, so no identity policy for such a key is defined yet.
- `Task` exposes `claimedBy` (the agent, `Task.ClaimedBy`), not the claiming execution.
- Retrying `claim_task` or `complete_task` after a lost response returns `CONFLICT` (the `CodeConflict` returns in `Store.ClaimTask` and `Store.CompleteTask`). Task ownership errors are an **Open** item in `spec/0.1/state-machines.md` ([#28](https://github.com/october-dev/october-bus/issues/28)); the new tests do not pin them.
- `bus.AgentSession` ends on its first heartbeat failure (`AgentSession.heartbeat`). That session helper is not on the managed-connection path.
- The bridge fixes its tool list at startup. Tool changes upstream are not propagated to an already running bridge.

## Delivery guarantees

- Transport delivery never implies exactly-once model side effects. Delivery is at-least-once until acknowledged, and acceptance is not processing.
- Deduplication lasts only while the original message is retained. A client's retry window must be shorter than the operator's retention cutoff (`spec/0.1/README.md`).
- A connection or call can fail while the mutation's commit outcome is unknown. The caller resolves it by retrying with the same idempotency key or by reading the receipt.
- A disconnect never proves rollback, processing or laptop sleep.

## Artifact acceptance

Decided after the runs below, for linux/amd64 only:

| Artifact | Decision | Reason |
| --- | --- | --- |
| rc.5 `october-bus` as local daemon | **Accepted** for the rows marked released artifact | Checksum and SLSA provenance verified; binary hash matches `remote-bus-runtime.json`; `local-runtime` passed 23/23 and `mcp-adapter` 14/14 against it |
| rc.5 `october-bus` as ordinary `mcp stdio` helper | **Accepted** for the `mcp-adapter` rows | `mcp-adapter` profile passed 14/14 with this executable as the adapter command |
| rc.5 `october-bus` managed mode (`--connection-file`) and `hook` | **Accepted by inference only** | Not executed from the released binary. The rc.5→`83f7a63` diff under `bus`, `cmd` and `sdk/typescript/src` is #139 alone, and the source tests pass at `83f7a63`. A consumer requiring executed-binary evidence for these paths must run it. |
| npm `0.1.0-next.15` | **Not accepted here** | No integrity, provenance or qualification run recorded |
| Other rc.5 platforms | **Not evaluated** | Not downloaded or run |

No product or launch flag follows from these decisions.

## Hosted fixture request

Prepared for #142; posting it is a separate, explicitly authorized action.

> To finish #142 we need the hosted service's concrete, versioned contract fixtures. For each fixture, please give the contract name, semantic version, source commit or build, file names and SHA-256. The bundle should include success and failure cases for:
>
> - outbound and inbound messages;
> - uncertain sends and retries;
> - principal identity;
> - execution replacement and retirement;
> - independent task attempts and results, with the attempt boundary defined.
>
> Prose, screenshots, unversioned or mutable examples cannot be used as evidence. Please do not attach tenant data or credentials; where a fixture contains them, share a secure artifact ID and its hash instead.

Fixture concepts will be mapped without changing authority semantics:

| Fixture concept | Bus field |
| --- | --- |
| recipient | `to`/`toKind` |
| logical send / retry | `idempotencyKey` |
| accepted ID | `messageId` |
| correlation | `responseTo`/`responseMessageId` |
| actor | credential-derived `from`/`fromKind` (never payload-supplied) |
| execution | `executionId` |
| attempt/result | pending ([Task attempts and results](#task-attempts-and-results)) |

B02 is populated only if a required fixture fails, and then with the exact failing fixtures.

## Commands and results

Run on 2026-09-28, Linux 7.1.8 x86_64 (Fedora 44).

### Source and mocked evidence

Toolchain: go1.27.0 linux/amd64 from the Go module cache, matching the `toolchain` line in `go.mod`; not installed system-wide.

```sh
go test ./... -count=1              # 10 packages ok
go test -race -count=1 ./...        # 10 packages ok
go vet ./...                        # ok
go build ./...                      # ok
gofmt -l .                          # no output
go test ./bus -run TestColdRestartPreservesIdempotencyAndRedelivery -count=1 -v
go test ./cmd/october-bus -run 'TestMCPStdioManagedConnectionDoesNotReplayLostMutation|TestMCPStdioManagedConnectionForwardsAuthorityDefinedFields|TestHookDoesNotAcknowledgeWhenStdoutRejectsInjection' -count=1 -v
```

New or extended tests:

- `TestColdRestartPreservesIdempotencyAndRedelivery` (`bus/runtime_test.go`)
- `TestMCPStdioManagedConnectionDoesNotReplayLostMutation`, extended in place (`cmd/october-bus/mcp_connection_test.go`)
- `TestMCPStdioManagedConnectionForwardsAuthorityDefinedFields` (`cmd/october-bus/mcp_connection_test.go`)
- `TestHookDoesNotAcknowledgeWhenStdoutRejectsInjection` (`cmd/october-bus/hook_test.go`)

As a check that the hook test detects a regression, acknowledging regardless of the stdout result was applied temporarily to `cmd/october-bus/hook.go`; the test failed with the extra `inbox-ack` and `context-ack` requests and passed again once the change was reverted.

The TypeScript SDK is unchanged and was not run here. Its CI jobs (`typecheck`, `test:errors`, `test:integration`) are source evidence only and are not npm `next.15` qualification.

### Released-artifact evidence

```sh
gh release download v0.1.0-rc.5 -R october-dev/october-bus \
  -p 'october-bus_0.1.0-rc.5_linux_amd64.tar.gz' -p checksums.txt -p remote-bus-runtime.json
sha256sum --ignore-missing -c checksums.txt      # archive OK, remote-bus-runtime.json OK
gh attestation verify october-bus_0.1.0-rc.5_linux_amd64.tar.gz --repo october-dev/october-bus
# exit 0; SLSA v1 provenance, source digest 8357265c…, signer
# .github/workflows/release.yml@refs/tags/v0.1.0-rc.5 (gh 2.76.2)
tar -xzf october-bus_0.1.0-rc.5_linux_amd64.tar.gz
D="$PWD/october-bus_0.1.0-rc.5_linux_amd64"
"$D/october-bus" version --json                  # runtime 0.1.0-rc.5, protocol 0.1
sha256sum "$D/october-bus"                       # equals binarySha256 for amd64
export OCTOBER_BUS_DATA_DIR="$PWD/data" OCTOBER_BUS_RUNTIME_DIR="$PWD/run"   # fresh private dirs
"$D/october-bus" start &                         # foreground daemon; loopback, isolated dirs
"$D/october-bus-conformance" --profile local-runtime --format json
"$D/october-bus-conformance" --profile mcp-adapter \
  --adapter-command "$D/october-bus" --adapter-arg mcp --adapter-arg stdio --format json
"$D/october-bus" stop                            # "October Bus stopped"; daemon exited 0
```

Results:

- `local-runtime`, exit 0, 23 passed, 0 failed: `health-and-version`, `scope-authority`, `portable-scope-archive`, `registration-and-peer-link`, `scope-route-authority-errors`, `owner-controlled-agent-card-publication`, `scoped-a2a-principal-credentials`, `addressable-output-streams`, `presence-and-discovery`, `durable-request-and-idempotency`, `reservation-delivery-and-acknowledgement`, `correlated-response`, `message-expiry`, `bounded-inbox-wait`, `task-dependencies-release-and-completion`, `durable-task-progress`, `execution-replacement-and-stale-claim-recovery`, `human-escalation-boundary`, `resumable-scope-events`, `storage-diagnostics-and-retention`, `scope-isolation`, `mcp-tool-surface`, `clean-offline-lifecycle`.
- `mcp-adapter`, exit 0, 14 passed, 0 failed: `health-and-version`, `adapter-start-and-execution-identity`, `external-heartbeat`, `long-poll-delivery`, `exact-peer-discovery`, `durable-messaging-and-acknowledgement`, `correlated-request-and-response`, `idempotent-send`, `bounded-context`, `shared-task-lifecycle`, `human-escalation-boundary`, `execution-replacement`, `clean-and-expired-lifecycle`, `credential-isolation`.

The `local-runtime` run qualifies the extracted daemon. The `mcp-adapter` run qualifies the extracted helper in ordinary `mcp stdio` mode against that daemon. Neither exercises `--connection-file` reconnect or hook paths.

### Prior cross-repo evidence

Not rerun: see [Integration follow-up (2026-09-24)](desktop-remote-integration.md#integration-follow-up-2026-09-24).

### Live evidence

None performed. No hosted service, tenant or live cloud endpoint was contacted.

### Follow-up (2026-09-30)

Source: `c227136` (#143 on `main`) plus the change to `TestMCPStdioManagedConnectionForwardsAuthorityDefinedFields` in this follow-up: its JSON-looking `encoded` string moved from `metadata` to a top-level argument whose schema type is `["string","object"]`. Toolchain: go1.27.0 linux/amd64 from the Go module cache, as above. Linux 7.1.8 x86_64 (Fedora 44). No production code changed. The released-artifact, prior cross-repo and live evidence above was not rerun, and no hosted fixture was compared.

```sh
go test ./... -count=1              # 10 packages ok
go test -race -count=1 ./...        # 10 packages ok
go vet ./...                        # ok
go build ./...                      # ok
gofmt -l .                          # no output
go test ./cmd/october-bus -run TestMCPStdioManagedConnectionForwardsAuthorityDefinedFields -race -count=1 -v   # PASS
go test ./bus -run 'TestSQLitePreservesAcceptedWorkAcrossRestart$' -race -count=1 -v                         # PASS
```

As a check that the forwarding test detects a regression, the `!allowed["string"]` guard in `structuredArgumentTypes` was removed temporarily. `TestMCPStdioManagedConnectionForwardsAuthorityDefinedFields` failed with "arguments changed in transit", and `TestStructuredArgumentCoercion` failed too. With the guard restored, `git diff` showed no production change, and the forwarding test passed again. The full commands above ran with the guard in place, on Go sources identical to the final tree.
