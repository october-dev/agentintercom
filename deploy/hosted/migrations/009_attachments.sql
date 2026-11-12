-- Private file metadata. Bytes live in the service's private attachment directory.
CREATE TABLE attachments (
 attachment_id TEXT PRIMARY KEY,
 scope_id TEXT NOT NULL REFERENCES scopes(scope_id) ON DELETE CASCADE,
 author_kind TEXT NOT NULL CHECK(author_kind IN ('agent','human')),
 author_id TEXT NOT NULL,
 name TEXT NOT NULL,
 media_type TEXT NOT NULL,
 size_bytes BIGINT NOT NULL CHECK(size_bytes>=0 AND size_bytes<=16777216),
 sha256 TEXT NOT NULL,
 idempotency_key TEXT,
 request_hash TEXT NOT NULL,
 created_at BIGINT NOT NULL,
 deleted_at BIGINT NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX attachments_idempotency ON attachments(scope_id,author_kind,author_id,idempotency_key) WHERE idempotency_key IS NOT NULL;
CREATE INDEX attachments_scope ON attachments(scope_id,created_at DESC,attachment_id DESC) WHERE deleted_at=0;
CREATE TABLE attachment_usage (
 scope_id TEXT NOT NULL REFERENCES scopes(scope_id) ON DELETE CASCADE,
 month TEXT NOT NULL,
 uploaded_bytes BIGINT NOT NULL DEFAULT 0,
 downloaded_bytes BIGINT NOT NULL DEFAULT 0,
 PRIMARY KEY(scope_id,month)
);
