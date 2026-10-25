# Contributing to October Bus

October Bus welcomes protocol improvements, adapters, SDKs, examples, tests, and runtime fixes.

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
go run ./cmd/october-bus demo

cd sdk/typescript
npm ci
npm run typecheck
npm run build
npm run test:errors
```

Run the TypeScript integration test with a freshly built daemon:

```bash
go build -o /tmp/october-bus ./cmd/october-bus
cd sdk/typescript
OCTOBER_BUS_BINARY=/tmp/october-bus npm run test:integration
```

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
