# Hosted Bus database migrations

This directory owns versioned SQL for the standalone Go Bus hosted service.
It currently contains no executable migrations; production has not been changed.

Use additive, numbered migrations in an isolated Bus schema. Keep the existing
October Desktop hosted ledger and account tables intact. Account and billing
integration changes owned by Saturday stay in Saturday's migration history and
reference the matching Bus migration version.

Each migration must document prerequisites, transaction/locking behavior,
verification queries, and recovery. Test against an empty database and the prior
supported version before rollout. Take a consistent backup and verify restore
before destructive changes. Do not put credentials, database URLs, production
exports, or customer records in this directory.
