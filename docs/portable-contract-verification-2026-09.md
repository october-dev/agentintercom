# Portable contract verification (September 2026)

Provisional evidence report for [issue #142](https://github.com/october-dev/october-bus/issues/142) (related: [#126](https://github.com/october-dev/october-bus/issues/126)). It records which portable protocol, helper and client behavior October's cloud v1 plan relies on, what was proven and against which artifact, and what is still pending.

**Status:** The hosted contract has now been compared case by case ([Hosted contract v1 comparison](#hosted-contract-v1-comparison), [follow-up 2026-10-01](#follow-up-2026-10-01)). No portable gap is demonstrated so far for contract digest `8a0901a2…1cc62365`, but one contract requirement is unresolved: §3.4 makes the origin-stable `streamPosition` part of conflict detection, while the v1 envelope has no such input and the hash preimage omits it ([Unresolved requirement](#unresolved-requirement)). Until it is resolved, the no-gap conclusion is not complete: B02 and B03 are not filed, whether they are needed stays pending, and no release follows. This proves contract-shape compatibility only: the Desktop and hosted adapter obligations it names are not implemented or qualified here, the hosted contract PR is still unmerged, and a changed digest reopens the affected rows.

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
| Source (2026-10-01 follow-up) | `f4174ebab93b6d5cc43a3e8b0ee13ed67c3f0dc6` plus two new tests ([Follow-up (2026-10-01)](#follow-up-2026-10-01)) | Executed (Go source tests) |
| Protocol | `spec/0.1` at `83f7a63` | Reference for field mapping |
| npm `@october-dev/october-bus@0.1.0-next.15` | Not downloaded | **Unqualified by this report.** Registry integrity, provenance and a matching qualification run are not recorded here. |
| Other rc.5 platforms (darwin, windows, linux/arm64) | Not downloaded | Not evaluated |
| Prior cross-repo evidence | Desktop `d5f7a0e2dbeb72cdd69b503e0f50367b79bfde88` native bridge test; Saturday PR #63 `6c867ee3eae760f7616b7b6b8d507b5e509ef46b` connection writer ([integration follow-up](desktop-remote-integration.md#integration-follow-up-2026-09-24)) | Prior; not rerun |
| Hosted versioned contract fixtures | October Cloud contract v1, `contractVersion 1`: `docs/fixtures/october-cloud-v1.json` at Saturday commit `4effd8565feb2b1e3954ae8e700af5a774a85924` (Saturday PR #87, open and unmerged on 2026-10-01), 23 669 bytes, SHA-256 `8a0901a28dccaf30219c7cbd37979712fc0d2039afa5546493be004a1cc62365`; 25 cases (17 `contractOnly`) over 15 operation keys; normative text `docs/OCTOBER_CLOUD_CONTRACT_V1.md` and schemas `api/lib/compute/contracts/v1.ts` at the same commit. Saturday is private; neither the fixture bytes nor its payloads are copied into this repository. | Fixture inspection (see [Hosted contract v1 comparison](#hosted-contract-v1-comparison)); not executed against a hosted handler |
| Desktop consumer of that contract | `docs/generated/october-cloud-v1.json` in the Desktop checkout at `de9085b88c8101dee104769570787ea6db528a04`, untracked there, identical bytes and digest | Hashed (digest match) |
| Desktop helper pin | `src/shared/remote-bus-runtime.json` at Desktop `de9085b8`: runtime `0.1.0-rc.5`, source `8357265c…`, `qualification: release`, the amd64 hashes listed above. Its cloud worker (`src/backend/compute-worker/supervisor.ts`) and remote launch (`src/main/remote-bus-launch.ts`) run `october-bus mcp stdio --connection-file`. Desktop does not depend on the npm package or the TypeScript SDK. | Code inspection; consumer evidence only |

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

Contract v1 now defines the attempt boundary (§3.3, §3.8): each independent start has its own `attemptId`, an immutable `instructionRevision` with its `instruction` text, the actor, state-event IDs, an optional result, and a `location` that is either a cloud environment and worker generation or a local Desktop device. A worker generation is never an attempt ID. §6 assigns attempt identity to the ledger that creates it: Desktop local storage for offline-originated work, or the hosted transaction for hosted-originated work.

That makes distinct attempts **adapter-only** work for Desktop and the hosted service, not a portable gap. Their obligations:

- Record a new durable `attemptId` at each independent start, and reuse it for every retry of that same start. Because Bus `(taskId, executionId)` is identical across a release and reclaim, the attempt is recorded when it starts, never inferred later from Bus task events. Whether a reclaim after release counts as a new independent start is Desktop's product decision; this report does not make it.
- Store the immutable instruction content and revision with the attempt. A changed location under a reused attempt ID is a changed payload, not a replay (§3.8).
- Keep attempt, event and result IDs consistent, with results distinct per attempt. Synchronize by idempotent union of attempt and event IDs, so a replay never creates a third attempt. One attempt completing never overwrites or cancels another, or marks the task done while another attempt is active.
- Keep Bus `(taskId, executionId)` as attribution only. Local single-winner claim arbitration is unchanged, and `task.claimed`/`task.released` events mark boundaries but can need a resync, so they are evidence, not the attempt record.

None of these adapters is implemented or qualified by this report. No attempt API is added to the Bus.

## Capability matrix

Evidence kinds: **source test** (Go test at `83f7a63` plus this change, run locally with go1.27.0; tests added on 2026-10-01 ran at `f4174eb` plus that follow-up), **mocked fault injection** (a source test with a synthetic proxy, authority or writer), **released artifact** (rc.5 linux/amd64 binary executed), **source-equivalence inference** (see above), **code inference** (read from source, not executed), **fixture inspection** (the hosted contract and its fixture were read and mapped by digest, not executed against a hosted handler), **prior cross-repo**, **live** (none performed).

| Requirement | Classification | Evidence | Evidence kind |
| --- | --- | --- | --- |
| Local send, receive and task work independent of any remote | proved | Local daemon with SQLite; `TestSQLitePreservesAcceptedWorkAcrossRestart` (`bus/runtime_test.go`: an agent token issued before the restart still reserves the message accepted before it), `TestAcceptedWriteAndLockRecoverAfterProcessDeath` (`bus/fault_recovery_test.go`); rc.5 `local-runtime` profile, all 23 checks, against the extracted daemon listening only on loopback | source test; released artifact |
| Durable IDs and receipts | proved | `Message.id`, `DeliveryReceipt` (`bus/protocol.go`); `TestDurableRequestRedeliveryAcknowledgementAndReply`, `TestInspectReceiptEndToEnd`; rc.5 checks `durable-request-and-idempotency`, `reservation-delivery-and-acknowledgement`, `durable-messaging-and-acknowledgement` | source test; released artifact |
| Retry deduplication | proved | The `messages_idempotency` unique index on `(scope_id, from_kind, from_id, idempotency_key)` (`bus/schema.go`); `TestMessageIdempotencyRejectsPayloadChanges`; **new** `TestColdRestartPreservesIdempotencyAndRedelivery`, **extended** `TestMCPStdioManagedConnectionDoesNotReplayLostMutation`; rc.5 check `idempotent-send`. **Adapter obligation (Desktop and hosted):** send one key per logical send. The Go SDK never generates one, and the TypeScript SDK exports `newIdempotencyKey()` but does not apply it to sends, although `spec/0.1/README.md` says SDKs SHOULD. | source test; mocked fault injection; released artifact |
| Changed payload on the same key | proved (`CONFLICT`) | `TestMessageIdempotencyRejectsPayloadChanges`; **new** `TestColdRestartPreservesIdempotencyAndRedelivery` (after restart) | source test |
| Per-recipient fan-out sends | proved (portable primitive); hosted fan-out: adapter-only | **New (2026-10-01)** `TestFanOutSendsAreIndependentAndRecipientScopedForDeduplication`: with one key per recipient, a send to a linked peer is accepted while a send to an unlinked, absent agent is refused with `PERMISSION_DENIED`; retrying the accepted key returns the same `messageId` and `acceptedAt`, retrying the refused one is refused again while the topology is unchanged, reusing the accepted key for the other recipient is a `CONFLICT`, and the recipient holds exactly one message. Because `messageRequestHash` includes `to`, it fails if recipient identity is removed from that hash (checked by temporary mutation, see the follow-up). This is not the hosted stale-generation refusal or lost-aggregate-response test; see `enqueue-fan-out-partial-success-one-committed-one-refused` below. | source test |
| Cold local restart | proved | **New** `TestColdRestartPreservesIdempotencyAndRedelivery`: file-backed store closed and reopened, both agents re-registered as new executions, same `messageId` and `acceptedAt` for the retried key; `TestSQLitePreservesAcceptedWorkAcrossRestart`: without re-registering, the reviewer's pre-restart agent token still reserves the message accepted before the store was closed and reopened; `TestTaskProgressSurvivesRestart` | source test |
| Lost acknowledgement and replay | proved (at-least-once handoff; Bus delivery acknowledgement with a lost response). Hosted transfer acknowledgement: adapter-only. | **Executed:** **new** `TestColdRestartPreservesIdempotencyAndRedelivery`: a delivered, unacknowledged message is reserved again after restart, exactly once, keeping its first `deliveredAt`; the acknowledgement counts 1, a repeated acknowledgement counts 0 without error, and the sender's receipt for that retained message ends `acknowledged`. **New (2026-10-01)** `TestMCPStdioManagedConnectionDoesNotReplayLostAcknowledgement`: the managed bridge runs as the recipient behind a proxy that lets the daemon commit `acknowledge_messages` and then drops the response. The call fails; reconnecting does not replay it; the receipt reads `acknowledged` with an `acknowledgedAt`; an explicit repeat counts 0 and leaves `acknowledgedAt` unchanged; the inbox does not redeliver the message; and acknowledging an unknown ID also counts 0, while its receipt is `NOT_FOUND`. A count of 0 alone therefore does not prove prior success, and the caller reconciles through the receipt, which either participating agent may read. The hosted `ack_messages` transfer acknowledgement is a separate operation (see the stage mapping in [Hosted contract v1 comparison](#hosted-contract-v1-comparison)); hook/controller acknowledgement uncertainty belongs to the execution controller (see Uncertain injection). | source test; mocked fault injection |
| Lost response / uncertain mutation outcome | proved: the call fails and the outcome stays unknown until reconciled | **Extended** `TestMCPStdioManagedConnectionDoesNotReplayLostMutation`: the proxy drops the response after the daemon commits; the call fails; reconnecting does not replay it; an explicit retry with the same `idempotencyKey` returns a `messageId`, and the receiver holds exactly one message with that ID. Streamable client retries disabled (`MaxRetries: -1` on the `StreamableClientTransport` built by the `connect` function in `runMCPStdio`, which managed mode uses for its initial connection and every reconnect). | mocked fault injection |
| Actor and execution scoping | proved | `executionId` per registration, `claimed_execution_id`; `TestEveryProtectedAgentMutationFencesReplacedAndExpiredExecution` (`bus/hardening_test.go`), `TestAuditReplacementMustFenceMutations` (`bus/production_regression_test.go`), `TestExecutionReplacementRetiresPreviousToken`; rc.5 checks `execution-replacement-and-stale-claim-recovery`, `execution-replacement` | source test; released artifact |
| Stable recipient and correlation IDs | proved in protocol; contract v1 mapping: adapter-only | `to`/`toKind`, `responseTo`, `responseMessageId`; rc.5 checks `correlated-response`, `correlated-request-and-response`. Hosted `conversationId`, `correlationId`, `streamPosition` and recipient `generation` have no Bus field and travel as adapter data; hosted `responseTo` maps to Bus `responseTo` only for a real Bus request and response ([Hosted contract input](#hosted-contract-input)). | released artifact; fixture inspection |
| Originating principal | proved for existing kinds (`agent`, `a2aPrincipal`); hosted `actorId`: adapter-only | Credential-derived `from`/`fromKind`; `TestHTTPAndMCPUseTheSameAgentAuthority`; rc.5 checks `credential-isolation`, `scoped-a2a-principal-credentials`. A payload-supplied origin is never authority. Contract v1 derives `actorId`, `resourceOwnerId` and `billingOwnerId` from hosted credentials and rows (§3.1); the adapter preserves the original actor as data and in its key derivation and never collapses it into the forwarding agent. | source test; released artifact; fixture inspection |
| Helper transport of authority-defined fields | proved (transport only) | `TestMCPStdioBridgeForwardsDaemonTools` (daemon tool set, ordinary mode); **new** `TestMCPStdioManagedConnectionForwardsAuthorityDefinedFields`: a synthetic go-sdk authority's tool definition, the arguments it received (nested object, array, null and number under `metadata`, plus a top-level `encoded` field whose schema type is `["string","object"]` holding a JSON-looking string, which arrives as a string rather than being coerced to an object; see [Follow-up (2026-09-30)](#follow-up-2026-09-30)) and its structured result are semantically equal on both sides of managed mode | mocked fault injection |
| Distinct task-attempt identity and results | adapter-only (Desktop and hosted); not implemented or qualified here | Contract v1 §3.3, §3.8 and §6; see [Task attempts and results](#task-attempts-and-results). Local arbitration evidence is the row below; no Bus test proves attempt identity, and none is claimed. | fixture inspection |
| Local arbitration and receipt safety preserved | proved | `TestTaskClaimsRespectDependenciesAndOwnership`, `TestTaskReleaseAndExecutionReplacementRecoverClaims`; rc.5 checks `task-dependencies-release-and-completion`, `execution-replacement-and-stale-claim-recovery`, `shared-task-lifecycle` | source test; released artifact |
| Reconnect and backoff | helper: proved. Backoff: Desktop/hosted adapter. | Reconnect is demand-driven on the next call and never replays (`cmd/october-bus/mcp_reconnect.go`); `TestManagedMCPBridgeRecoversAfterRefusalWithoutReplay`, `TestManagedMCPBridgeReconnectsAfterAuthorityRestart`, `TestManagedMCPBridgeRenewsExpiredCredentialWithoutRestart`, `TestManagedMCPCloudHTTPSConnection`. The helper has no background reconnect loop, so it has nothing to back off. Backoff before starting a new call belongs to Desktop or the hosted service, and the helper never automatically retries an ambiguous mutation. Released managed mode was not executed. | source test; mocked fault injection; source-equivalence inference (rc.5) |
| Explicit retirement and replaced execution | proved | `POST /v1/me/retire` and the `session-retirement` health feature; `TestManagedMCPConnectionRefreshAndRetirement`, `TestAuditSessionCloseMustRetireAuthorityAndClaims`, `TestSessionContextCancellationRetiresAuthority`; rc.5 check `execution-replacement` | source test; released artifact |
| Disconnected is not asleep | proved: reports disconnected or unknown | `reachable` means the lease is current and the lifecycle is not offline (the `Reachable` computation in `scanAgent`). Neither the protocol nor the helper failure classes (the `connectionFailure` constants in `cmd/october-bus/mcp_connection.go`) have a sleep state. `TestOfflineHeartbeatCannotClaimReadiness`; rc.5 checks `clean-offline-lifecycle`, `clean-and-expired-lifecycle` (a stopped heartbeat leads to lease expiry, unreachable status and a recovered claim). Telling sleep from a crash is Desktop/host-owned. | source test; released artifact |
| Uncertain injection | portable side proved; controller-side uncertainty: adapter-only (execution controller) | **New** `TestHookDoesNotAcknowledgeWhenStdoutRejectsInjection`: when stdout takes a partial write and fails, the hook neither posts `/hook/inbox-ack` nor `/hook/context-ack` (Claude and Codex flavors). `TestHookClaudePrePromptPullsAndAcknowledges` covers the success path. Stdout accepting the bytes proves native handoff, not model consumption. The tmux foreground proof is Desktop-owned. Contract v1 §3.5 and §6 give the `staged` and `uncertain` stages to the authoritative delivery record for the exact execution generation, which is the Desktop or hosted controller serving the hook routes, not the portable daemon; it must record them, keep them observable and never reinject blindly. | source test; fixture inspection |
| Hosted versioned fixture comparison | compared for digest `8a0901a2…1cc62365`; no missing portable capability demonstrated so far; **one contract requirement unresolved**, so the no-gap conclusion is not complete | [Hosted contract v1 comparison](#hosted-contract-v1-comparison): 25 cases, one row each | fixture inspection; source test; mocked fault injection |
| Tenancy, accounts, cloud inbox, compute, authorization, provider selection, laptop-off SSH | hosted/Desktop; excluded | #142 item 6 | — |

No row is classified as missing portable.

## Hosted contract v1 comparison

Input: October Cloud contract v1, fixture digest `8a0901a28dccaf30219c7cbd37979712fc0d2039afa5546493be004a1cc62365` (see [Artifacts](#artifacts)). The fixture names `october-bus#142` as a consumer, and contract §9 asks for this review before dependent PRs land. Every v1 operation is a hosted HTTP route that Desktop or the hosted service calls; none is a Bus operation. So each case asks one question: can the required semantics be built on the existing portable Bus, and by whom?

Classifications: **adapter-only** (Desktop or the hosted service must do the named work, over the named Bus primitive where one applies), **hosted-owned / excluded** (#142 item 6: tenancy, authorization, compute, billing, provider, SSH), **supported portable** (the Bus itself provides the case's semantics; no v1 case is a Bus operation, so no row uses it) and **missing portable** (a required portable field or operation is absent). The "Contract requirement" column is fixture inspection only. The "Executed Bus evidence" column lists the source tests that ran for the narrower Bus primitive; none executes a hosted route. 17 cases are `contractOnly`: their handlers land in Saturday #73–#77, which must replay these cases as their own qualification gates (contract §8).

| # | Fixture case | `contractOnly` | Classification | Contract requirement and obligation (fixture inspection) | Executed Bus evidence | Evidence kind |
| --- | --- | --- | --- | --- | --- | --- |
| 1 | `enqueue-success` | yes | adapter-only: the originating ledger's adapter (Desktop for offline-originated work, hosted for hosted-originated work), over durable send | The client-chosen hosted `messageId` maps to a Bus `idempotencyKey` derived from the originating actor, message ID and recipient, kept within the Bus's 256-byte key limit (a fixed-length digest works). The Bus `messageId` is server-assigned and is stored as a separate mapped ID. `recipientId` maps to `to`. A Bus send carries no destination `generation`, so the adapter checks the recipient registry generation before sending and records that binding. `actorId`, `conversationId`, `correlationId`, `streamPosition` and `kind` have no Bus field and travel as adapter data (for example a `text` context item with `mediaType: application/json`, which the Bus hash covers); they are never Bus authority, and the actor is never collapsed into the forwarding agent. Text larger than the destination inbox accepts is refused explicitly, never truncated ([Observations](#observations)). | `TestDurableRequestRedeliveryAcknowledgementAndReply`, `TestInspectReceiptEndToEnd`; rc.5 `durable-request-and-idempotency` | fixture inspection; source test; released artifact; code inference (context in `messageRequestHash`, `validateContext`) |
| 2 | `enqueue-lost-response-then-identical-replay-returns-original-receipt` | yes | adapter-only over supported Bus replay | On replay the adapter derives the same per-recipient key and resends identical Bus input, so the Bus returns the original `messageId` and `acceptedAt`. The hosted receipt (hash, stream position, creation time, three roles) comes from the adapter's own durable record; the Bus does not store it. Nothing replays automatically. | `TestMessageIdempotencyRejectsPayloadChanges`, `TestColdRestartPreservesIdempotencyAndRedelivery`, `TestMCPStdioManagedConnectionDoesNotReplayLostMutation` | fixture inspection; source test; mocked fault injection |
| 3 | `enqueue-same-id-changed-payload-refused-without-mutation` | yes | adapter-only over supported Bus conflict detection; `streamPosition` conflict rule **unresolved** | The adapter compares the hosted hash under the actor-scoped key (§3.2–§3.4) and refuses with `request_id_conflict` (409) without writing. `messageReceiptPreimage` covers workspace, recipient, generation, actor, message ID, conversation, correlation, `responseTo`, `fanoutId`, kind and text. §3.4 also requires `streamPosition` to be hashed, but the v1 envelope has no such field and the preimage omits it, so how an origin's position is supplied and protected on replay is **unresolved** ([Unresolved requirement](#unresolved-requirement)). The Bus hash covers only `to`, body, mode, `responseTo`, expiry and context, so a Bus `CONFLICT` is a second line of defense mapped to the same code, not proof of the hosted rule. | `TestMessageIdempotencyRejectsPayloadChanges`; `TestColdRestartPreservesIdempotencyAndRedelivery` (after restart); **new** `TestFanOutSendsAreIndependentAndRecipientScopedForDeduplication` (same key, other recipient) | fixture inspection; source test |
| 4 | `enqueue-fan-out-partial-success-one-committed-one-refused` | yes | adapter-only (originating ledger's adapter) over independent per-recipient sends | One Bus send per recipient, each in its own transaction under its own derived key (§7 rules out savepoints). The adapter validates each recipient generation and returns `stale_capability` per refused recipient; it records `fanoutId` and every per-recipient outcome; and it reconciles a lost aggregate response from those records (a #73 gate, §8). The Bus keeps no refusal receipt. | **New** `TestFanOutSendsAreIndependentAndRecipientScopedForDeduplication`: portable primitive only; its refusal is `PERMISSION_DENIED` for an unlinked, absent peer, not a stale generation | fixture inspection; source test |
| 5 | `reconnect-from-recipient-delivery-cursor-with-unacknowledged-work` | yes | adapter-only: hosted owns the cursor; Desktop as destination | The hosted recipient cursor is opaque, monotonic and bound to workspace, recipient and generation; it replays unacknowledged items in a deterministic order, rejects foreign cursors without disclosure, and is retained by the hosted service (§3.6, #73). Desktop re-fetches and transfers idempotently under the same derived key. A Bus scope-event cursor is not a recipient delivery cursor. | `TestColdRestartPreservesIdempotencyAndRedelivery` (Bus redelivery of unacknowledged work only) | fixture inspection; source test |
| 6 | `acknowledge-transferred-messages-idempotently` | yes | adapter-only: hosted owns transfer acknowledgement; Desktop as destination | `ack_messages` advances the hosted cursor (`cursor_advanced`) once the destination inbox commit is durable (`transferred`). It is not a Bus delivery acknowledgement: it must never mark the local Bus message acknowledged, and a Bus delivery acknowledgement must not be sent early to suppress local work. Counts do not translate; a zero count is ambiguous on both sides, so reconcile through receipts. | **New** `TestMCPStdioManagedConnectionDoesNotReplayLostAcknowledgement` and `TestColdRestartPreservesIdempotencyAndRedelivery`, for the separate Bus delivery acknowledgement only | fixture inspection; source test; mocked fault injection |
| 7 | `revoked-and-replaced-execution-generation-rejects-stale-work` | yes | adapter-only over supported Bus execution fencing | Desktop and the hosted service validate the recipient generation and return `stale_capability` (403) only for a confirmed revocation or replacement. A Bus authentication failure means the credential is not current; it can also follow expiry or retirement, so it is not translated to replacement by itself. | `TestEveryProtectedAgentMutationFencesReplacedAndExpiredExecution`, `TestExecutionReplacementRetiresPreviousToken`, `TestManagedMCPConnectionRefreshAndRetirement`; rc.5 `execution-replacement` | fixture inspection; source test; released artifact |
| 8 | `non-owner-direct-environment-api-denied-owner-scoped-refusal` | no | hosted-owned / excluded (authorization) | Central owner-only action policy (§4). No Bus counterpart. | — | fixture inspection |
| 9 | `non-owner-hosted-workspace-action-denied` | no | hosted-owned / excluded (authorization) | Same policy. | — | fixture inspection |
| 10 | `create-independent-cloud-task-attempt` | yes | adapter-only (Desktop and hosted) | A durable `attemptId` per independent start, reused for retries of that start, with immutable instruction content and revision and a cloud or local `location` (§3.3, §3.8, §6); details in [Task attempts and results](#task-attempts-and-results). Not implemented or qualified here. | `TestTaskClaimsRespectDependenciesAndOwnership`, `TestTaskReleaseAndExecutionReplacementRecoverClaims` (local arbitration unchanged; no attempt identity claimed) | fixture inspection; source test |
| 11 | `two-independent-attempts-one-cloud-one-local-merged-as-a-union-replayed-without-a-third` | yes | adapter-only (Desktop and hosted) | Idempotent union by attempt and event ID with distinct per-attempt results; a replay never creates a third attempt, and one completion never overwrites another (§3.8; a #73 gate, §8). | same as row 10 | fixture inspection; source test |
| 12 | `attach-metadata-bound-to-owner-environment-generation-and-expiry` | yes | hosted-owned / excluded (SSH grant; laptop-off SSH is v2) | Attach metadata bound to owner, environment, generation and expiry (§3.9). | — | fixture inspection |
| 13 | `access-path-provider-denied-frozen-current-generic-response` | no | hosted-owned / excluded (provider) | Frozen generic provider-denial response (§5). | — | fixture inspection |
| 14 | `recovery-accepted-concurrently-with-deletion-one-transactional-winner` | yes | hosted-owned / excluded (compute) | Single transactional winner (§3.9, §8). | — | fixture inspection |
| 15 | `recovery-second-claim-refused-no-second-entitlement` | yes | hosted-owned / excluded (compute, billing) | One funded recovery per environment (§3.9). | — | fixture inspection |
| 16 | `cutover-retained-snapshot-message-becomes-one-recipient-receipt-after-fence` | yes | hosted-owned / excluded (hosted ledger migration) | The hosted snapshot writer is fenced and retained facts transfer exactly once (§7). The portable Bus is not that snapshot authority. | — | fixture inspection |
| 17 | `quote-launch-server-derives-action-from-quote-kind` | yes | hosted-owned / excluded (billing) | Server-derived quote action (§3.9). | — | fixture inspection |
| 18 | `quote-recovery-server-derives-recover-action` | yes | hosted-owned / excluded (billing) | Same. | — | fixture inspection |
| 19 | `create-environment-launch` | no | hosted-owned / excluded (compute) | Environment launch. | — | fixture inspection |
| 20 | `register-hosted-workspace` | yes | hosted-owned / excluded (tenancy) | Hosted workspace registration. | — | fixture inspection |
| 21 | `register-recipient-binding-distinct-op-from-workspace-register` | yes | hosted-owned / excluded (recipient registry, §6) | The registry binds a stable recipient to its current execution generation. The Bus analogue, where each registration issues a new `executionId` and fences the previous one, is already proved. | `TestExecutionReplacementRetiresPreviousToken` (Bus analogue only) | fixture inspection; source test |
| 22 | `resolve-hosted-question-owner-only` | no | hosted-owned / excluded (authorization) | Owner-only question resolution (§4). | — | fixture inspection |
| 23 | `delete-requires-confirmation-frozen-refusal` | no | hosted-owned / excluded (compute) | Frozen refusal. | — | fixture inspection |
| 24 | `stop-pre-acceptance-operation-rejection-is-operation-rejected-not-request-id-conflict` | no | hosted-owned / excluded (compute) | `operation_rejected`, distinct from `request_id_conflict` (§5). | — | fixture inspection |
| 25 | `lifecycle-status-current-flat-body-is-a-subset-of-the-versioned-lifecycle-shape` | no | hosted-owned / excluded (compute) | Lifecycle shape compatibility. | — | fixture inspection |

**Missing portable:** none demonstrated so far for this digest. No case shows a need for a new protocol field or operation, helper behavior, or daemon change, and no release follows. The conclusion is **not complete**, because one requirement is unresolved (below). Until it is resolved, B02 and B03 are not filed, and whether they are needed stays pending. The other open conditions sit outside the Bus: the adapters above are not built or qualified, Saturday PR #87 is unmerged, live evidence is none, the Desktop decision on reclaim-after-release attempts is still Desktop's to make (it does not change the Bus either way), and aligning hosted text size with a destination inbox limit is a Desktop and hosted decision ([Observations](#observations)). If the digest changes, the affected rows are rechecked before this conclusion is reused.

### Unresolved requirement

Contract §3.4 gives each message an origin-stable `streamPosition` and says it is part of the message hash preimage, so changing it under a reused message ID is a `request_id_conflict`. At commit `4effd856`, though, the strict `hostedMessageEnvelopeSchema` has no `streamPosition` input, and `messageReceiptPreimage` omits it. The field appears only on the receipt (`recipientReceiptSchema`), and the receipt alone does not say how the originating value is supplied or protected on replay. This comparison therefore cannot show how the stated origin-order and conflict semantics survive the v1 envelope, so the requirement stays open. It affects rows 1–5 and the `streamPosition` line of [Hosted contract input](#hosted-contract-input).

Two things can resolve it: an authoritative clarification from the hosted contract owner, with a concrete, consistent mapping for how an origin supplies its position and how replay protects it; or a corrected contract. If the correction changes the fixture digest, the affected rows are rechecked. Nothing here shows that the Go Bus needs a change, and this report does not presume the outcome. No Bus implementation or release is proposed.

### Receipt stages

All seven `receiptStageSchema` values, against contract §3.5 (stages) and §6 (authority registry):

| Stage | Owner (§6) | Bus equivalent and limit |
| --- | --- | --- |
| `accepted` | The originating ledger: hosted transaction, or Desktop local storage for offline-originated work | None. A Bus send receipt is local commit evidence for a message the Bus itself accepted; it is not the hosted acceptance record. |
| `transferred` | Post-cutover hosted recipient receipt and inbox rows (#73); the destination adapter proves a durable inbox commit before reporting it | When the destination inbox is a Bus inbox, its send commit can be that evidence. The hosted transfer acknowledgement and cursor advance (row 6) are a separate hosted record, never a Bus delivery acknowledgement. |
| `reserved` | The authoritative delivery record for the exact execution generation | Bus `reserved` (`ReserveInbox`), which expires back to queued. |
| `staged` | Same; the execution controller | No Bus state. The controller records it before handing work to the harness. The portable `hook` pulls from that controller with `requestId` and `providerTurnId`. |
| `submitted` | Same | Closest primitive: Bus `delivered` (`CommitInbox`). A reservation commit is a handoff only; it proves neither a terminal write nor model processing. |
| `acknowledged` | Same | Bus delivery `acknowledged`, only at the matching delivery boundary (**new** `TestMCPStdioManagedConnectionDoesNotReplayLostAcknowledgement`). The hook acknowledges only after a successful stdout handoff, which proves native handoff, not model consumption. |
| `uncertain` | Same | No Bus state. The controller marks `staged` or `submitted` work without a provable terminal write as `uncertain`, keeps it observable, and never reinjects it blindly. The portable side, no acknowledgement after a failed handoff, is `TestHookDoesNotAcknowledgeWhenStdoutRejectsInjection`. |

Neither a Bus reservation commit nor hook stdout acceptance proves that a model processed work, and the contract promises no exactly-once model behavior (§3.5).

### Transport note

No v1 case targets the worker MCP, wake, heartbeat or hook routes the helper uses. Those are `LIVE_TRANSPORT_ROUTES`, which v1 does not freeze. The helper's transport-only proof (`TestMCPStdioManagedConnectionForwardsAuthorityDefinedFields`) stands as it was, and this comparison is rerun if a later contract version freezes worker tool schemas.

## Observations

These are recorded with code evidence only. None is a demonstrated gap; revisit one only if a required hosted fixture fails because of it. Contract shapes belong in B02, designed against that failing fixture.

- `add_task` has no idempotency key (`Store.AddTask`). A caller retry after a lost response creates a second task. Task creation accepts either scope or agent authority, so no identity policy for such a key is defined yet.
- `Task` exposes `claimedBy` (the agent, `Task.ClaimedBy`), not the claiming execution.
- Retrying `claim_task` or `complete_task` after a lost response returns `CONFLICT` (the `CodeConflict` returns in `Store.ClaimTask` and `Store.CompleteTask`). Task ownership errors are an **Open** item in `spec/0.1/state-machines.md` ([#28](https://github.com/october-dev/october-bus/issues/28)); the new tests do not pin them.
- `bus.AgentSession` ends on its first heartbeat failure (`AgentSession.heartbeat`). That session helper is not on the managed-connection path.
- The bridge fixes its tool list at startup. Tool changes upstream are not propagated to an already running bridge.
- Contract v1 allows message `text` up to 262 144 characters (`hostedMessageEnvelopeSchema`), while protocol 0.1 limits `body` to 65 536 bytes (`spec/0.1/schemas/protocol.schema.json`; `validateText` in `Runtime.SendMessage`). No fixture case comes near either limit, and no v1 operation sends hosted text through a protocol 0.1 body: Desktop's local Bus authority is its own store, and the managed helper forwards whatever the upstream authority's tools define. A destination adapter that writes hosted messages into an inbox with a smaller limit, including a protocol 0.1 authority, must refuse oversized text explicitly and never truncate it. Aligning the two limits is a Desktop and hosted decision. Raising the protocol 0.1 limit becomes a B02 candidate only if a required path must carry larger text through a protocol 0.1 body.

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
| Hosted contract v1 fixture, SHA-256 `8a0901a28dccaf30219c7cbd37979712fc0d2039afa5546493be004a1cc62365` (decided 2026-10-01) | **Accepted as comparison input only** | The committed bytes at Saturday `4effd856` and Desktop's generated copy at `de9085b8` hash to this digest; 25 cases, 17 `contractOnly`. Saturday PR #87 is unmerged and no hosted handler was executed, so this qualifies no hosted behavior and upgrades none of the rows above; the rc.5 managed-mode and hook row stays accepted by inference only. |

No product or launch flag follows from these decisions.

## Hosted contract input

The fixture request prepared for #142 was never posted and is obsolete: the hosted contract exists and names #142 as a consumer ([Artifacts](#artifacts)). Its concepts map to Bus fields without changing authority semantics:

| Contract v1 concept | Bus field or owner |
| --- | --- |
| recipient (`recipientId`) | `to`/`toKind` |
| recipient `generation` | no Bus field; the adapter validates it against the hosted recipient registry before sending |
| logical send / retry (client-chosen `messageId`, scoped by workspace, recipient and actor) | `idempotencyKey`, derived per actor, message and recipient |
| accepted ID | Bus `messageId` (server-assigned), kept in a durable mapping beside the hosted `messageId` |
| fan-out (`fanoutId`) | no Bus field; one Bus send per recipient, outcomes recorded by the adapter |
| correlation (`conversationId`, `correlationId`) and order (`streamPosition`) | no Bus field; adapter data, for example a JSON `text` context item. How an origin supplies `streamPosition` and how replay protects it is unresolved ([Unresolved requirement](#unresolved-requirement)). |
| reply (`responseTo`) | Bus `responseTo`/`responseMessageId` only for a real Bus request and response; otherwise adapter data |
| actor (`actorId`), resource and billing owners | credential-derived `from`/`fromKind` names the forwarding agent (never payload-supplied); the hosted roles are adapter data and hosted authority |
| receipt `hash` | adapter-owned; the Bus `messageRequestHash` is narrower and is not the hosted hash |
| receipt `stage` | see [Receipt stages](#receipt-stages) |
| recipient delivery cursor and transfer acknowledgement | hosted-owned; not the Bus scope-event cursor and not Bus delivery acknowledgement |
| execution | `executionId`; the hosted generation is validated by the adapter |
| attempt / result (`attemptId`, `instructionRevision`, `location`, events, `result`) | adapter-owned by Desktop and the hosted service ([Task attempts and results](#task-attempts-and-results)); Bus `(taskId, executionId)` is attribution only |

No required fixture failed, so B02 stays unpopulated. The unresolved `streamPosition` requirement is not a failed case and is not yet a portable gap; it keeps the no-gap conclusion pending ([Unresolved requirement](#unresolved-requirement)). If a later digest produces a failing case, B02 starts from that exact case, its minimal reproduction and a backward-compatible contract proposal.

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

### Follow-up (2026-10-01)

Source: `f4174eb` (#144's head) plus the two new tests below, on a separate branch. Toolchain: go1.27.0 linux/amd64 from the Go module cache, as above. Linux 7.1.8 x86_64 (Fedora 44). No production code, protocol, SDK, schema, migration, dependency or release tooling changed. The released-artifact, prior cross-repo and live evidence above was not rerun, and the TypeScript SDK and conformance profiles were not run again (no SDK or runtime change; Desktop uses neither the npm package nor the SDK).

Hosted contract identity, checked against the committed bytes and the Desktop copy (paths relative to the Saturday and Desktop checkouts):

```sh
git -C saturday show 4effd8565feb2b1e3954ae8e700af5a774a85924:docs/fixtures/october-cloud-v1.json | wc -c        # 23669
git -C saturday show 4effd8565feb2b1e3954ae8e700af5a774a85924:docs/fixtures/october-cloud-v1.json | sha256sum    # 8a0901a2…1cc62365
sha256sum october-desktop/docs/generated/october-cloud-v1.json   # 8a0901a2…1cc62365 (Desktop de9085b8, untracked)
```

The fixture has `contractVersion` 1, 25 cases (17 `contractOnly`) and 15 operation keys. A script read the 25 case names from the fixture and checked that each appears exactly once as a row of the [comparison table](#hosted-contract-v1-comparison), and that the table has no other rows: 25 of 25 found once, no extra rows.

```sh
go test ./... -count=1              # 10 packages ok
go test -race -count=1 ./...        # 10 packages ok
go vet ./...                        # ok
go build ./...                      # ok
gofmt -l .                          # no output
go test ./bus -run TestFanOutSendsAreIndependentAndRecipientScopedForDeduplication -race -count=1 -v              # PASS
go test ./cmd/october-bus -run TestMCPStdioManagedConnectionDoesNotReplayLostAcknowledgement -race -count=1 -v    # PASS
```

New tests:

- `TestFanOutSendsAreIndependentAndRecipientScopedForDeduplication` (`bus/runtime_test.go`)
- `TestMCPStdioManagedConnectionDoesNotReplayLostAcknowledgement` (`cmd/october-bus/mcp_connection_test.go`)

As a check that the fan-out test detects a regression, `to` was removed temporarily from the value hashed by `messageRequestHash` in `bus/store.go`. `TestFanOutSendsAreIndependentAndRecipientScopedForDeduplication` failed with "expected CONFLICT" when the accepted key was reused for another recipient; `TestMessageIdempotencyRejectsPayloadChanges` still passed, so only the new test catches this. With the change reverted, `git diff` showed no production change. The full commands above ran on the reverted tree. The acknowledgement test needs no mutation check: it injects the fault into existing behavior, and the bridge has no acknowledgement replay path to remove.

A same-day audit found that the conclusion overstated completeness: the §3.4 `streamPosition` inconsistency was acknowledged but not resolved. The status and conclusion now keep it as an [unresolved requirement](#unresolved-requirement), and B02 and B03 stay pending instead of being declared unnecessary. The audit also corrected the abbreviated digest to `8a0901a2…1cc62365`. Tests were not affected.

The public diff was reviewed for copied private material: no fixture payloads, example IDs or hashes, credentials, or provider, pricing or host examples. As a supplemental smoke check only, `grep -niE 'microusd|daytona|ssh-user-token|hostKeys|sandboxId'` over this report matches only this sentence, which quotes the pattern.
