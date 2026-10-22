# Deployment

## Complete Compose stack

Copy `.env.example` to `.env`, set your allowed email(s), and run `docker compose up -d --build`. The gateway listens only on loopback by default. ttyd has no published host port. The terminal copies the pinned static ttyd 1.7.7 executable into Debian instead of retaining the upstream image's obsolete OS base. Both services run as non-root users with read-only root filesystems, dropped capabilities and persistent data volumes. The terminal home volume is writable; software installation belongs in a derived terminal image.

Run `docker compose exec gate shell-gate invite --email you@example.com` to set up an account at the configured public address. Codes expire after one hour by default, are single-use, and must be shared privately. Enter the code under **Set up or recover an account**, then choose a password of 12 to 128 characters. A passkey can be chosen instead. An invitation proves operator authorization, not email-delivery verification. To recover a password, issue another invitation for the same email; existing local cookies become invalid on their next request or WebSocket handshake. Already open terminal connections continue until disconnected. To add another account, add its email to the allowlist, recreate the gateway and issue an invitation. To deny an account immediately, remove its email and recreate the gateway; existing sessions are checked against the current list on every request.

For a remote host without a domain, keep its port on loopback and use `ssh -L 18765:127.0.0.1:18765 your-server`, then open `http://localhost:18765`. No external authentication service is involved.

## HTTPS

Set `SHELL_DOMAIN=shell.example.com` in `.env`, point its DNS to your server, allow inbound 80/443 and run:

```sh
docker compose -f compose.yaml -f compose.https.yaml up -d --build
docker compose -f compose.yaml -f compose.https.yaml exec gate shell-gate invite --email you@example.com
```

Caddy obtains a certificate and the override sets the gateway's public URL to that exact HTTPS origin. Open `https://shell.example.com/login`. Passkeys for localhost do not migrate to another domain; create an invitation and enroll at the final origin. An existing reverse proxy can forward HTTP and WebSocket upgrades to the loopback port instead. Always set `SHELL_PUBLIC_URL` to the browser-visible origin. Local authentication requires a hostname (`localhost` for development), not an IP address, because its WebAuthn RP ID must be a domain; `doctor` rejects that unsupported configuration before startup. See the [WebAuthn RP ID rules](https://www.w3.org/TR/webauthn-3/#relying-party-identifier). For isolated deployments, preload images and use an operator-provided trusted TLS certificate instead of public certificate issuance.

## Configuration

| Setting | Default / behavior |
| --- | --- |
| `SHELL_ALLOWED_EMAILS` | Required comma-separated emails; no author account or wildcard default. |
| `SHELL_PUBLIC_URL` | `http://localhost:18765`; exact origin, HTTPS outside localhost. SSO uses it as the callback base when edge routing is off. |
| `SHELL_AUTH_MODE` | `local`; legacy external settings select `supabase` for compatibility; `sso` (alias `oidc`) selects generic OIDC. |
| `SHELL_LISTEN` | `127.0.0.1:18765`; constrain exposure with host/firewall or Compose bind address. |
| `SHELL_TTYD_URL` | Required unless `SHELL_TTYD_TARGETS` is set; private HTTP(S) URL without embedded credentials or query. Compose supplies its terminal service URL. |
| `SHELL_TTYD_TARGETS` | Optional `local=http://terminal:7681,build=https://terminal.example.com`; first entry default. |
| `SHELL_DATA_DIR` | `$XDG_CONFIG_HOME/shell-gate` or `~/.config/shell-gate`; Compose uses `/data`. |
| `SHELL_DATABASE_URL` | `workspace.db` in data directory; optional PostgreSQL DSN. |
| `SHELL_SESSION_TTL` | `12h`; positive duration, same expiry for token and cookie. |
| `SHELL_NETMON_INTERVAL` | `15s`; zero disables active probes. |
| `SHELL_DEFAULT_PRESETS` | Empty; JSON name/command list seeded once per account. |
| `SHELL_DEBUG_LOGS` | `false`; explicit opt-in diagnostics. |
| `SHELL_LEGACY_SQLITE_PATH` | Empty; explicit atomic workspace import into empty destination. |
| `SHELL_WAKE_COMMANDS` / `SHELL_SESSION_LEASE_COMMANDS` | Optional machine-to-executable hook maps; see Mac guide. |
| `SHELL_WAKE_HOME_PRESENCE_*` | Optional presence policy; never needed for local deployment. |

