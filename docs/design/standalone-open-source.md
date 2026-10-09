# Standalone open-source deployment

## Requirements

ShellGate must be deployable by another operator without the author's authentication service, databases, domains, networks, neighboring source checkout, or browser CDN. The default deployment hosts account/password authentication and optional passkeys locally, persists account data in SQLite, and proxies a private ttyd upstream. PostgreSQL and external GoTrue authentication remain optional for existing multi-edge deployments. First enrollment requires an operator-generated secret; no public registration or authentication bypass is allowed. Account data stays isolated and mutations record an actor, action, target, and timestamp in the same transaction.

Configuration must have safe defaults, reject invalid security settings, and keep credentials out of public files and errors. Session signing keys persist in private operator storage and must be distinct from external login tokens. Default launchers must not require author-specific programs. Mobile keyboard ownership, canvas rendering, terminal login-shell behavior, persistent tabs, and per-machine selection remain unchanged.

The default runnable bundle includes ttyd and requires no remote service at runtime. HTTPS is required for remote authentication; localhost is the development exception. Downloads and certificate issuance require connectivity during installation. Local certificates and preloaded container images allow isolated operation. Users of a shared ttyd OS account have that account's OS privileges: account-isolated tab metadata is not OS sandboxing.

Public distribution must contain no personal infrastructure, emails, usernames, private addresses, secrets, local files, or unreviewed historical content. The private Git history must not be pushed to a public remote. Release artifacts derive from a committed and scanned source snapshot.

Container deployment is optional: ship Docker Compose for the complete gateway + ttyd stack, with an optional HTTPS entry. Keep a direct binary deployment route and identical authentication, storage, and security behavior across both routes.

Adapters separate token verification, durable workspace storage, terminal proxy transport, and optional wake/lease hooks. Keep interfaces capability-specific; provider selection is explicit and invalid configuration never falls back to unauthenticated access. Document extension contracts and acceptance before implementing new providers.

## References and adaptation

- https://github.com/go-webauthn/webauthn — use the maintained verification library and its session/user interfaces; do not write cryptographic attestation validation.
- https://developers.google.com/identity/passkeys/developer-guides/server-registration — enforce challenge, origin, relying-party identity, and user verification. Enrollment authorization is owned by ShellGate.
- https://go.dev/pkg/net/http/httputil/ — retain the standard reverse proxy and Rewrite API; strip browser-supplied authorization headers and enforce WebSocket origin.
- https://github.com/tsl0922/ttyd — reuse the upstream terminal and auth-proxy contract, not its unauthenticated public exposure.
- https://docs.docker.com/compose/how-tos/use-secrets/ — keep deployment secrets in operator-owned files; no checked-in credentials.
- https://github.com/nirui/sshwifty — adopt README screenshots and short quick-start structure, not its SSH credentials model or frontend architecture.

Workspace storage errors must surface as failures, never as an empty tab list. A failed close keeps the tab visible so the user can retry. Legacy imports are opt-in, atomic, and audited. Session shutdown must close live proxy connections before closing storage.

Container listeners bind the service network inside the container; host exposure remains loopback unless explicitly configured. Direct binaries retain a loopback default. Public UI copy defaults to English while preserving existing terminal interaction behavior.

## Release readiness

Record verification coverage and any unresolved limits here before release. Do not claim an operating-system sandbox or transparent process continuation for plain shell tabs.

A browser-cancelled proxy request is normal lifecycle activity: do not log it as an upstream outage or increment proxy-failure metrics. Release binaries omit private VCS metadata; release provenance identifies the reviewed public source tree.

Local account/password sign-in is the primary public login route; passkeys remain optional. Password accounts use the same invitation and email allowlist, identity, ownership and session rules. Passwords use salted Argon2id hashes, never plaintext or reversible encryption. Bound password work and login request rates, use generic authentication failures and constant-time hash comparisons. An operator invitation may set/recover a password without requiring a passkey or external service. Verify password enrollment, wrong-password rejection, sign-in, persisted credentials and coexistence with passkeys through the real browser path.

Password recovery invalidates previously issued local cookies on subsequent requests and WebSocket handshakes. Bind a password login to the credential version actually verified so a concurrent recovery cannot upgrade an old-password login to a new valid session. Existing terminal connections are not forcibly terminated by password recovery.

The gateway data directory is operator-owned private storage: enforce owner-only directory permissions on Unix, including existing Compose volumes; database sidecars and session keys remain owner-readable only.

Account setup/recovery displays one active form at a time. Hide the sign-in controls while setup is expanded, and provide a direct return to sign-in, so mobile users do not scroll past an unrelated login form.

