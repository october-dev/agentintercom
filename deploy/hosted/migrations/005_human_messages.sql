-- Human messages use the same durable message and receipt lifecycle as agents.
ALTER TABLE messages DROP CONSTRAINT messages_from_kind_check;
ALTER TABLE messages DROP CONSTRAINT messages_to_kind_check;
ALTER TABLE messages ADD CONSTRAINT messages_from_kind_check CHECK (from_kind IN ('agent','a2aPrincipal','human'));
ALTER TABLE messages ADD CONSTRAINT messages_to_kind_check CHECK (to_kind IN ('agent','a2aPrincipal','human'));
CREATE INDEX messages_scope_history ON messages(scope_id,created_at DESC,message_id DESC);
