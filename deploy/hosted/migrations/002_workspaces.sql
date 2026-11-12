-- Standalone workspace accounts and revocable connections. User IDs refer to
-- verified external identities; no dependency on another application's tables.
CREATE TABLE hosted_workspaces (
  scope_id TEXT COLLATE "C" PRIMARY KEY REFERENCES scopes(scope_id) ON DELETE CASCADE,
  name TEXT NOT NULL,
  owner_credential TEXT NOT NULL,
  created_at BIGINT NOT NULL,
  trial_ends_at BIGINT NOT NULL,
  paid_until BIGINT NOT NULL DEFAULT 0,
  disabled_at BIGINT NOT NULL DEFAULT 0,
  member_limit BIGINT NOT NULL DEFAULT 5 CHECK(member_limit > 0),
  session_limit BIGINT NOT NULL DEFAULT 50 CHECK(session_limit > 0),
  delivery_limit BIGINT NOT NULL DEFAULT 100000 CHECK(delivery_limit > 0)
);
CREATE TABLE hosted_members (
  scope_id TEXT COLLATE "C" NOT NULL REFERENCES hosted_workspaces(scope_id) ON DELETE CASCADE,
  user_id TEXT COLLATE "C" NOT NULL,
  email TEXT NOT NULL,
  role TEXT NOT NULL CHECK(role IN ('owner','admin','member')),
  joined_at BIGINT NOT NULL,
  PRIMARY KEY(scope_id,user_id)
);
CREATE INDEX hosted_members_user ON hosted_members(user_id,scope_id);
CREATE TABLE hosted_invites (
  invite_id TEXT COLLATE "C" PRIMARY KEY,
  scope_id TEXT COLLATE "C" NOT NULL REFERENCES hosted_workspaces(scope_id) ON DELETE CASCADE,
  token_hash TEXT COLLATE "C" NOT NULL UNIQUE,
  email TEXT NOT NULL,
  role TEXT NOT NULL CHECK(role IN ('admin','member')),
  invited_by TEXT COLLATE "C" NOT NULL,
  created_at BIGINT NOT NULL,
  expires_at BIGINT NOT NULL,
  consumed_at BIGINT NOT NULL DEFAULT 0,
  revoked_at BIGINT NOT NULL DEFAULT 0
);
CREATE INDEX hosted_invites_scope ON hosted_invites(scope_id,created_at DESC);
CREATE TABLE hosted_devices (
  device_id TEXT COLLATE "C" PRIMARY KEY,
  scope_id TEXT COLLATE "C" NOT NULL REFERENCES hosted_workspaces(scope_id) ON DELETE CASCADE,
  user_id TEXT COLLATE "C" NOT NULL,
  name TEXT NOT NULL,
  kind TEXT NOT NULL CHECK(kind IN ('device','mcp')),
  token_hash TEXT COLLATE "C" UNIQUE,
  created_at BIGINT NOT NULL,
  expires_at BIGINT NOT NULL,
  revoked_at BIGINT NOT NULL DEFAULT 0,
  FOREIGN KEY(scope_id,user_id) REFERENCES hosted_members(scope_id,user_id) ON DELETE CASCADE
);
CREATE INDEX hosted_devices_user ON hosted_devices(scope_id,user_id,revoked_at);
CREATE TABLE hosted_sessions (
  scope_id TEXT COLLATE "C" NOT NULL,
  agent_id TEXT COLLATE "C" NOT NULL,
  device_id TEXT COLLATE "C" NOT NULL REFERENCES hosted_devices(device_id) ON DELETE CASCADE,
  credential TEXT NOT NULL,
  created_at BIGINT NOT NULL,
  PRIMARY KEY(scope_id,agent_id),
  FOREIGN KEY(scope_id,agent_id) REFERENCES agents(scope_id,agent_id) ON DELETE CASCADE
);
CREATE INDEX hosted_sessions_device ON hosted_sessions(device_id);
CREATE TABLE hosted_usage (
  scope_id TEXT COLLATE "C" NOT NULL REFERENCES hosted_workspaces(scope_id) ON DELETE CASCADE,
  month TEXT COLLATE "C" NOT NULL,
  deliveries BIGINT NOT NULL DEFAULT 0,
  PRIMARY KEY(scope_id,month)
);
