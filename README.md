**Languages:** English | [简体中文](README.zh-CN.md)

# shell-gate

Open your servers' terminals in a browser, sign in with your email and password (or a passkey), and return to your saved tabs. ShellGate is a self-hosted Go gateway for ttyd with local authentication, SQLite storage, multiple terminal hosts and a compact mobile workspace.

![ShellGate terminal workspace](docs/assets/workspace.jpg)

## Install

**Complete stack with Docker Compose** — no external identity provider, database service or browser CDN:

```sh
git clone https://github.com/x0c/shell-gate.git && cd shell-gate
cp .env.example .env  # Set SHELL_ALLOWED_EMAILS to your own email.
docker compose up -d --build
```

Create your first invitation:

```sh
docker compose exec gate shell-gate invite --email you@example.com
```

Open **http://localhost:18765/login**, select **Set up or recover an account**, and enter your email, invitation code and a password of at least 12 characters. Later visits use **Sign in**. Passkeys are optional. For another server, add [HTTPS](docs/DEPLOYMENT.md#https) or use an SSH tunnel to localhost. Passkeys bind to the chosen domain; keep that address stable.

**Gateway binary** — use an existing private ttyd upstream. Install without Docker:

macOS / Linux:

```sh
curl -fsSL https://raw.githubusercontent.com/x0c/shell-gate/main/scripts/install.sh | bash
```

Windows (PowerShell):

```powershell
irm https://raw.githubusercontent.com/x0c/shell-gate/main/scripts/install.ps1 | iex
```

First use on macOS / Linux:

```sh
SHELL_ALLOWED_EMAILS=you@example.com SHELL_TTYD_URL=http://127.0.0.1:7681 shell-gate serve
```

First use on Windows:

```powershell
$env:SHELL_ALLOWED_EMAILS='you@example.com'; $env:SHELL_TTYD_URL='http://127.0.0.1:7681'; shell-gate serve
```

Run `shell-gate invite --email you@example.com` with the same settings in another terminal. Add the installer's reported directory to PATH if needed. [Release downloads](https://github.com/x0c/shell-gate/releases/latest) include checksums and binaries for amd64 and arm64 on all three platforms. Windows runs the gateway; its terminal backend is a Unix host or the Compose container.

## Features

- Local email/password login, optional passkeys and one-use invitations; explicit email allowlist.
- Account-owned tabs and launchers saved across refreshes and browser sign-ins.
- Multiple ttyd hosts, per-machine tabs and remembered active selection.
- Compact desktop tabs, touch scrolling and mobile terminal shortcut keys.
- Runtime light/dark themes, clipboard bridges and terminal screenshots.
- Optional PostgreSQL, external GoTrue authentication and wake/lease hooks.
- Embedded browser assets and optional diagnostics, disabled by default.

The default stack needs no remote service at runtime. Initial downloads require internet access; public HTTPS certificate issuance also does. Preload images and provide a browser-trusted certificate for isolated operation. ShellGate has no paid service dependency; your server, domain and provider choices may have their own costs.

## Deploy and configure

See [deployment](docs/DEPLOYMENT.md) for HTTPS, multiple hosts, direct services, backups and upgrades; [.env.example](.env.example) for initial settings; and [architecture](docs/ARCHITECTURE.md) for adapters and extension contracts. Build locally with Go 1.26.9+ using `go build ./cmd/shell-gate`.

**Trust boundary:** invited people receive the configured terminal account's OS privileges. Gateway account isolation protects tabs and presets; it does not isolate files or processes within a shared OS account. Keep ttyd private and use separate accounts/containers/hosts when users require isolation. See [security](SECURITY.md).

Plain shell tabs restore their launch definition after reconnect, rather than the exact old shell process. Programs with their own session recovery can reconnect naturally.

## Contribute

Read [contributing](CONTRIBUTING.md). Local checks and release packaging work independently of CI. Source is [MIT licensed](LICENSE); bundled and container dependencies retain their [own licenses](THIRD_PARTY_NOTICES.md).
