# Maintainer-assisted verification

Contributors do not need to buy harness subscriptions or own every operating system to submit safe experimental work. Missing infrastructure can be tracked separately; known correctness failures and unsupported compatibility claims still need correction. This workflow implements the repository-side part of [#108](https://github.com/october-dev/october-bus/issues/108), not account provisioning or automatic certification.

## Request a run

Open a **Maintainer-assisted harness verification** issue using the repository template. Supply the PR and immutable commit, exact versions, target platform, public setup, expected permissions, missing [runbook](RUNBOOK.md) scenarios, and any known failures. State when authentication prevented a run or no sanitized log is available.

A maintainer must explicitly approve the exact revision before running it. Use a disposable environment and a maintainer-operated account or manually run the scenarios on a maintainer-controlled machine. Never ask contributors to send credentials. Do not expose account, repository-write, or release secrets to arbitrary fork code; a passing PR check is not approval to execute it with those privileges. Additional platform runs may remain pending while accurately scoped experimental documentation merges.

## Prepare a local bundle

The dependency-free tool uses Node.js 20 or newer. It does not install or execute a harness, obtain account access, contact the network, upload files, or edit the verified registry.

Keep inputs outside the repository in a private directory. Copy the fields from [attempt.example.json](attempt.example.json) into your private `attempt.json`, replacing **every example value** with the actual attempt metadata. `attemptedAt` is the attempt time in ISO UTC; `repositoryCommit` is the full reviewed SHA, not a branch. `outcome` is a contributor-reported `passed`, `failed`, `partial`, or `not-run`, never an independent verification claim. For `not-run`, record setup observations instead of inventing a transcript. Supply exact versions where known; explicitly record unknown runtime or harness versions and their limitations instead of guessing. Such attempts cannot qualify as formal passing evidence.

From the repository root, with those private paths substituted:

```sh
node scripts/verification-bundle.mjs \
  --metadata /private/path/attempt.json \
  --log /private/path/raw-run.log \
  --out /private/path/new-bundle \
  --redact-env CUSTOM_HARNESS_TOKEN

node scripts/verification-bundle.mjs verify /private/path/new-bundle
```

`--redact-env` is optional and repeatable. Pass **environment variable names, never secret values**; a named variable must exist and be nonempty. Default redaction covers Bus admin/scope/agent tokens and common GitHub, npm, OpenAI, and Anthropic token variables when present. It also handles common bearer/basic headers, JSON credential fields, shell/query credential assignments, URL user/password pairs, terminal controls, and the current home-directory prefix. Supply additional known credentials via environment variables, including any generated during the attempt. Never persist those values in the issue template or example configuration.

The output directory must not exist; its parent must already exist. The tool accepts regular UTF-8 files, up to 64 KiB of metadata and 16 MiB of log text. It refuses symbolic-link inputs and never overwrites inputs or an existing bundle. Output contains only `run.log`, `bundle.json`, and `REVIEW.md`. Files are created with mode `0600` in a `0700` directory on POSIX; use a private directory with appropriate ACLs on Windows. A write failure can leave a partial, private bundle; inspect it and retry with a new output directory.

## Review before sharing

**Redaction is best effort, not a privacy guarantee.** Manually inspect both the sanitized log and metadata for private prompts, project data, arbitrary paths and URLs, unknown or encoded credentials, and secrets split across records. Retain required scenario evidence, exact tool arguments (except secrets), approvals, failures/retries, and limitations. If redaction removes information needed to judge a scenario, repeat it with synthetic non-sensitive data rather than claiming it passed.

The digest covers the exact bytes of the **sanitized** `run.log`, not the private raw source. If either the source or redaction rules change, generate a new bundle and verify it again. The `verify` command checks the log digest/size and basic manifest structure only: it does not authenticate the author, attest the metadata, guarantee privacy, or certify compatibility. Bundle status always remains `unreviewed`, even when the reported outcome is `passed`.

Only after explicit human review should a maintainer publish the sanitized artifacts at a reproducible public HTTPS location. Never upload the raw input. This repository does not automatically upload bundles or provide accounts/runners through the issue template.

## Promote evidence separately

An independent reviewer must compare the complete named-harness run against the [runbook](RUNBOOK.md), exact versions and commit, required approvals, failure/retry history, restart/replacement, and clean/unclean recovery. Generic bridge CI does not substitute for this run.

A completed run can then produce a separate record matching the [evidence schema](../spec/0.1/schemas/compatibility-evidence.schema.json), using the public sanitized-log digest and an `attestation` link to the reviewed artifacts. The formal schema accepts only `passed` or `failed`; `partial`, `not-run`, and informal observations belong in `compatibility/observations/`, not in `compatibility/evidence/`. Do not copy `bundle.json` into the evidence directory.

CI validates **every** file in the flat `compatibility/evidence/` directory against the formal schema, including failed, historical, and unregistered records. Keep that directory limited to regular JSON evidence files: no notes, subdirectories, symlinks, or bundles. This structural check does not promote a record, judge freshness, or establish that the harness really ran. Only independently reviewed current passing evidence may enter `registry.json`. Leave the adapter's compatibility issue open until its actual acceptance criteria are satisfied.

Local tooling checks (no additional harness installation):

```sh
node --test scripts/verification-bundle.test.mjs
go test ./spec -run 'Test(AllCompatibilityEvidence|UnregisteredCompatibilityEvidenceIsValidated)$'
```
