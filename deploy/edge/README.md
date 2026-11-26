# Optional edge hooks

These scripts use the operator's `SHELL_GATE_RELAY` SSH alias. Configure a dedicated key with a pinned host key and a forced command pointing at `deploy/relay/shell-gate-mac-bridge.sh`. Do not grant a general shell with this key.

Map a machine to these executable paths using `SHELL_WAKE_COMMANDS` and `SHELL_SESSION_LEASE_COMMANDS`. They are entirely optional. See [deployment](../../docs/DEPLOYMENT.md) and [Mac upstream](../../docs/MAC_UPSTREAM_GUIDE.md).

For renewable leases, upgrade the Mac and relay/edge hooks before the gateway. All validators accept only `acquire`, `renew` or `release` plus a validated tab ID. `renew` acts only on an existing lease; it must never become a general SSH command or a wake action.