Compose passes the SSO settings below from `.env`; advanced upstream settings and the legacy GoTrue adapter require an override's `environment`. The program itself reads the process environment, not `.env` files. Invalid settings fail startup. `shell-gate doctor --json` validates configuration without contacting an upstream or writing data; `invite --dry-run --json` validates an invitation without creating it. CLI JSON uses `{ok,data}` on success and `{ok:false,error:{code,message}}` on failure; exit 0 means success, 1 failure. Invitation output intentionally contains a secret; never send it to application logs.

## Direct binary and service

Install using the README command or download a checksummed release. On the upstream host, run ttyd 1.7.7 with `-W -a -H X-Webauth-User`, bind to a private interface and launch the account's interactive login shell through `deploy/container/terminal-shell` or `deploy/mac/terminal-shell`. Keep default shell plugins enabled. Users must not be able to reach ttyd directly: the trusted-header check alone does not prove a request came from the gateway.

For Linux service management, place the committed release binary in `/opt/shell-gate`, create a dedicated `shellgate` service account, write a root-readable `/etc/shell-gate.env` and install `deploy/shell-gate.service`. Its state directory is `/var/lib/shell-gate`. On macOS use the Mac guide for the terminal upstream; the gateway binary can run directly or under an operator-owned LaunchAgent. Windows gateway binaries use the same environment settings and a separate Unix/container backend. There is no native Windows shell/ConPTY adapter in this release. Optional wake/lease hooks use executable scripts on Unix or native `.exe` programs on Windows; bundled SSH bridge scripts require a Unix gateway.

## Optional providers

External GoTrue: set `SHELL_AUTH_MODE=supabase`, `SHELL_SUPABASE_URL`, `SHELL_SUPABASE_ANON_KEY`, `AUTH_JWT_SECRET` (at least 32 bytes), `AUTH_JWT_ISSUERS` and `AUTH_JWT_AUDIENCE`. This compatibility adapter expects HS256 identity tokens and a provider supporting the SDK's passkey API. No private SDK checkout or CDN is required. Local login does not expose the external token-exchange route.

### Generic OIDC SSO

This optional adapter supports a confidential OIDC web client with Discovery, asymmetric signed ID tokens, S256 PKCE and `client_secret_basic` or `client_secret_post` authentication. It does not implement SAML, public clients or `private_key_jwt`. Local password/passkey authentication remains the default.

1. Register a web client at your provider. Set its exact redirect URI to `https://shell.example.com/api/sso/callback`; for local development use `http://localhost:18765/api/sso/callback`. In multi-edge mode register the apex origin callback, never an individual edge.
2. Set the browser-visible `SHELL_PUBLIC_URL`, `SHELL_ALLOWED_EMAILS` and the following process environment (or `.env` for Compose):

   ```sh
   SHELL_AUTH_MODE=sso
   SHELL_SSO_ISSUER=https://sso.example.com/realms/shellgate
   SHELL_SSO_CLIENT_ID=shell-gate
   SHELL_SSO_CLIENT_SECRET=<provider-issued-secret>
   ```

3. Restart/recreate the gateway and open `/login`. Choose **Sign in with SSO**. The callback performs the code exchange on the server; the secret and provider tokens never enter frontend configuration. Test an allowed account, a denied account, cancellation and logout.

`oidc` is accepted as an alias for `sso`. Copy the issuer exactly from Discovery, including a trailing slash if present. Optional `SHELL_SSO_SCOPES` defaults to `openid email profile`; explicit scopes are preserved with `openid` added when needed. `SHELL_SSO_EMAIL_CLAIM` defaults to `email` and is honored exactly, with no automatic username/UPN aliases. The claim may come from a verified ID token or subject-matched UserInfo. By default that same claim source must contain boolean `email_verified=true`; missing, false or string values fail closed. If an enterprise provider verifies addresses administratively but omits this claim, configure its mapper first or explicitly set `SHELL_SSO_ALLOW_UNVERIFIED_EMAIL=true` only when the provider prevents users from choosing an allowlisted address.

Syntax errors fail startup. Provider reachability is checked when login starts, so a short provider outage does not prevent an existing gateway session from working. Tokens must pass signature, exact issuer, audience, subject, issued-at, expiry, authorized-party and nonce checks. Invalid tokens never fall back to UserInfo. Provider redirects, unsafe endpoints, HTTPS downgrade and oversized responses are rejected. Provider and token response contents are not logged.

