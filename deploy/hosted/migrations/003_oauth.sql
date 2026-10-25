-- Public OAuth clients receive only workspace-scoped Bus credentials.
CREATE TABLE oauth_clients (
  client_id TEXT COLLATE "C" PRIMARY KEY,
  name TEXT NOT NULL,
  redirects_json TEXT NOT NULL,
  created_at BIGINT NOT NULL
);
CREATE TABLE oauth_requests (
  request_id TEXT COLLATE "C" PRIMARY KEY,
  client_id TEXT COLLATE "C" NOT NULL REFERENCES oauth_clients(client_id),
  redirect_uri TEXT NOT NULL,
  state TEXT NOT NULL,
  challenge TEXT NOT NULL,
  resource TEXT NOT NULL,
  created_at BIGINT NOT NULL,
  expires_at BIGINT NOT NULL,
  consumed_at BIGINT NOT NULL DEFAULT 0
);
CREATE INDEX oauth_requests_expiry ON oauth_requests(expires_at);
CREATE TABLE oauth_tokens (
  token_id TEXT COLLATE "C" PRIMARY KEY,
  client_id TEXT COLLATE "C" NOT NULL REFERENCES oauth_clients(client_id),
  device_id TEXT COLLATE "C" NOT NULL REFERENCES hosted_devices(device_id) ON DELETE CASCADE,
  code_hash TEXT COLLATE "C" UNIQUE,
  access_hash TEXT COLLATE "C" UNIQUE,
  refresh_hash TEXT COLLATE "C" UNIQUE,
  payload TEXT NOT NULL,
  code_consumed BIGINT NOT NULL DEFAULT 0,
  access_revoked BIGINT NOT NULL DEFAULT 0,
  refresh_consumed BIGINT NOT NULL DEFAULT 0,
  expires_at BIGINT NOT NULL
);
CREATE INDEX oauth_tokens_device ON oauth_tokens(device_id);
CREATE INDEX oauth_tokens_expiry ON oauth_tokens(expires_at);
