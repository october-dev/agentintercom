-- Records which member revoked someone else's connection. Empty means the
-- connection's owner revoked it or the service did (for example after a
-- replayed grant). An owner may restore their own connection by replacing it,
-- but not one that a workspace owner or admin revoked.
ALTER TABLE hosted_devices ADD COLUMN revoked_by TEXT NOT NULL DEFAULT '';
