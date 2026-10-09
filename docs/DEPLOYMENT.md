# Deployment

## Complete Compose stack

Copy `.env.example` to `.env`, set your allowed email(s), and run `docker compose up -d --build`. The gateway listens only on loopback by default. ttyd has no published host port. Both services run as non-root users with read-only root filesystems, dropped capabilities and persistent data volumes. The terminal home volume is writable; software installation belongs in a derived terminal image.

Run `docker compose exec gate shell-gate invite --email you@example.com` to set up an account at the configured public address. Codes expire after one hour by default, are single-use, and must be shared privately. Enter the code under **Set up or recover an account**, then choose a password of 12 to 128 characters. A passkey can be chosen instead. An invitation proves operator authorization, not email-delivery verification. To recover a password, issue another invitation for the same email; existing local cookies become invalid on their next request or WebSocket handshake. Already open terminal connections continue until disconnected. To add another account, add its email to the allowlist, recreate the gateway and issue an invitation. To deny an account immediately, remove its email and recreate the gateway; existing sessions are checked against the current list on every request.

For a remote host without a domain, keep its port on loopback and use `ssh -L 18765:127.0.0.1:18765 your-server`, then open `http://localhost:18765`. No external authentication service is involved.

## HTTPS

Set `SHELL_DOMAIN=shell.example.com` in `.env`, point its DNS to your server, allow inbound 80/443 and run:

```sh
docker compose -f compose.yaml -f compose.https.yaml up -d --build
docker compose -f compose.yaml -f compose.https.yaml exec gate shell-gate invite --email you@example.com
```

Caddy obtains a certificate and the override sets the gateway's public URL to that exact HTTPS origin. Open `https://shell.example.com/login`. Passkeys for localhost do not migrate to another domain; create an invitation and enroll at the final origin. An existing reverse proxy can forward HTTP and WebSocket upgrades to the loopback port instead. Always set `SHELL_PUBLIC_URL` to the browser-visible origin. For isolated deployments, preload images and use an operator-provided trusted TLS certificate instead of public certificate issuance.

## Configuration

| Setting | Default / behavior |
| --- | --- |
| `SHELL_ALLOWED_EMAILS` | Required comma-separated emails; no author account or wildcard default. |
| `SHELL_PUBLIC_URL` | `http://localhost:18765`; exact origin, HTTPS outside localhost. |
| `SHELL_AUTH_MODE` | `local`; legacy external settings select `supabase` for compatibility. |
| `SHELL_LISTEN` | `127.0.0.1:18765`; constrain exposure with host/firewall or Compose bind address. |
| `SHELL_TTYD_URL` | `http://127.0.0.1:7681`; private HTTP(S) URL without embedded credentials or query. |
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

Compose exposes its documented settings in `.env`; advanced upstream/provider settings require editing an override's `environment`. The program itself reads the process environment, not `.env` files. Invalid settings fail startup. `shell-gate doctor --json` validates configuration without contacting an upstream or writing data; `invite --dry-run --json` validates an invitation without creating it. CLI JSON uses `{ok,data}` on success and `{ok:false,error:{code,message}}` on failure; exit 0 means success, 1 failure. Invitation output intentionally contains a secret; never send it to application logs.

## Direct binary and service

Install using the README command or download a checksummed release. On the upstream host, run ttyd 1.7.7 with `-W -a -H X-Webauth-User`, bind to a private interface and launch the account's interactive login shell through `deploy/container/terminal-shell` or `deploy/mac/terminal-shell`. Keep default shell plugins enabled. Users must not be able to reach ttyd directly: the trusted-header check alone does not prove a request came from the gateway.

For Linux service management, place the committed release binary in `/opt/shell-gate`, create a dedicated `shellgate` service account, write a root-readable `/etc/shell-gate.env` and install `deploy/shell-gate.service`. Its state directory is `/var/lib/shell-gate`. On macOS use the Mac guide for the terminal upstream; the gateway binary can run directly or under an operator-owned LaunchAgent. Windows gateway binaries use the same environment settings and a separate Unix/container backend. There is no native Windows shell/ConPTY adapter in this release.

## Optional providers

External GoTrue: set `SHELL_AUTH_MODE=supabase`, `SHELL_SUPABASE_URL`, `SHELL_SUPABASE_ANON_KEY`, `AUTH_JWT_SECRET` (at least 32 bytes), `AUTH_JWT_ISSUERS` and `AUTH_JWT_AUDIENCE`. This compatibility adapter expects HS256 identity tokens and a provider supporting the SDK's passkey API. No private SDK checkout or CDN is required. Local login does not expose the external token-exchange route.

PostgreSQL: provision an operator-owned database with schema creation privileges, then supply `SHELL_DATABASE_URL`. Use verified TLS across hosts. Existing multi-edge operation requires external authentication, shared PostgreSQL, a cookie domain and configured edge hosts; local ceremonies deliberately reject multi-edge routing. A separate migration import transfers tabs/presets only and never copies local credentials or signing keys.

## Backups and upgrades

Stop the gateway before a consistent SQLite backup; copy the complete gateway data volume, including `workspace.db` and `session.key`, plus the terminal home volume. Keep backups private. Do not delete volumes during an upgrade. PostgreSQL deployments use `pg_dump` in addition to the signing-key/terminal backup. Losing the session key signs everyone out; losing the database loses password hashes, registered passkeys and workspace metadata.

For source/Compose updates, choose the release tag, back up, and run `docker compose up -d --build`. For binary updates, run the installer with the desired `SHELL_GATE_VERSION=v0.1.0`, then restart the service. Schema migrations run at startup; restore a backup for rollback after an incompatible migration. Review release notes before upgrading. Validate `/healthz`, password login (and passkeys when used), a real terminal command and saved tabs after every deployment.

`docker compose down` keeps volumes. `docker compose down -v` deletes account data and terminal files; use it only for disposable test stacks. Application account separation is not an OS sandbox.
