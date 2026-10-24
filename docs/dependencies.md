# Dependency pins

Go module versions and checksums are recorded in `go.mod` and `go.sum`.
The TypeScript SDK records exact installation resolution in its package lock.
Workflow action revisions are pinned in the workflow files.

The PostgreSQL backend uses `github.com/jackc/pgx/v5` **v5.11.0** through its
`database/sql` adapter. It provides PostgreSQL parameter binding, verified TLS
connections and transaction-pooler-compatible execution without introducing a
second domain implementation. Its transitive module versions are recorded in the
same Go lock files. Source and release: [pgx](https://github.com/jackc/pgx/releases/tag/v5.11.0).
