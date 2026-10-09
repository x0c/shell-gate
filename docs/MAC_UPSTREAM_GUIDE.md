# Mac terminal upstream

A Mac is an optional ttyd upstream, not a required part of ShellGate. Keep the terminal private and preserve the account's zsh/bash login environment, prompt and line-editing plugins. The Mac wrapper resolves the account shell and supplies true-color support.

## Install

From the repository on macOS, run `bash deploy/mac/install-mac-upstream.sh`. It builds the pinned ttyd 1.7.7 + libwebsockets 4.3.10 combination with WebSocket compression, renders a user-specific LaunchAgent and starts it. Homebrew, build tools and Python 3 are installation dependencies; they are not remote runtime services. Binding defaults to `127.0.0.1:7681`. `TTYD_ADDR` may select an operator-controlled private interface. Restrict that interface to your gateway before exposing it. The installer does not alter SSH or firewall policy.

`--no-start` renders the scripts and LaunchAgent without building or starting a service. The plist template is not installable directly: launchd does not expand home variables in paths. The installer uses plist serialization to safely write real local paths, including homes containing spaces. Generated files stay on the operator's machine and must not be committed.

The LaunchAgent supervises clean and abnormal exits. The wrapper waits for the chosen interface and yields to an already occupied port. Logs, cache and leases live under `~/.config/shell-gate` or `XDG_CONFIG_HOME`; executable tooling lives under `~/.local/bin`. Review `launchctl print gui/$(id -u)/io.shellgate.ttyd`, the private port and a real gateway terminal command after installation.

## Compression and security

Do not replace the pinned build with Homebrew libwebsockets 5.0: its default build omits extensions, while enabling them reproduced compressed-paste disconnects. Keep both directions tested using `deploy/mac/check-ttyd-compression.py`. Do not reduce output refresh rate or merge terminal frames to hide transport problems. See [compression troubleshooting](troubleshooting/2026-09-10-mac-corral-line-by-line-refresh.md).

The trusted identity header is not an independent network authentication mechanism. Do not publish ttyd on the internet. Review `deploy/mac/pf-ttyd-restrict.conf` as an example, substitute your own addresses, validate it, then apply it explicitly with `enable-remote-and-pf.sh YOUR_CONFIG`. This changes only the supplied anchor; it does not infer topology or replace SSH access rules. `harden-upstream-security.sh --key-access-verified` is a separate operator action that disables password SSH after a working key login has been verified.

## Optional wake and sleep leases

Map `SHELL_WAKE_COMMANDS` and `SHELL_SESSION_LEASE_COMMANDS` to executable scripts for that machine. Only explicit user actions may wake a sleeping host. Background monitoring and hidden reconnects cannot wake it. A tab lease blocks system sleep, survives refresh/background/temporary network loss and is released on explicit tab close. Abnormal browser departure expires the lease within eight hours; never hold a permanent caffeinate assertion for the whole ttyd service.

`deploy/edge/` uses an operator-configured restricted SSH alias (`SHELL_GATE_RELAY`). `deploy/relay/shell-gate-mac-bridge.sh` accepts only empty wake requests or validated lease actions and 32-character lowercase hex tab IDs. Configure `SHELL_GATE_MAC_HOST` and `SHELL_GATE_MAC_LEASE_PATH` on the relay. The wake script requires `SHELL_GATE_WAKE_MAC`, `SHELL_GATE_WAKE_BROADCAST` and `SHELL_GATE_WAKE_SSH_HOST`; it sends a magic packet and optionally nudges the Mac's SSH service. The key must have a forced command, no port forwarding and a pinned host key. Generic shell access through this key is prohibited.

An optional presence adapter can suppress wake while `comfort_home` is true. Its failure permits wake for compatibility; this is not an authentication boundary. See [presence design](design/2026-09-09-wake-suppress-when-home.md) and [product behavior](PRODUCT_BEHAVIOR.md).
