-- Support confidential MCP clients without storing their reusable secrets.
ALTER TABLE oauth_clients ADD COLUMN auth_method TEXT NOT NULL DEFAULT 'none'
  CHECK(auth_method IN ('none','client_secret_basic','client_secret_post'));
ALTER TABLE oauth_clients ADD COLUMN secret_hash TEXT COLLATE "C";
