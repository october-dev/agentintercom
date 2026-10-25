# Production audit remediation

Status date: 2026-09-06. Baseline audit: 2026-09-05, commit `e65eef2158aeed6c27493000f85de6b940675c2e`. This document records the subsequent working-tree implementation, not an already published release or certification.

Follow-up: PR #109 at `33659f7` passed all seven hosted checks, including native Linux/macOS/Windows installation tests. The subsequent ten-item preparation pass is recorded in [launch validation](launch-validation.md); its combined runtime tests, package rebuilds and actual-binary rehearsal are deliberately deferred. Earlier green checks must not be presented as results for those new changes.

The concrete code defects in F01–F10 have fixes and regression coverage. F11 has a repository-side release check, and the approved one-review and code-owner requirements are now enabled on live `main`. The existing owner bypass remains in place. **A stable launch is not yet certified.** Native candidate CI, released-binary upgrade rehearsal, current harness evidence, and the remaining release-policy decisions below remain gates.

## Findings and changes

| Finding | Implemented change | Principal regression evidence |
| --- | --- | --- |
| F01: privileged credentials inherited by harness | Strip admin, standard scope, and configured scope-token variables before launching the child. | [Credential isolation](../cmd/agentintercom/credential_isolation_test.go); existing managed-launch tests. |
| F02: stale in-flight authority | Revalidate execution/lease and scoped authority inside protected transactions. A2A checks current secret, enabled state, grant, and publication, including retries. Replacement releases old reservations and claims. | [Authority interleavings](../bus/production_regression_test.go), [hardening tests](../bus/hardening_test.go): replacement/expiry across 13 protected operations, scope rotation during registration, A2A disable and rotation. |
| F03: rc.4 database rejected | Back up schema 2 with SQLite's snapshot operation, migrate tables atomically to schema 9, validate foreign keys, preserve existing state. Retain the backup on failure. Reject unsupported intermediate schemas. | [Released-DDL migration and crash-before-commit tests](../bus/migration_test.go), populated with credentials, a delivered outstanding request, dependent tasks/claims, and an escalation; migrated archive restores. |
| F04: live ownership stolen after readiness failure | Persistent OS-held locks on runtime discovery and the canonical database path, Unix and Windows implementations, hard-link rejection, conservative legacy PID guard. | [Slow-daemon reproduction](../bus/production_regression_test.go), [path aliases and distinct runtime directories](../bus/ownership_test.go), [process-death recovery](../bus/fault_recovery_test.go). |
| F05: A2A pruning violates foreign keys | Prune terminal A2A tasks and correlation history as a unit only after all correlated messages are eligible; delete in dependency-safe order. Unfinished work retains retry history. | [Completed conversation](../bus/production_regression_test.go); [unfinished/multi-turn, expired failed task, dry-run/execute and restore](../bus/retention_a2a_test.go). |
| F06: exports cannot restore | Preserve the transitive dependency closure of every retained task, normalize empty publisher arrays, validate exports against importer rules and the transfer-size limit. | [Post-prune dependency and empty-publisher restore regressions](../bus/production_regression_test.go), existing archive suite. |
| F07: close leaves usable execution authority | New idempotent retirement operation revokes the execution and releases claims/reservations. Go and TypeScript managed sessions retire on close/cancellation/failure. Temporary offline presence remains distinct. | [Go shutdown/cancellation](../bus/session_shutdown_test.go), [retired authority/claims](../bus/production_regression_test.go), strengthened MCP adapter conformance and TypeScript integration. |
| F08: SDK startup/close races | Serialize lifecycle writes; clean up failed starts; share one close operation; cancel in-flight heartbeats; reject state changes after close. Intentional close cancellation is not reported as a background heartbeat failure. | [Five TypeScript lifecycle regressions](../sdk/typescript/test/sessions.mjs), Go lifecycle tests, integration. |
| F09: no supported scope recovery | Admin-only scope listing, token rotation, and explicitly confirmed deletion, with Go/TypeScript clients and CLI commands. Rotation retires executions and disables scoped credentials atomically. | [Admin HTTP permissions and rotation tests](../bus/hardening_test.go), TypeScript scope recovery integration. |
| F10: incomplete operational limits/recovery | Bounded HTTP and inbox-wait concurrency; aggregate remote backlog partition; task pagination; active-state covering indexes; bounded portable exports; streamed full-database backups. | [Budgets, pagination, and >64 MiB backup/restore](../bus/hardening_test.go), [storage-full/process crash](../bus/fault_recovery_test.go), [query plans](../bus/query_plans_test.go), [mixed-load benchmark](../bus/load_test.go). |
| F11: review/release policy gap | Release workflow checks that the tag targets a merged main PR independently approved at its final head. Direct commits and stale/dismissed approvals fail this check. After owner approval, live `main` was updated to require one approval and code-owner review. | [Release-policy tests](../scripts/release-policy.test.mjs); GitHub API read-back confirmed only the two approved settings changed. Existing bypass allowances, tag policy, and signing were not changed. |

