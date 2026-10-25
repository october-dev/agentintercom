# Releases

Agent Intercom publishes native runtime archives for macOS, Linux, and Windows on amd64 and arm64.

## Tagged runtime releases

A tag matching `v*` runs the full Go and TypeScript validation suite, builds the runtime and conformance runner, creates archives, generates SPDX SBOMs and SHA-256 checksums, attaches GitHub build-provenance attestations, and creates a GitHub release.

Release source must identify the merge commit of a main-branch PR. Authorization
can come from either an independent human approval of its final head, submitted
before merge by a GitHub owner, member or collaborator, or an explicit release
action by a human repository administrator. The administrator can dispatch the
npm workflow on `main` or push the native release tag. The gate checks the
triggering account's current admin permission through GitHub; rerunning a release
also checks the account requesting the rerun. This allows an owner to release
self-authored changes without inventing an independent review.

A trusted reviewer's unresolved changes request blocks both routes. Comments and
unpublished draft reviews do not replace submitted reviews. Direct commits and
unmerged branches cannot be released. Without explicit administrator release
authorization, stale, dismissed, post-merge-only, outsider-only and self-approvals
fail verification. This gate supplements branch protection and required CI; it
does not change GitHub settings or create signing identities. Native releases
still require a signed annotated tag verified by GitHub.

Release binaries embed the version from the tag. Tags containing a hyphen create a prerelease.

A stable tag must point to the original reviewed candidate source commit. Verification pins a separately reviewed `main` evidence commit and requires all six launch-core host records to match both the tag's runtime version and source SHA. This allows evidence to land after candidate publication without rebuilding a different source revision. Native archives are still built from the tagged source, not from the later evidence checkout.

