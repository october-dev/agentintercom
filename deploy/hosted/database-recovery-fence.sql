-- Apply only to a newly restored, private service before reconnecting clients.
-- A snapshot can predate revocations. Retain work/history, invalidate connection
-- authority, and require operator review before each workspace is re-enabled.
\set ON_ERROR_STOP on
BEGIN;
UPDATE october_bus.hosted_workspaces
SET disabled_at = (extract(epoch FROM clock_timestamp()) * 1000)::bigint
WHERE disabled_at = 0;
UPDATE october_bus.hosted_devices
SET revoked_at = (extract(epoch FROM clock_timestamp()) * 1000)::bigint
WHERE revoked_at = 0;
UPDATE october_bus.hosted_invites
SET revoked_at = (extract(epoch FROM clock_timestamp()) * 1000)::bigint
WHERE revoked_at = 0;
DELETE FROM october_bus.oauth_tokens;
DELETE FROM october_bus.oauth_requests;
DELETE FROM october_bus.hosted_pairings;
UPDATE october_bus.agents
SET lease_expires_at=0,ready=0,lifecycle='offline';
UPDATE october_bus.reservations SET expires_at=0;
COMMIT;
