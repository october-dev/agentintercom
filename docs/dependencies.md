# Dependency pins

Go module versions and checksums are recorded in `go.mod` and `go.sum`.
The TypeScript SDK records exact installation resolution in its package lock.
Workflow action revisions are pinned in the workflow files.

The PostgreSQL backend uses `github.com/jackc/pgx/v5` **v5.11.0** through its
`database/sql` adapter. It provides PostgreSQL parameter binding, verified TLS
connections and transaction-pooler-compatible execution without introducing a
second domain implementation. Its transitive module versions are recorded in the
same Go lock files. Source and release: [pgx](https://github.com/jackc/pgx/releases/tag/v5.11.0).

Hosted MCP authorization uses `github.com/go-oauth2/oauth2/v4` **v4.6.0** for
OAuth authorization-code/PKCE validation and token rotation. Its storage adapter
uses the existing workspace transaction and encrypted credentials. Source and
release: [go-oauth2](https://github.com/go-oauth2/oauth2/releases/tag/v4.6.0).

Hosted workspace billing uses the official `github.com/stripe/stripe-go/v87`
**v87.0.0** client and webhook signature verifier, pinned to Stripe API version
`2026-09-30.endive`. The service uses a separate workspace billing flow and does
not modify subscriptions belonging to another product. Release:
[stripe-go v87.0.0](https://github.com/stripe/stripe-go/releases/tag/v87.0.0).

The workspace browser uses `@supabase/auth-js` **2.118.0** for account-session
handling; it does not load Supabase database or realtime clients. `web/package-lock.json`
pins its dependency graph, the `esbuild` **0.28.2** build tool and
`@playwright/test` **1.64.1** browser test runner. No scripts are loaded from a CDN.
The generated assets in `bus/hostedweb` are embedded in the Go binary. Regenerate
with `npm ci --prefix web --ignore-scripts && npm run build --prefix web`.
