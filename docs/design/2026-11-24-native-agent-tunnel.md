# Native HTTPS/WSS Reverse Tunnel (`shell-gate agent`)

## Context & Problem

In dual-edge deployments where upstream machines sit behind residential NATs, relying solely on a UDP-based overlay network (such as ZeroTier) exposes terminal proxying, health probes, home-presence checks, wake commands, and session-lease hooks to ISP/carrier UDP blackholes or heavy throttling. Observed on 2026-10-21, 2026-11-19, 2026-11-21, and 2026-11-24 (19:55–20:03 CST): both cloud edges lost overlay connectivity to home hosts simultaneously while direct TCP to HTTPS `:443` and SSH remained healthy at ~30–40 ms.

## Design Goals

1. **Pure TCP 443 transport**: Upstream hosts run `shell-gate agent`, establishing outbound HTTPS/WSS (`wss://.../api/agent/tunnel?name=<agent>`) connections through the gateway's existing reverse proxy (e.g., Caddy on `:443`) with zero new public ports and zero UDP dependency.
2. **Full-path coverage**: Support terminal HTTP/WebSocket proxying (`ttyd`), optional home presence HTTP checks (`presence`), on-demand host wake hooks (`wake`), and per-tab sleep lease hooks (`lease`) over the multiplexed tunnel.
3. **Zero keep-awake side effects**: `shell-gate agent` holds no `caffeinate` or OS sleep assertions; Macs continue sleeping normally and only `shell-gate-session-lease` manages sleep assertions per open tab. A wall-clock jump detector triggers rapid reconnection (~1–2 s) when the host wakes from sleep.
4. **Backward compatibility & automatic fallback**: Existing `http://`/`https://` targets and shell hooks remain valid. When both an agent tunnel and direct HTTP/hook settings are configured for a machine, ShellGate prefers the active tunnel and automatically falls back to direct transport when the agent is offline. Pure-NAT deployments without an overlay network can use `agent://<name>` target URLs directly.

## Protocol & Transport

- **Transport stack**: `github.com/coder/websocket` (ISC) wrapped with `websocket.NetConn` (bound to a tunnel-lifetime context, never an HTTP request timeout) + `github.com/hashicorp/yamux` (MPL-2.0) stream multiplexing.
- **Direction**: Agent initiates outbound WebSocket to each configured gateway (`yamux.Server` on agent, `yamux.Client` on gateway) so the gateway opens streams back toward the agent behind NAT.
- **Framing & limits**:
  - WebSocket compression is disabled on the tunnel carrier (`websocket.CompressionDisabled`) so inner `ttyd` `permessage-deflate` traffic is not double-compressed.
  - WebSocket read limit is raised to 4 MiB (`SetReadLimit(4 << 20)`).
  - Yamux keepalive runs every 10 s (`EnableKeepAlive: true`, `KeepAliveInterval: 10s`, `ConnectionWriteTimeout: 10s`, `MaxStreamWindowSize: 512 KiB`).
  - Per-stream control header: one `\n`-terminated JSON line (`io.LimitReader` bounded to 4096 bytes) before raw byte forwarding:
    - Request: `{"type":"dial"|"wake"|"lease","service":"ttyd"|"presence","machine":"...","action":"acquire"|"renew"|"release","tabId":"<32-hex>"}`
    - Response: `{"ok":true|false,"error":"..."}`
  - Deadlines for stream setup are set only on individual `*yamux.Stream` objects, never on the underlying `websocket.NetConn` (since `coder/websocket` closes the underlying WebSocket if a `NetConn` read/write deadline expires).

## Security Boundaries