The gateway enforces the current email allowlist on login and every session request. Workspace metadata belongs to the normalized email, consistent with other adapters; prevent email reassignment at your provider. SSO does not need local invitations. After login the gateway issues its own cookie with `SHELL_SESSION_TTL` (default `12h`), independently of provider token lifetime. Logging out clears only this cookie, so the next login may reuse an existing provider session. Immediate provider-side logout/revocation is not propagated; remove an email from the gateway allowlist to deny its existing sessions.

Multi-edge operation needs shared PostgreSQL, secure shared-domain session cookies and identical issuer/client/secret/apex settings. The ten-minute login-state cookie is apex-host-only; the completed gateway session is shared with the configured edges. The session key is derived from the client secret plus issuer, client ID and origin; changing any of these invalidates existing SSO sessions. Back up credentials privately; never commit `.env` or print a fully expanded Compose configuration containing the secret.

PostgreSQL: provision an operator-owned database with schema creation privileges, then supply `SHELL_DATABASE_URL`. Use verified TLS across hosts. Existing multi-edge operation requires external authentication, shared PostgreSQL, a cookie domain and configured edge hosts; local ceremonies deliberately reject multi-edge routing. A separate migration import transfers tabs/presets only and never copies local credentials or signing keys.

## Two-factor authentication

Each user can open **Security** (mobile: **More → Account security**) and configure an authenticator. TOTP uses six digits, SHA-1 and a 30-second period, compatible with standard authenticator apps. Setup requires a code before activation. Ten one-use recovery codes are shown only when created; copy or download them into private storage. The setup key and QR are generated locally, never sent to a QR service. Changes require a sign-in within five minutes; sign in again when prompted. Turning 2FA off or creating a new recovery set also requires a current authenticator/recovery code. New recovery codes invalidate the old set.

After activation, password, passkey, SSO and external-token sign-ins all stop at the second step. The pending sign-in expires after five minutes and cannot open terminals or APIs. A successful time step cannot be reused; wait for the next authenticator code if you just used one. Five failed proofs lock account verification for five minutes across instances. Check the server and phone clocks when valid codes are rejected. Password recovery preserves 2FA. Factor changes invalidate other cookies on their next request/handshake; existing terminal connections continue until disconnected.

If the authenticator is lost, choose **Use a recovery code** at sign-in. If both the authenticator and recovery codes are lost, the operator must verify the account owner through a trusted channel, then run with the gateway's usual environment and database settings:

```sh
shell-gate totp-reset --email you@example.com --dry-run --json
shell-gate totp-reset --email you@example.com --json
# Compose: docker compose exec gate shell-gate totp-reset --email you@example.com
```

This audited local command removes only that account's factor and pending sign-ins, invalidates its earlier cookies, and does not recover passwords. The user can sign in with their primary credential and enroll again. There is no browser reset bypass. A missing account factor returns an error; the dry run validates arguments/configuration without writing.

Back up `totp.key` with the database. It is a private, persistent 32-byte encryption key in `SHELL_DATA_DIR`; no manual configuration is needed on a single instance. Initial generation derives from the gateway signing key, then persists independently so later SSO-secret rotation retains enrolled factors. Cluster nodes must share the established key as well as PostgreSQL and session settings; copy that private file before adding a node after credentials have changed. Startup checks existing ciphertext and refuses a mismatched key. Losing both the matching key and its backup makes enrolled seeds unreadable; restore the key rather than delete/regenerate it. Account-bound AES-GCM protects stored seeds; recovery codes are hashed. A compromised running gateway or a database plus its encryption key can still expose factors.

## Backups and upgrades

Stop the gateway before a consistent SQLite backup; copy the complete gateway data volume, including `workspace.db`, `session.key` and `totp.key`, plus the terminal home volume. Keep backups private. Do not delete volumes during an upgrade. PostgreSQL deployments use `pg_dump` in addition to the signing-key/terminal backup. Losing the session key signs everyone out; losing the database loses password hashes, registered passkeys and workspace metadata.

For source/Compose updates, choose the release tag, back up, and run `docker compose up -d --build`. For binary updates, run the installer with the desired `SHELL_GATE_VERSION=v0.3.0`, then restart the service. Schema migrations run at startup; restore a backup for rollback after an incompatible migration. Review release notes before upgrading. Validate `/healthz`, password login (and passkeys/2FA when used), a real terminal command and saved tabs after every deployment.

`docker compose down` keeps volumes. `docker compose down -v` deletes account data and terminal files; use it only for disposable test stacks. Application account separation is not an OS sandbox.
