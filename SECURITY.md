# Security

ShellGate grants access to a real shell. Only invite people who are trusted to use the configured terminal account. Account-owned tabs and presets are isolated by the gateway, but ttyd processes share the upstream OS account. All allowed accounts can access every configured machine; there is no per-account machine access policy. When groups must not share files, processes or privileges, deploy separate gateway + ttyd stacks with distinct allowlists and domains, backed by separate OS accounts, containers or machines.

Local account/password sign-in is the default; passkeys are optional. All remote login requires a trusted HTTPS origin, with localhost as the development exception. Registration requires an operator-created, expiring, one-use invitation for an allowed email. The invitation stores only a hash. WebAuthn validates origin, RP ID, challenge and user verification; sessions use a separate persistent signing key, an expiry and HttpOnly cookies. Restarting during an unfinished passkey ceremony requires starting that ceremony again. Local ceremonies support one gateway instance; use the external identity adapter for multiple edges.

Passwords are stored as salted Argon2id hashes (19 MiB, two iterations, one lane), using constant-time comparisons. Passwords accept 12–128 characters without composition rules. Login errors do not disclose account existence. Password endpoints admit at most 30 requests per minute per socket peer and two concurrent hash operations per instance; behind a reverse proxy, peers may share that limit. Recovery requires a fresh operator invitation and changes the credential version, invalidating earlier cookies on their next request or WebSocket handshake. It does not forcibly terminate an already open terminal connection. There is no email delivery or public self-registration dependency.

Keep ttyd unreachable except from the gateway. `X-Webauth-User` authenticates the gateway's request; it does not create or select an OS user. Browser-supplied authentication headers are stripped. Cookies and bearer credentials are removed before upstream forwarding. Proxy transports do not use environment proxies. Cross-site writes and WebSockets are rejected, request bodies are bounded and framing the workspace is prohibited.

Back up the database and session key as sensitive data. On Unix, the directory uses mode 0700 and sensitive files mode 0600. Windows operators must restrict the data directory ACL to the service account; Unix mode bits are not a Windows access-control guarantee. Password hashes, public keys, emails, tab commands and audit metadata reside in the database; terminal screen contents are not persisted by ShellGate. Browser diagnostics are disabled by default. Enabling them can collect browser URLs and diagnostic metadata; protect and rotate that data. Do not enable diagnostics on a public demo with real accounts.

Audit records contain actor, action, target and time and commit with user mutations. They are append-only through the application, not tamper-proof against a database administrator. SQLite is the standalone default. PostgreSQL is optional; use a private service network or verified TLS when crossing a host boundary.

The initial public history is exported from a reviewed source tree without importing earlier private commits. Secret scans are preventive checks, not proof that a credential was never exposed. Rotate any real secret that was previously committed to a private or public repository.

## Dependency assessment

Go 1.26.9 fixes the reachable standard-library findings discovered in the original toolchain. The release uses that version as its minimum. `govulncheck -show verbose ./...` reports GO-2026-5932 in the required `golang.org/x/crypto` module: its unmaintained OpenPGP packages have no fixed version. ShellGate imports none of those packages and has no OpenPGP feature; package and symbol scans are clean. Retain the module for WebAuthn's supported cryptography and recheck each release. See [the official advisory](https://pkg.go.dev/vuln/GO-2026-5932).

## Reporting

Please do not put credentials or exploit details in a public issue. Use private vulnerability reporting where available, or contact the repository maintainer privately. Supported security fixes apply to the latest release.
