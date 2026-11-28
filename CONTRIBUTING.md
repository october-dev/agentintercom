# Contributing to Agent Intercom

Agent Intercom welcomes protocol improvements, adapters, SDKs, examples, tests, and runtime fixes.

## Before you start

- Open an issue before a protocol change or large feature.
- Keep changes focused on agent communication and coordination.
- Keep product decisions such as staffing, model selection, routing, supervision, and outcome scoring outside the Bus.
- Do not weaken durability, execution authority, or harness permissions for convenience.

## Development

The Go module language floor is 1.26; `go.mod` selects the Go 1.27 toolchain and CI builds/tests with Go 1.27.x. Use Go 1.27 for reproducible development. The TypeScript SDK requires Node.js 20 or newer; CI includes Node 20/22 compatibility checks and the primary Node 24 SDK/integration jobs.

```bash
go test -race -count=1 ./...
go vet ./...
go run ./cmd/agentintercom demo

cd sdk/typescript
npm ci
npm run typecheck
npm run build
npm run test:errors
```

Run the TypeScript integration test with a freshly built daemon:

```bash
go build -o /tmp/agentintercom ./cmd/agentintercom
cd sdk/typescript
OCTOBER_BUS_BINARY=/tmp/agentintercom npm run test:integration
```

## Workspace UI (browser) development

The browser workspace needs no production credentials, paid AI subscription, or running hosted account service. The browser tests intercept every `/api` and auth request with fake accounts and API responses on loopback, so a clean checkout plus Node.js is enough. Use Node.js 22 or newer: the UI's locked dependencies require it (see `web/package-lock.json`), so the TypeScript SDK's Node.js 20 floor does not apply here. CI runs this workflow with Node.js 24 (see the `workspace-ui` job in `.github/workflows/ci.yml`).

Source lives in `web/src`; the Go server embeds and serves generated files from `bus/hostedweb/`. Never edit `bus/hostedweb/` by hand: edit `web/src`, rebuild, and commit the regenerated files in the same pull request.

From a clean checkout:

```bash
cd web
npm ci --ignore-scripts
npm run build
npx playwright install chromium
npm test
```

`npm ci --ignore-scripts` installs UI dependencies, `npm run build` regenerates `bus/hostedweb/app.js`, `bus/hostedweb/index.html`, and `bus/hostedweb/style.css` from `web/src`, `npx playwright install chromium` fetches the browser the tests drive (add `--with-deps` on a fresh Linux machine for OS packages), and `npm test` serves the built bundle on `http://127.0.0.1:4794` and runs the mocked Playwright suite. Rebuild after every `web/src` change; CI rebuilds and fails on drift with `git diff --exit-code -- ../bus/hostedweb`, so an uncommitted regeneration breaks the build.

Run one browser test by name while iterating:

```bash
cd web
npx playwright test -g "part of the test name"
```

Watch it in a real browser window with `npx playwright test --headed -g "part of the test name"`. The window closes when the test ends; use `--debug` instead of `--headed` to pause and step through it in the Playwright Inspector. To drive a browser executable you already have instead of the downloaded one, set `CHROMIUM_PATH=/path/to/chromium` (see `web/playwright.config.js`). The mobile onboarding and signed-out sign-in tests also write full-page screenshots to `/tmp/agentintercom-*.png` each time they run, which is a quick way to inspect those layouts without keeping a browser open.

## Protocol changes

Protocol behavior belongs in the versioned specification and schemas. A change must include tests and explain its effect on existing clients. See [the specification policy](spec/README.md).

## Adapters

Each adapter belongs in its own directory under `adapters/` and must include an `adapter.json` manifest. Experimental adapters must state their limitations. An adapter becomes verified only after its released version passes the applicable public conformance profile.

New adapters should keep registration, heartbeat, credential handling, and cleanup outside the model loop when the host allows it.

## Pull requests

- Add or update tests for changed behavior.
- Update public documentation for user-visible changes.
- Keep credentials, private messages, local databases, and runtime files out of commits.
- Use clear commit messages that describe the user-visible outcome.
- Confirm compatibility claims against the public evidence registry.

## Review and merge policy

- Changes to `main` go through a pull request.
- Wait for every required check to pass before merging.
- External contributions require an approving maintainer review.
- Protocol, runtime, dependency, security, and workflow changes require code-owner review.
- Resolve review conversations and request a new review after material changes.
- Never merge with a failed, skipped, or incomplete required check.
- Changes to a pinned dependency must update the public pin record in the same pull request.
- Release tags are created only from reviewed commits with passing checks.

By contributing, you agree that your contribution is licensed under Apache 2.0.