- **Authentication**: Gateway authenticates `GET /api/agent/tunnel?name=<agent>` via `Authorization: Bearer <token>` compared in constant time (`subtle.ConstantTimeCompare` over SHA-256 digests) against `SHELL_AGENT_TOKENS`. Tokens must be ≥32 bytes without whitespace or control characters. Cookies are never accepted for agent tunnels; tokens are never passed in URL query strings.
- **CSRF middleware boundary**: `/api/agent/tunnel` is exempted from browser `Origin`/`Sec-Fetch-Site` checks in `protectRequests` because it does not use cookies, while still enforcing `Host` validation.
- **No arbitrary SSRF**: `"dial"` only accepts fixed service identifiers (`"ttyd"` or `"presence"`), mapped on the agent solely to locally configured loopback/private addresses (`--ttyd` / `--presence`).
- **No shell execution**: `"wake"` and `"lease"` look up fixed absolute executable paths in `--wake-hooks` / `--lease-hooks` and execute them via `exec.CommandContext` with strict argument validation (`wake`: 0 args; `lease`: `acquire|renew|release` + 32-char lowercase hex `tabId`).
- **No environment proxies**: Both gateway upstreams and `shell-gate agent` disable `ProxyFromEnvironment` (`Proxy: nil`) unless explicitly overridden.
- **Secret file permissions**: Agents read tokens via `--token-file` / `SHELL_AGENT_TOKEN_FILE` (mode `0600`, or `SHELL_AGENT_TOKEN` env fallback for containers).

## Configuration

### Gateway (`shell-gate serve`)

| Variable | Description |
|---|---|
| `SHELL_AGENT_TOKENS` | Comma-separated `agent=token` pairs (agent name 1–16 chars `[a-zA-Z0-9_-]`, token ≥32 chars). Enables `/api/agent/tunnel`. |
| `SHELL_TTYD_TARGETS` | Accepts `http(s)://...` (with automatic tunnel preference if a matching agent token exists) or `agent://<name>` (tunnel-only, requires matching entry in `SHELL_AGENT_TOKENS`). |
| `SHELL_WAKE_AGENT_ROUTES` | Optional comma-separated `machine=agent` mapping. When connected, wake executes through `agent` first and falls back to `SHELL_WAKE_COMMANDS` if configured. |
| `SHELL_LEASE_AGENT_ROUTES` | Optional comma-separated `machine=agent` mapping. When connected, lease `acquire`/`renew`/`release` executes through `agent` first and falls back to `SHELL_SESSION_LEASE_COMMANDS`. |
| `SHELL_WAKE_HOME_PRESENCE_AGENT` | Optional agent name for `presence` HTTP dialing before falling back to direct `SHELL_WAKE_HOME_PRESENCE_URL`. |

### Agent (`shell-gate agent`)

Flags / environment variables (`SHELL_AGENT_*`):
- `--name` / `SHELL_AGENT_NAME`: Agent identity (matches gateway `SHELL_AGENT_TOKENS` key).
- `--gateways` / `SHELL_AGENT_GATEWAYS`: Comma-separated gateway base URLs (`https://...` or `wss://...`).
- `--token-file` / `SHELL_AGENT_TOKEN_FILE` (or `SHELL_AGENT_TOKEN`): Bearer token.
- `--ttyd` / `SHELL_AGENT_TTYD`: Local `ttyd` `host:port` (or `http://host:port`).
- `--presence` / `SHELL_AGENT_PRESENCE`: Optional local presence `host:port` (or `http://host:port`).
- `--wake-hooks` / `SHELL_AGENT_WAKE_HOOKS`: Optional `machine=/abs/executable` map.
- `--lease-hooks` / `SHELL_AGENT_LEASE_HOOKS`: Optional `machine=/abs/executable` map.

## Observability & Acceptance

- `GET /api/machines` includes `"transport": "tunnel" | "direct"` per upstream machine.
- Acceptance requires unit/race tests, `go vet`, `golangci-lint`, `govulncheck`, `scripts/check-public.py`, and real `ttyd` compressed WebSocket (`101` + `permessage-deflate`) I/O round-trip over the agent tunnel on every configured edge.