The operator procedures are in [operations](operations.md), lifecycle compatibility in [clients](clients.md), and the corrected contracts in the [HTTP profile](../spec/0.1/http.md) and [archive profile](../spec/0.1/archives.md). These are additive endpoints, but the new session helpers require a runtime with `/v1/me/retire`; do not pair them with rc.4. No schema-version bump beyond the existing schema 9 is required for the new operational indexes.

## Verification performed locally

Host: Apple M1 Pro, macOS arm64; Go 1.27.0; Node 26.4.0.

- Full `go test -race -count=1 ./...`, including conformance, CLI, schema and example tests.
- `go vet ./...` and runtime builds for Darwin, Linux and Windows, each on amd64 and arm64. Cross-compilation is not native runtime verification.
- TypeScript typecheck, build, transport/error tests, lifecycle tests, and integration against the newly built local daemon, including streamed backup.
- Release-policy unit tests and `git diff --check`.
- Consistent HTTP backup larger than the portable 64 MiB envelope, reopened successfully with `PRAGMA integrity_check`, record counts and credentials preserved; oversized portable export rejected.
- SQLite storage-full simulation using `max_page_count`: failed write rolls back, and an idempotent retry succeeds after capacity is restored. This is not a physical host-disk exhaustion test.
- Abrupt subprocess exit after an accepted write: committed WAL data survives and ownership can be reacquired. Interrupted migration before commit leaves schema 2 readable and retryable.

The migration fixture uses the exact DDL in the rc.4 Git tree and populated test state. It is stronger than the audit's original version-marker-only probe, but it is **not** a fixture captured from running the distributed rc.4 executable. Likewise, a clean race detector result is not evidence of authorization ordering by itself; deterministic interleaving tests establish those assertions.

### Local workload measurement

Reproduce the fixed workload with:

```sh
go test ./bus -run '^$' -bench '^BenchmarkLocalMixedHTTP$' -benchtime=20000x -count=1 -timeout=7m
```

The measured run used 32 registered agents in 16 HTTP worker pairs, performing 20,000 cycles of heartbeat, message send/reserve/commit/acknowledge, and task add/claim/complete. A concurrent maintenance worker took a SQLite snapshot and pruned old terminal work. The test checked that retained plus pruned messages and tasks each equaled 20,000 and that the snapshot passed integrity checking.

Result: approximately **250 seconds** elapsed, **77.81 ms heartbeat p95**, and 12.48 ms amortized wall time per completed mixed cycle. Other verification was running concurrently; this is not a controlled performance comparison. A separate 5,000-cycle run completed in approximately 21.7 seconds with 19.39 ms heartbeat p95. An earlier 20,000-cycle repeat exceeded the benchmark's five-minute context before the active-state covering-index corrections; the measurements above are after those corrections. The final retention dependency walk was subsequently made linear and verified by the regression suite; the long benchmark was not rerun for that change.

These are finite local correctness/load samples, not a supported maximum agent count, a latency SLO, multi-hour soak evidence, or hosted capacity certification. The benchmark's five-minute deadline intentionally makes an overloaded run fail. Determine the release's supported retained-data size and latency target on representative customer hardware; repeat with larger payloads/history and late-run maintenance. SQLite still serializes database operations, and snapshots/pruning can delay heartbeats.

### Enforced bounds

