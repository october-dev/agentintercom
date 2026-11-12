-- Short-lived browser approval for a CLI device. No account tokens leave the browser.
CREATE TABLE hosted_pairings (
 pairing_id TEXT PRIMARY KEY,
 user_code TEXT NOT NULL UNIQUE,
 device_code_hash TEXT NOT NULL UNIQUE,
 name TEXT NOT NULL,
 status TEXT NOT NULL DEFAULT 'pending' CHECK(status IN ('pending','approved','denied')),
 scope_id TEXT REFERENCES scopes(scope_id) ON DELETE CASCADE,
 device_id TEXT REFERENCES hosted_devices(device_id) ON DELETE CASCADE,
 credential TEXT,
 expires_at BIGINT NOT NULL,
 last_poll_at BIGINT NOT NULL DEFAULT 0
);
CREATE INDEX hosted_pairings_expiry ON hosted_pairings(expires_at);
