# shell-gate

Self-hosted browser terminal gateway. Read documents whose described contents are relevant before making decisions or changes. Long-lived product knowledge belongs in `docs/`; do not store credentials, personal machine identifiers or private infrastructure in repository files.

## Documents

- [README.md](README.md): Public quick start and platform support.
- [README.zh-CN.md](README.zh-CN.md): Complete Simplified Chinese quick start.
- [docs/DEPLOYMENT.md](docs/DEPLOYMENT.md): Container, HTTPS, binary, SSO/2FA setup, account recovery, backups and upgrades.
- [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md): Adapter boundaries, account isolation, OIDC SSO and self-service TOTP security.
- [docs/PRODUCT_BEHAVIOR.md](docs/PRODUCT_BEHAVIOR.md): Established terminal, clipboard, renderer and mobile interaction behavior.
- [docs/TERMINAL_PROXY_KNOWLEDGE_BASE.md](docs/TERMINAL_PROXY_KNOWLEDGE_BASE.md): Multi-machine registry, ttyd reverse proxy, login-shell command wrapping and upstream hardening.
- [docs/TAB_PERSISTENCE_KNOWLEDGE_BASE.md](docs/TAB_PERSISTENCE_KNOWLEDGE_BASE.md): Account-owned tab directory, machine resolution, per-machine active-tab restore and reconnect semantics.
- [docs/WORKSPACE_FRONTEND_KNOWLEDGE_BASE.md](docs/WORKSPACE_FRONTEND_KNOWLEDGE_BASE.md): Workspace shell and enhance.js modules: tight fit, theme broadcast, keyboard ownership, clipboard and reconnect guards.
- [docs/MAC_WAKE_LEASE_KNOWLEDGE_BASE.md](docs/MAC_WAKE_LEASE_KNOWLEDGE_BASE.md): Mac on-demand wake, at-home suppression, sleep leases and compressed ttyd build.
- [docs/design/standalone-open-source.md](docs/design/standalone-open-source.md): Standalone requirements, references and release acceptance.
- [docs/design/frontend-redesign.md](docs/design/frontend-redesign.md): Visual direction and compact workspace layout.
- [docs/design/2026-08-24-multi-upstream.md](docs/design/2026-08-24-multi-upstream.md): Machine selection and per-machine tab restoration.
- [docs/design/2026-09-07-tab-server-persistence.md](docs/design/2026-09-07-tab-server-persistence.md): Account-owned persistent tabs and reconnect semantics.
- [docs/design/2026-09-08-dual-edge-shared-store.md](docs/design/2026-09-08-dual-edge-shared-store.md): Optional multi-edge routing and shared PostgreSQL.
- [docs/design/2026-09-09-wake-suppress-when-home.md](docs/design/2026-09-09-wake-suppress-when-home.md): Optional presence adapter and wake suppression.
- [docs/MAC_UPSTREAM_GUIDE.md](docs/MAC_UPSTREAM_GUIDE.md): Mac terminal build, LaunchAgent, firewall and optional wake/lease setup.
- [docs/troubleshooting/TROUBLESHOOTING_INDEX.md](docs/troubleshooting/TROUBLESHOOTING_INDEX.md): Verified terminal and transport troubleshooting, including Mac wake with home-LAN ARP checks.
- [SECURITY.md](SECURITY.md): Threat model, dependency assessment and private reporting.
- [CONTRIBUTING.md](CONTRIBUTING.md): Development and release checks.
- [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md): Dependency licensing and embedded SDK rebuild.
- [CHANGELOG.md](CHANGELOG.md): Bilingual release notes.
- [TASKBOARD.md](TASKBOARD.md): Concurrent work ownership.

## Engineering

- Work on the default branch and preserve existing workspace changes.
- Keep local account/password login, optional passkeys and SQLite usable without an external service or neighboring checkout. Optional providers must be explicit and fail closed for authentication.
- Account ownership applies to tabs, presets and diagnostics. Shared ttyd OS accounts are not per-user sandboxes.
- User mutations and their actor/action/target/time audit records commit in one transaction. Storage errors never mean an empty workspace.
- Keep the server-provided machine directory, remembered tabs, per-machine active selection, interactive login shell, canvas renderer and mobile keyboard ownership contracts.
- Private upstream transport never uses environment proxies. Strip browser identity headers and gateway credentials before forwarding. Reject unsafe URLs and cross-site WebSockets.
- Use Go 1.26.10 or newer. Run full race tests, vet, lint, vulnerability and privacy checks. Real terminal changes require a ttyd handshake and input/output acceptance.
- Build release packages with `python3 scripts/release.py --version VERSION` from a clean committed tree. Public exports must never include the earlier private history.
- New long-lived documents require a descriptive navigation entry here or in the troubleshooting index. Root `CLAUDE.md` redirects to this file.