Download the archive for your operating system and architecture from the [GitHub releases page](https://github.com/october-dev/agent-intercom/releases). Verify its SHA-256 value against `checksums.txt`, extract the archive, and place the `agentintercom` binary on your `PATH`. Each archive also contains the conformance runner, license, specification, and documentation.

Before creating a tag:

1. confirm the protocol and package versions;
2. run the complete local validation suite;
3. update release notes and migration guidance;
4. verify that no compatibility claim exceeds current public evidence;
5. use an annotated, signed Git tag.

Platform code signing and notarization require the relevant platform identities. Unsigned artifacts must remain clearly identified until those identities and verification steps are configured.

The release workflow smoke-tests the Linux amd64, Linux arm64, macOS arm64 and Windows amd64 archives on real runners of that architecture before publishing. A host product that installs the pinned Linux helper (October Desktop's Agents Anywhere release) verifies the archive against `checksums.txt` and the build-provenance attestation; the exact commands are in [desktop-remote-integration.md](desktop-remote-integration.md).

Native builds require a clean checkout and embed VCS metadata. After smoke tests,
the workflow generates `remote-bus-runtime.json` from the exact Linux archives:
archive and binary SHA-256 per architecture, tag-derived runtime version,
protocol version, source commit, `sourceModifiedAtBuild: false` and
`qualification: "release"`. Generation rejects binaries with missing metadata,
a different commit, a dirty source tree, the wrong architecture or CGO enabled.
The manifest and a standalone `LICENSE` are included in `checksums.txt` and
published beside the archives. Consumers can copy the manifest into their pin;
publication still requires release authorization and a verified signed tag.

## npm CLI and TypeScript distribution

The npm package uses pre-1.0 versions. From `0.1.0-next.15`, it contains both the TypeScript SDK and a Node launcher for the native Go daemon. Six exact-version optional dependencies, named `agentintercom-{darwin,linux,win32}-{x64,arm64}`, carry the prebuilt executables. The launcher does not download binaries, execute a shell, or fall back to PATH. Linux builds use `CGO_ENABLED=0`, so a separate musl package is unnecessary.

The prerelease workflow accepts reviewed `main` commits, runs Go and SDK validation, cross-builds all six native packages, and packs the SDK/launcher. It then installs the actual tarballs through a temporary registry on Linux, macOS, and Windows. These tests verify automatic optional-package selection, `npm exec`, a real daemon demo, SDK imports, and failure exit codes with install scripts disabled.

Only after those checks pass does the `npm` environment publish the six platform packages, followed by the parent package, using GitHub OIDC and provenance. The parent `next` tag is not updated if a platform publish fails. Reruns accept already-published versions only when their tarball integrity matches exactly. If an artifact differs, bump the parent version instead of trying to overwrite it; platform versions and optional dependency pins are generated from that version. Stable releases still require an approved stable protocol and SDK compatibility policy.

npm may accept a publication before its public version listing is available. The
publisher checks visibility every ten seconds for up to ten minutes before moving
to the next package. Only missing-version reads are retried; integrity mismatches,
malformed metadata and other registry errors stop publication immediately. If
visibility times out, wait for npm to finish processing and rerun only the failed
job with its original artifacts. An accepted publish is never repeated by the wait.

Builds record the source commit, a digest of tracked and non-ignored source files, and the native binary integrity. Packing rejects an outdated version, source, target metadata, or changed binary. The SDK compiles into a new temporary staging directory, excluding stale output and published lifecycle/dev scripts. Every `.tgz` has a `.tgz.json` record identifying the checked package, required executable path, source, and SHA-512 integrity. These records travel with the CI artifacts; publishing rejects missing, changed, or mismatched records before contacting npm. They detect accidental artifact mixups, not a malicious replacement of both the workflow and its records; GitHub review, protected artifacts and provenance remain the trust boundary.

The publisher then preflights **all seven** immutable versions before its first registry write. An existing-content mismatch or a registry/authentication error at the final package cannot partially publish the earlier packages. Publishing itself is not transactional: a failure during the write phase can leave some native versions published. Preserve the exact tarballs and records for a retry. Identical existing versions are not retagged, avoiding accidental rollback of another release's `next` tag.

### Candidate publication and stable promotion

The dispatch workflow retains its `publish-npm-prerelease.yml` filename for trusted-publisher identity. Its explicit `channel` selects:

- `next`: prerelease versions only, published with provenance under `next`.
- `candidate`: a stable version, with exact `confirm_version`, published under a non-default `candidate` tag. This does not declare launch readiness.
- `latest`: promote already-published stable artifacts, never rebuild or republish them. Supply the exact version and original successful candidate workflow's `candidate_run_id`.

Promotion validates the original run's repository, workflow, main branch and success; both the candidate and evidence/promotion commits must meet the release-authorization policy above. It checks the original tarballs against their original clean source checkout and all seven public registry integrities. Current evidence must pass the exact-version `--require-attestation --launch-core` gate for all six launch hosts. Only then are native tags moved, followed by the parent. A retry repeats exact-version assignments; dist-tags are not transactional. Keep candidate artifacts available until promotion finishes—expired artifacts must not be replaced by an unverified rebuild.

Evidence normally lands after candidate publication. Rebuilding from the evidence commit would change source identity and make immutable-version promotion impossible; the separate original-artifact lane avoids this cycle.

OIDC authorizes publication, not arbitrary dist-tag changes. The promotion job requires a separately approved, narrowly scoped `NPM_DIST_TAG_TOKEN` in the protected `npm` environment; provision/rotate it under maintainer policy, never in this repository. Existing local interactive npm authentication is another operator option for tag promotion. Do not weaken provenance or package protection to bypass missing authority. See [npm's trusted-publishing limitations](https://docs.npmjs.com/trusted-publishers/).

These checks are necessary, not sufficient for stable launch. Platform execution coverage, protocol freeze, signing/notarization or an explicitly approved unsigned boundary, retention/upgrade evidence and the real-user pilot remain release sign-offs. No account settings, secrets, tags or publications are changed by preparing this code.

### First-publication setup

The six new package names require initial publication before npm allows a trusted
publisher to be configured. The workflow preserves a signed Sigstore provenance
bundle for each checked tarball after all platform smoke tests pass. A maintainer
can use those exact CI artifacts and interactive npm authentication to establish
ownership, with provenance, before enabling trusted publishing. No placeholder
package or automation token is necessary.

For the first release, download `npm-distribution` and all `npm-provenance-*`
artifacts from the authorized workflow run. Before publishing anything, verify
every tarball against its artifact record and its bundle, pinning the source SHA
from that run:

```sh
# GitHub CLI requires a .json or .jsonl extension for a local bundle.
cp "$TARBALL.sigstore" "$TARBALL.sigstore.json"
gh attestation verify "$TARBALL" --bundle "$TARBALL.sigstore.json" \
  --repo october-dev/agentintercom --digest-alg sha512 \
  --signer-workflow october-dev/agentintercom/.github/workflows/publish-npm-prerelease.yml \
  --source-ref refs/heads/main --source-digest "$SOURCE_SHA" \
  --deny-self-hosted-runners
```

Publish the six native packages first with `--tag next --access public
--ignore-scripts --provenance-file "$TARBALL.sigstore"`. npm verifies and attaches
the supplied signed CI bundle. Do not also pass `--provenance`, even with a false
value: npm treats the two CLI options as mutually exclusive. The package manifest
leaves this choice to the publishing command; the normal CI publisher always
passes `--provenance` to generate its statement. Never omit the bundle for an
interactive publication. Preserve all original tarballs for integrity-safe retries.

After each package exists, configure its publisher with `npm trust github
PACKAGE --repo october-dev/agentintercom --file publish-npm-prerelease.yml
--env npm --allow-publish`. Then rerun the failed publish job: it accepts identical
native packages and publishes the parent through its existing trusted publisher.
The initial workflow's publish job may fail while the native names are unowned;
do not rebuild its artifacts when retrying. Authentication failures must not be
worked around by publishing a parent whose binaries are unavailable or dropping
provenance. See [npm trusted publishing](https://docs.npmjs.com/trusted-publishers/)
and the [`provenance-file` option](https://docs.npmjs.com/cli/v11/using-npm/config#provenance-file).

### Local package validation

From `sdk/typescript`:

```sh
npm ci --ignore-scripts
npm run test:errors
npm run build:native
npm run pack:distribution
npm run test:distribution
```

Pass `-- --all` to the build and pack commands for every platform. Generated manifests, binaries, tarballs, and artifact records live under ignored `dist/npm/`; do not commit them. Finish source edits and commit before building release artifacts: a new commit or source change invalidates old build stamps. Packing stages the parent manifest with its six exact-version optional dependencies. They are deliberately absent from the source lockfile so `npm ci` works before a new version's platform packages exist; publishing directly from the source SDK directory is blocked. The bundled binary embeds the npm version and is built from the same commit as its SDK, including the session-retirement endpoint needed by the updated SDK. Native Go release tags and archives retain their own versioning.

The combined validation and actual-binary upgrade rehearsal are documented in [launch validation](launch-validation.md). Neither command publishes or downloads harnesses. Publication, public-registry installation tests, independent harness evidence and signing still need separate sign-off. Never move `latest` merely to make an unqualified `npx` command work; keep prerelease documentation explicit about the version or `next` tag until a stable release is approved.
