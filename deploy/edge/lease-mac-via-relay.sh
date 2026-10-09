#!/usr/bin/env bash
# Uses an operator-defined SSH alias with a forced-command key and pinned host key.
set -euo pipefail
[[ "$#" -eq 2 && "$1" =~ ^(acquire|release)$ && "$2" =~ ^[0-9a-f]{32}$ ]] || { echo 'Usage: lease-mac-via-relay.sh acquire|release TAB_ID' >&2; exit 64; }
relay="${SHELL_GATE_RELAY:?Set SHELL_GATE_RELAY to your restricted SSH alias}"
exec /usr/bin/ssh -T -o BatchMode=yes -o ConnectTimeout=8 -o StrictHostKeyChecking=yes "$relay" lease "$1" "$2"
