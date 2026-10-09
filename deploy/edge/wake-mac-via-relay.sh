#!/usr/bin/env bash
# Uses an operator-defined SSH alias with a forced-command key and pinned host key.
set -euo pipefail
[[ "$#" -eq 0 ]] || { echo 'Usage: wake-mac-via-relay.sh' >&2; exit 64; }
relay="${SHELL_GATE_RELAY:?Set SHELL_GATE_RELAY to your restricted SSH alias}"
exec /usr/bin/ssh -T -o BatchMode=yes -o ConnectTimeout=8 -o StrictHostKeyChecking=yes "$relay"
