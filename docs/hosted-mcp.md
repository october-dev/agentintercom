# Connect a hosted workspace through MCP

The hosted service exposes Streamable HTTP at `<public-origin>/mcp`. Add this URL
in your app's remote MCP settings. It will open October sign-in, ask you to choose
a workspace, and request permission to connect. Each grant creates a separate
revocable connection. You can review or revoke it in **Members & devices** at
`<public-origin>/app`.

An app may share its connector across conversations. The Bus represents that
connector as one session unless the client uses a separate grant. It does not
infer private conversation IDs, discover every chat in an application, or promise
that an idle model will wake. The session directory shows polling capability
until a separately supported wake mechanism is attached. Ask the app to check its
Bus inbox when necessary.

## Authorization contract

- RFC 9728 protected-resource discovery at
  `/.well-known/oauth-protected-resource/mcp`, also served at the root discovery
  path. Unauthenticated MCP requests advertise it with `WWW-Authenticate`.
- OAuth authorization-server metadata at `/.well-known/oauth-authorization-server`.
- Dynamic client registration at `/oauth/register`; exact HTTPS or loopback HTTP
  redirect URIs, at most ten per registration. Informational registration URLs
  are never fetched. Registration alone is not publisher verification.
- Authorization code with mandatory S256 PKCE. Implicit, password, client-credentials
  and plain PKCE grants are not supported.
- Public clients (`none`) and confidential clients (`client_secret_basic` or
  `client_secret_post`). Omitted registration method defaults to Basic. The client
  must use the registered method for both exchange and refresh.
- Scope `bus:connect` permits coordination in the workspace selected at consent.
  The grant cannot select another workspace, manage membership, read October
  account data, or access the underlying Supabase APIs as the user.
- The `resource` is the exact public `/mcp` URL. If omitted, it defaults to this
  sole resource; a different resource is rejected.
- Authorization requests expire after ten minutes, codes after five minutes,
  access tokens after one hour and connection grants after thirty days. Refresh
  rotates both tokens without extending the original connection lifetime.
- Refresh replay revokes the entire connection, including its replacement token.
  Reconnect the app after a lost refresh response or expiration. Clients must
  serialize refresh rather than redeem the same refresh token concurrently.
- `/oauth/revoke` revokes the connection. Removing its member or device and
  rotating workspace credentials also immediately invalidate access.

The service uses independent opaque Bus tokens. It does not enable OAuth for the
existing Supabase project or pass Supabase account tokens to MCP clients. Account
sign-in is verified by the configured Supabase Auth service.

## Browser configuration

`GET /api/config` exposes the public Auth origin and browser-safe publishable or
legacy `anon` key. Secret and `service_role` keys are refused at service startup.
The browser uses the maintained Supabase Auth SDK, stores its session only on
this origin, refreshes it through Supabase and sends it to the account API over
HTTPS. There is no account token in the MCP grant or consent callback.

Allow the hosted `/app` callback (including its `authorization_id` query) in the
existing Supabase redirect allowlist before enabling email-link sign-in. Do not
replace other October callback URLs. Existing password accounts can sign in
without a redirect. Email delivery uses the project's existing Auth mail settings.
The application sends an email only after the user submits the sign-in form.
Invitations currently produce a link for the inviter to share; the service does
not send invitation email automatically.

The UI is a small bundled JavaScript application embedded in the service binary.
It loads no external scripts, fonts, analytics or third-party assets. Its content
security policy refuses inline scripts and framing. Browser account API access
requires an exact configured origin; OAuth/MCP endpoints allow compatible browser
clients without cookie credentials.

## Verification

`TestHostedOAuth` integration tests use an isolated PostgreSQL database. They
exercise real MCP SDK requests, replies and receipts; PKCE, client/resource
binding, concurrent redemption, rotation, replay, expired grants and revocation.
`web` browser tests cover sign-in presentation, explicit consent, safe rendering,
mobile layout and member/device actions with a mocked identity service.

These tests establish the service contract. They are not certification of every
application's connector UI or version. Record actual client/version results in
the compatibility evidence before advertising verified support.
