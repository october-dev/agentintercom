# Hosted workspace service

The hosted service runs the common Bus HTTP/MCP runtime over PostgreSQL. Its
account API adds workspace membership and revocable devices. These APIs are
available in source; browser onboarding, MCP OAuth and checkout must be deployed
and qualified together before offering a public hosted signup.

## Account and workspace authority

Account routes authenticate direct Supabase sessions against the configured Auth
service. They validate issuer, audience, expiry, role and verified email. Positive
verification results are cached for at most 30 seconds. Account identity comes
from verification, never request fields. Third-party Supabase OAuth tokens are
not accepted as direct account sessions.

A workspace has owner, admin and member roles. Owners manage roles and other
owners; removing or demoting the last owner is refused. Admins invite members and
remove members or their devices. Members can leave, enroll their own devices and
revoke them. All members share workspace collaboration state.

Invitations bind to a verified email, expire after seven days and can be accepted
once. An existing member accepting an invitation retains their current role.
The create response contains the invitation secret once; later lists omit it.
Pass invite secrets in a POST body, never a query string or analytics event.

Each device has its own credential, issued once and stored as a digest. Enrollment
expires after a year. A device can start a fresh session or resume one it owns;
resuming replaces the old execution token and preserves its inbox. It cannot
replace another device's session. New hosted sessions are linked to the other
active sessions in that workspace. Sessions still use the common five-minute
lease and must heartbeat while connected.

Removing a member or revoking a device retires its executions, releases inbox
reservations and task claims, and prevents new registrations. Device expiry is
also checked by the ordinary protocol routes. Other members remain connected.

## HTTP routes

Account routes require `Authorization: Bearer <account-session>`:

| Method | Route | Purpose |
| --- | --- | --- |
| GET / POST | `/api/workspaces` | List memberships / create a workspace with `name` |
| GET | `/api/workspaces/{id}/members` | List members |
| PATCH / DELETE | `/api/workspaces/{id}/members/{user}` | Change `role` / remove or leave |
| GET / POST | `/api/workspaces/{id}/invites` | List pending invites / invite `email` with `role` |
| DELETE | `/api/workspaces/{id}/invites/{invite}` | Revoke an invitation |
| POST | `/api/invites/accept` | Accept an invitation `token` |
| GET / POST | `/api/workspaces/{id}/devices` | List connections / enroll a device with `name` |
| DELETE | `/api/workspaces/{id}/devices/{device}` | Revoke a connection |
| GET | `/api/workspaces/{id}/sessions?after=...` | Page through sessions and machine/member attribution |

`POST /api/device/sessions` instead requires a device bearer credential and a
`name`; optional `resumeId` must name a session owned by that device. Its response
contains the new execution credential. Keep it in a private process environment
or owner-only connection file, never application logs. Use it for `/mcp` and the
ordinary `/v1` agent APIs. It cannot administer workspace members or devices.

Responses use the standard `{ok,result}` / `{ok:false,error}` envelope. Unknown
input fields are rejected. Browser requests need an explicitly allowed HTTPS
origin; only the configured public Host is accepted. The hosted front door does
not expose `/v1/admin` or administrator scope creation.

The session directory reports `deliveryMode: "poll"` for generic connections.
That means MCP tools are available when the application invokes them; a current
lease does not imply native wake support.

## Initial allowances

A new workspace has a 14-day trial, five members and up to 50 concurrent sessions.
The trial allows 2,000 new deliveries in total, including when it crosses a month.
An active paid entitlement permits 100,000 new deliveries per UTC calendar month.
Counters are committed with messages. An idempotent retry, receipt, poll,
heartbeat, acknowledgement or response to an accepted request does not count as
a new delivery. A stopped subscription prevents new work and new sessions;
existing executions can finish accepted requests while their credentials remain
valid. Account access remains available for workspace management.

The account API currently caps ownership at three workspaces and enrollment at
20 live connections per member per workspace. Invitations cannot exceed the
workspace's member limit when accepted.

## Run behind a TLS proxy

Supply these through an owner-only service environment file or secret manager:

- `OCTOBER_BUS_DATABASE_URL`: migrated PostgreSQL connection with verified TLS.
- `OCTOBER_BUS_ENCRYPTION_KEY`: base64 encoding of 32 cryptographically random bytes.
- `OCTOBER_BUS_AUTH_URL`: trusted Supabase Auth origin.
- `OCTOBER_BUS_AUTH_PUBLIC_KEY`: that project's public API key.

```sh
october-bus database migrate
october-bus hosted serve --public-url https://bus.example.com \
  --allow-origin https://www.example.com
```

The default listener is `127.0.0.1:4780`. Preserve the public Host through the TLS
proxy. Additional trusted Auth issuers can be declared explicitly with repeated
`--auth-issuer` flags when migrating a project's authentication domain.

Service-owned workspace/session credentials are encrypted with AES-256-GCM,
including an identity-specific authenticated purpose. The runtime's credential
indexes and user device/invite credentials remain digests. Back up the encryption
key separately from the database; losing it prevents decrypting service grants.
Do not replace this key during an ordinary deployment. Backups, restore and
credential-key rotation need an explicit deployment procedure.

Canonical SQL is in [the migration directory](../deploy/hosted/migrations/README.md).
The service never runs DDL at startup and does not modify another application's
account, billing or coordination tables.
