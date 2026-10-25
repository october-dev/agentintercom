-- Shared workspace channels. Subscription controls inbox notifications, not visibility.
CREATE TABLE channels (
 scope_id TEXT NOT NULL REFERENCES scopes(scope_id) ON DELETE CASCADE,
 channel_id TEXT NOT NULL,
 name TEXT NOT NULL,
 description TEXT NOT NULL,
 created_at BIGINT NOT NULL,
 PRIMARY KEY(scope_id,channel_id),
 UNIQUE(scope_id,name)
);
CREATE TABLE channel_subscriptions (
 scope_id TEXT NOT NULL,
 channel_id TEXT NOT NULL,
 agent_id TEXT NOT NULL,
 created_at BIGINT NOT NULL,
 PRIMARY KEY(scope_id,channel_id,agent_id),
 FOREIGN KEY(scope_id,channel_id) REFERENCES channels(scope_id,channel_id) ON DELETE CASCADE,
 FOREIGN KEY(scope_id,agent_id) REFERENCES agents(scope_id,agent_id) ON DELETE CASCADE
);
CREATE TABLE channel_posts (
 scope_id TEXT NOT NULL,
 post_id TEXT NOT NULL,
 channel_id TEXT NOT NULL,
 author_kind TEXT NOT NULL CHECK(author_kind IN ('agent','human')),
 author_id TEXT NOT NULL,
 body TEXT NOT NULL,
 search_body TEXT NOT NULL,
 thread_id TEXT,
 idempotency_key TEXT,
 request_hash TEXT NOT NULL,
 created_at BIGINT NOT NULL,
 PRIMARY KEY(scope_id,post_id),
 FOREIGN KEY(scope_id,channel_id) REFERENCES channels(scope_id,channel_id) ON DELETE CASCADE,
 FOREIGN KEY(scope_id,thread_id) REFERENCES channel_posts(scope_id,post_id)
);
CREATE UNIQUE INDEX channel_post_idempotency ON channel_posts(scope_id,author_kind,author_id,idempotency_key) WHERE idempotency_key IS NOT NULL;
CREATE INDEX channel_posts_history ON channel_posts(scope_id,channel_id,created_at DESC,post_id DESC);
CREATE INDEX channel_posts_threads ON channel_posts(scope_id,thread_id,created_at,post_id);