Password implementation references: [OWASP password storage](https://cheatsheetseries.owasp.org/cheatsheets/Password_Storage_Cheat_Sheet.html) and [Go Argon2](https://pkg.go.dev/golang.org/x/crypto/argon2). CI uses immutable revisions of the official [checkout](https://github.com/actions/checkout/releases) and [setup-go](https://github.com/actions/setup-go/releases) actions; review dependency updates without changing runtime provider requirements.

## Verified release coverage

- Go 1.26.9: complete race suite, vet and static analysis passed with zero compiler/lint warnings. SQLite and PostgreSQL 18 both passed credential, invitation, recovery and audit-rollback contracts.
- An isolated Linux amd64 Compose stack ran both services as non-root, with no published ttyd port. Actual Chrome flows passed password enrollment, incorrect-password rejection, recovery/revocation, optional passkey enrollment/sign-in, account isolation, saved tabs, restart persistence, cross-origin rejection, one-shot launchers and terminal input/output. Browser requests stayed on the gateway origin; no external identity/CDN was used.
- Desktop and mobile-size screenshots were inspected. Mobile layout and shortcut geometry were checked in Chrome emulation; this does not verify a physical iOS/Android keyboard. Native Mac window capture lacked screen-recording permission, so visual acceptance used browser-rendered captures.
- Retained diagnostic: Chrome blocks multiple beforeunload dialogs from several ttyd iframes during a single navigation. This originates in ttyd 1.7.7's terminal leave-warning handler; no application JavaScript exception or lost tab metadata occurred. Keep the warning behavior rather than suppressing terminal leave protection; review it with a future upstream upgrade. An intentional fixture deletion produced one expected 404 reconnect on the deleted tab, not an upstream outage.
- Dependency assessment: no reachable or imported-package vulnerabilities. The unused, unmaintained OpenPGP package in x/crypto is individually assessed in SECURITY.md.
- HTTPS Compose configuration was validated, but public certificate issuance was not tested. macOS/Linux/Windows amd64/arm64 release builds are required; physical Windows/arm64 runtime and physical mobile keyboard acceptance remain separate coverage limits.

Container package-install notices were assessed individually: missing apt-utils defers debconf preconfiguration to package setup (all dpkg setup steps completed); the Dialog frontend is unavailable without TERM; the Readline frontend is unavailable without its optional Perl module. Debian falls back to Teletype successfully in the minimal build image. These are build-time frontend notices, not Go/compiler warnings or runtime failures. Retain the minimal package set; container health and installed certificate/curl behavior were checked. No extra UI packages are needed by the runtime.

Public-tree privacy and gitleaks scans found no private home paths, private network addresses or credential leaks. The additional leak gate reported 11 non-blocking entropy matches: ten documentation path strings (two knowledge-base links and eight troubleshooting references, including one repeated path) and one Mac build-script path variable. Each was inspected as a repository path, not credential material; no broad scanner exemption was added. Earlier private history is excluded from the public export.

The terminal runtime must not inherit an OS base beyond standard security maintenance. The upstream ttyd 1.7.7 image uses Ubuntu 20.04, whose [standard support ended in May 2025](https://ubuntu.com/20-04). Inspection of its Dockerfile and `ldd` confirmed the shipped ttyd executable is static. Copy that pinned upstream executable into the maintained Debian runtime; do not copy Ubuntu packages or change the terminal/frontend version. Recheck the real terminal path after this runtime change.

Cross-platform source checkouts retain LF line endings through [Git attributes](https://git-scm.com/docs/gitattributes); tests use host-native absolute paths. Unix executable hooks require execute bits, while Windows hooks require an existing native .exe file (Windows has no Unix execute-bit contract). Public API errors use English, and storage failures never return raw driver messages or internal connection details to a browser.

CI matrix failures do not cancel checks on the other supported operating systems; all platform results remain available for diagnosis.

Release-stage Windows CI exposed two LF-sensitive source assertions and a Unix-only path fixture. LF checkout attributes and native absolute-path/executable validation now cover both wake and lease hooks. Local complete race, vet and lint checks passed after the fixes. Storage-failure tests also confirm that preset create/update return a generic service error while validation remains a distinct client error.

A zero elapsed TCP connection interval is a valid measurement at clock resolution. Keep that sample when both timestamps exist; reject only missing or reversed timestamps. Windows CI intermittently exposed the previous strict-positive interval filter through a missing sample count. Preserve the count assertion and fix collection rather than weakening the test.

Release archive metadata must not reveal the build account. Tar members use empty owner/group names and numeric zero IDs; tar/gzip/zip timestamps derive from the reviewed source commit. Inspect archive headers as well as text before publishing. Earlier draft packages with filesystem owner metadata are not public releases and must not be published.
