-- Deletion intent survives provider outages and process restarts. A closing
-- workspace cannot resume, start another purchase or transfer ownership.
ALTER TABLE hosted_workspaces ADD COLUMN deletion_started_at BIGINT NOT NULL DEFAULT 0;
CREATE INDEX hosted_workspace_deletions ON hosted_workspaces(deletion_started_at)
 WHERE deletion_started_at>0;

-- Retain only the payment references needed for support and delayed webhooks.
-- Content, memberships, email addresses and credentials are deleted with scope.
CREATE TABLE hosted_closed_workspaces (
 scope_id TEXT COLLATE "C" PRIMARY KEY,
 requested_by_hash TEXT COLLATE "C" NOT NULL,
 customer_id TEXT COLLATE "C" UNIQUE,
 subscription_id TEXT COLLATE "C",
 closed_at BIGINT NOT NULL
);
