-- Operational state for native wake adapters. Message receipts remain authoritative.
CREATE TABLE wake_connections (
 scope_id TEXT NOT NULL REFERENCES scopes(scope_id) ON DELETE CASCADE,
 agent_id TEXT NOT NULL,
 execution_id TEXT NOT NULL,
 provider TEXT NOT NULL CHECK(provider IN ('claude-socket','codex-app-server')),
 last_seen_at BIGINT NOT NULL,
 last_attempt_at BIGINT NOT NULL DEFAULT 0,
 last_outcome TEXT NOT NULL DEFAULT 'waiting',
 PRIMARY KEY(scope_id,agent_id),
 FOREIGN KEY(scope_id,agent_id) REFERENCES agents(scope_id,agent_id) ON DELETE CASCADE
);
CREATE TABLE wake_attempts (
 scope_id TEXT NOT NULL REFERENCES scopes(scope_id) ON DELETE CASCADE,
 agent_id TEXT NOT NULL,
 execution_id TEXT NOT NULL,
 message_id TEXT NOT NULL REFERENCES messages(message_id) ON DELETE CASCADE,
 attempt_id TEXT NOT NULL,
 outcome TEXT NOT NULL CHECK(outcome IN ('dispatching','submitted','unavailable','uncertain')),
 tries BIGINT NOT NULL,
 updated_at BIGINT NOT NULL,
 PRIMARY KEY(scope_id,agent_id,execution_id,message_id),
 FOREIGN KEY(scope_id,agent_id) REFERENCES agents(scope_id,agent_id) ON DELETE CASCADE
);
CREATE INDEX wake_attempts_batch ON wake_attempts(scope_id,agent_id,execution_id,attempt_id);