| Surface | Bound / behavior |
| --- | --- |
| HTTP work | 256 general concurrent requests, with a separate 32-slot control pool for health, heartbeat, retirement and shutdown. |
| Inbox long polls | 32 per agent and 128 per scope, held for the complete wait rather than reset on notifications. |
| Remote active message backlog | At most 5,000 requests and 64 MiB of request bodies per scope, in addition to per-principal limits; local messages retain capacity within the shared 10,000-message backlog. |
| Task history | Legacy list refuses more than 10,000 records; `/v1/tasks/page` returns up to 500 per page. Pagination is not a point-in-time snapshot. |
| Portable JSON | Conservative preflight budget plus actual 64 MiB encoded-size validation. A valid small archive can be rejected conservatively; full SQLite snapshots are the large-state path. |
| Retained history | No automatic deletion. Operators choose retention cutoffs longer than supported retry windows and monitor storage growth. |

Request limits do not reserve database execution time. In particular, this patch does not claim isolation from slow disks or unlimited retained history, and does not turn the local reference daemon into a hosted multi-tenant service.

## Remaining launch gates and authority decisions

1. **Independent review and candidate CI.** Review this security/protocol/storage change as a PR; run native Linux/macOS/Windows CI, the existing vulnerability jobs, SDK and conformance on its final commit. Local cross-builds do not close the native-platform gate, particularly for file locking and process ownership.
2. **Upgrade/rollback rehearsal.** On copies of representative installations, produce state using the actual distributed rc.4 binary, stop it, run the candidate migration, continue outstanding work, restore the automatic backup into a fresh private directory with rc.4, and verify the old executable refuses upgraded schema 9. Do not run old and new daemons concurrently against the same database; legacy binaries did not honor the new database lock.
3. **Current harness evidence.** Run the [compatibility runbook](../compatibility/RUNBOOK.md) for the final candidate with each advertised released harness. Existing public Codex evidence targets rc.4; it was not relabeled as current evidence. Experimental adapters and the partial A2A bridge remain experimental.
4. **Remaining review and tag policy.** Following explicit owner approval, live `main` now requires one approving review and code-owner review. API read-back confirmed all other protection settings were preserved: six strict CI checks, stale-review dismissal, admin enforcement, linear history, conversation resolution, and force-push/deletion restrictions. The pre-existing `harshsaver` PR-review bypass remains, so review is not mandatory for every actor. `CODEOWNERS` still names that single owner; a second eligible code owner is needed for independently reviewed owner-authored changes without relying on bypass. Removing bypass privileges or protecting release tags needs a separate repository-owner decision. A workflow check cannot prevent someone authorized to replace that workflow from removing it.
5. **Release trust and supported profile.** Supply signing/notarization identities or explicitly retain the documented unsigned prerelease boundary. Adopt a measured local workload/SLO and retention policy before stable commitments. No release, tag, signing identity, scope credential rotation, or deletion of real user data was performed by this remediation.

## Open-issue alignment

The baseline audit's dated inventory contained 38 open issues; no GitHub issue was closed or edited in this pass. This patch is not a claim that every roadmap item has been implemented.

- [#27](https://github.com/october-dev/october-bus/issues/27): transaction authority, retirement and lifecycle fixes are implemented here; the broader host execution-authority reporting design still needs its own review.
- [#28](https://github.com/october-dev/october-bus/issues/28): retirement, recovery, retention, errors/limits and corresponding schemas/tests were added to the protocol profile. Review the complete independent profile before declaring it stable.
- [#33](https://github.com/october-dev/october-bus/issues/33), [#34](https://github.com/october-dev/october-bus/issues/34), [#37](https://github.com/october-dev/october-bus/issues/37): Cursor, OpenCode and Claude Code remain gates only if those adapters are promised at launch. This pass did not fabricate tested-version evidence.
- [#11](https://github.com/october-dev/october-bus/issues/11), [#18](https://github.com/october-dev/october-bus/issues/18): PostgreSQL and Supabase/shared deployment remain separate backend/deployment work, not prerequisites for a narrowly scoped local release.
- [#24](https://github.com/october-dev/october-bus/issues/24), [#26](https://github.com/october-dev/october-bus/issues/26): context extensions and active delivery remain design work outside the pull-only local launch scope.
- Adapter feasibility candidates and [#62](https://github.com/october-dev/october-bus/issues/62) remain roadmap work. The original full issue inventory is preserved with the 2026-09-05 audit artifacts under the local ignored `_audit/production-2026-09-05/` directory; that snapshot is historical, not a fresh live issue count.
