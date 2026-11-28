# Launch preparation and deferred validation

## Harness launch implementation (2026-09-10)

The subsequent harness-launch pass implements configuration-only bootstrap, private local scope credentials and mutation locking, explicit peer linking, 15 MCP tools including receipts, structured-argument coercion, cancellation recovery, generated host configurations and doctor probes. It includes 26 MCP host candidates, a native Pi extension and a cached-credential launcher for October Harness's existing native integration, all experimental. Stable candidate publication and original-artifact promotion are separate lanes; launch-core evidence and independent release review remain gates. See [adapter setup](../adapters/README.md), [integration boundaries](harness-boundaries.md) and [releases](releases.md).

The combined validation command below now includes native-extension hook tests and real Go-bridge/SDK integration. Named hosts, cloud accounts, all six architecture executions, eight-hour retained-data soak and user-pilot evidence must still be supplied. No support status or external publication is inferred from implementation. The following section is the historical ten-item preparation record, not a report of the current checkout's test results.

## Earlier ten-item preparation record

This pass implements ten bounded improvements without installing harnesses, publishing packages, changing repository permissions, or running a full runtime test suite. Regression tests and rehearsal tooling are prepared, **not evidence that the candidate passed them**. The original PR #109 revision `33659f7` passed CI; the follow-up needs a new combined run.

Static checks performed in this preparation pass: Go vet and formatting, TypeScript `--noEmit` typecheck, JavaScript syntax, JSON/YAML parsing, patch whitespace, and a dry run of the combined validation plan. No Go/Node regression tests, binary builds, installation smoke tests, or migration rehearsal were executed for these changes.

## Implemented in this pass

| # | Improvement | Prepared verification |
| --- | --- | --- |
| 1 | Health feature declaration and pre-registration runtime compatibility checks in Go and TypeScript. | Reject missing/wrong protocol or retirement support before any registration or credential-bearing health request. |
| 2 | Only confirmed lifecycle state becomes the next background heartbeat state, with writes ordered in both SDKs. | Failed and overlapping state-write regressions; existing close/cancellation tests. |
| 3 | Native build stamps and fresh SDK compilation/staging reject stale versions, source, targets, or binaries. | Build/pack/install stages in the combined command and native CI. |
| 4 | Checked artifact records bind each of seven tarballs to source, package, executable path and integrity. | Wrong metadata, stale source, and modified-byte regressions before publication. |
| 5 | Two-phase publish preflight checks every immutable version before any registry write. | A conflict/outage on the final package must produce zero publish attempts. |
| 6 | Release approval validation excludes outsiders/post-merge-only approvals and unresolved trusted change requests. | Deterministic policy tests; live code-owner approval is still a separate requirement. |
| 7 | Offline evidence linter cross-checks active claims against manifests, exact versions and expiry. | Deliberately invalid evidence regressions; optional exact-runtime/public-artifact sign-off mode. |
| 8 | One combined validation command with dry run, fail-fast reporting and optional cross-build/upgrade stages; CI can be manually dispatched. | Runner plan/failure tests; no publication or toolchain-install stage. |
| 9 | Actual-binary rc.4 upgrade/rollback rehearsal with isolated synthetic state. | Preserved credentials, requests, idempotency, dependencies, claims, escalation, continued work, old-schema refusal, and backup restore. |
| 10 | Install docs distinguish prepared versus published versions; client matrix and release/recovery boundaries are explicit. | Clean-machine and public-registry checks remain manual launch sign-offs. |

## Run the checks together later

Prerequisites: the project's supported Go and Node versions, and the SDK development dependencies already installed. The command does not run `npm ci` or install harnesses/toolchains. Go may fetch the project's pinned dependencies if they are not cached. The npm installation smoke test installs only the built Bus packages into an isolated temporary consumer.

From the repository root, inspect the plan without running any checks:

```sh
npm --prefix sdk/typescript run validate:launch -- --dry-run --all-platforms
```

When ready to test the committed candidate:

```sh
npm --prefix sdk/typescript run validate:launch -- --all-platforms
```

This runs release/tooling regressions, compatibility metadata checks, Go race tests and vet, SDK checks, native builds, fresh package staging, SDK/runtime integration and actual-tarball installation. Omit `--all-platforms` for a host-only build. It stops on the first failure and explicitly reports unrun later stages. It does not certify actual named harnesses, sustained load, platform signing, or public npm availability. The CI workflow's manual trigger provides native Linux/macOS/Windows runs after these changes are pushed; it has not been triggered by this preparation pass.

Commit before building artifacts. Changing source or HEAD requires a rebuild/repack; do not reuse the old ignored `dist/npm` binaries from the previous revision.

## Released-binary upgrade rehearsal

Obtain the released executable users upgrade from (currently `0.2.0`, from the `@october-dev/agentintercom-*` platform package) for the host platform and verify its checksum independently. The script does not download or authenticate it, and an embedded version string alone does not prove artifact origin.

Set `OCTOBER_BUS_PREVIOUS_BINARY` to that executable's absolute path, then add `--upgrade` to the combined command. The candidate executable is selected from the just-built host artifacts. To run only the rehearsal, also set `OCTOBER_BUS_BINARY` to a versioned candidate executable and run:

```sh
node scripts/rehearse-upgrade.mjs
```

The script needs a Node.js version with `node:sqlite` (22.13 or later). It creates a fresh private temporary directory, removes inherited Bus settings from child environments, and uses only its own PID-checked loopback endpoints. It never points either binary at the user's real data or runs both against the same database simultaneously. It records the SQLite schema version before and after the candidate runs. When the schema is unchanged, the previous release must run a copy of the upgraded database and see the work finished after the upgrade. When the schema changed, the previous release must refuse a copy, and an automatic pre-upgrade backup, if the migration made one, must restore the original work in another fresh directory, without copying WAL/SHM files from the upgraded database.

Passing output includes both executable hashes, the schema versions and the exercised checks. Successful synthetic fixtures are removed; failures retain the fixture directory for investigation after child termination. Those files contain synthetic credentials: do not upload the directory or run-file contents. The rehearsal covers a finite representative workflow, not arbitrary customer data, interrupted migration, physical disk exhaustion, or a long-running soak. Existing fault/migration tests cover additional failure interleavings.

## Remaining human/external sign-offs

- Independent review of the final commit, with all required checks rerun after material changes; no owner bypass as a substitute for release approval.
- Native npm ownership/trusted publishers, provenance and reviewed-main publication; installation from the actual public registry afterward.
- Exact-candidate, released-harness runbooks with accessible sanitized logs and digest review. `node scripts/check-compatibility.mjs --runtime VERSION --require-attestation` checks recorded claims, not the contents or truth of those logs.
- A clean-machine journey: install, connect two agents, exchange and acknowledge work, restart, inspect `doctor --json` and message receipts, upgrade, and remove the installation without deleting valuable state.
- A measured retained-data/load profile and multi-hour soak; signed/notarized platform artifacts or an explicit unsigned beta boundary; a small real-user pilot before stable claims.

No compatibility record has been promoted or rewritten as passing by this work. Readiness still does not gate explicit pulls, and this pass does not import the unreviewed wake behavior from PRs #90/#91 or claim those PRs are resolved.
