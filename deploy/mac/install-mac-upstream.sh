#!/usr/bin/env bash
# Install only this user's terminal LaunchAgent. Firewall/SSH changes are separate.
set -euo pipefail
scripts="$(cd "$(dirname "$0")" && pwd)"
user_home="${SHELL_GATE_HOME:-${HOME}}"
data_dir="${XDG_CONFIG_HOME:-${user_home}/.config}/shell-gate"
address="${TTYD_ADDR:-127.0.0.1}"
no_start=false
[[ "$#" -eq 0 ]] || { [[ "$#" -eq 1 && "$1" == --no-start ]] && no_start=true; } || { echo 'Usage: install-mac-upstream.sh [--no-start]' >&2; exit 64; }
mkdir -p "$user_home/.local/bin" "$user_home/Library/LaunchAgents" "$data_dir/logs"
chmod 700 "$data_dir" "$data_dir/logs"
install -m 755 "$scripts/terminal-shell" "$user_home/.local/bin/terminal-shell"
install -m 755 "$scripts/ttyd-keepalive.sh" "$user_home/.local/bin/ttyd-keepalive.sh"
plist="$user_home/Library/LaunchAgents/io.shellgate.ttyd.plist"
python3 - "$scripts/io.shellgate.ttyd.plist" "$plist" "$user_home" "$data_dir" "$address" <<'PYPLIST'
import plistlib, sys
from pathlib import Path
source, target, home, data, address = sys.argv[1:]
def render(value):
    if isinstance(value, str):
        return value.replace('__HOME__',home).replace('__DATA__',data).replace('__TTYD_ADDR__',address)
    if isinstance(value, list): return [render(v) for v in value]
    if isinstance(value, dict): return {k: render(v) for k,v in value.items()}
    return value
Path(target).write_bytes(plistlib.dumps(render(plistlib.loads(Path(source).read_bytes()))))
PYPLIST
if "$no_start"; then echo 'LaunchAgent rendered; not started.'; exit 0; fi
"$scripts/build-ttyd.sh"
launchctl bootout "gui/$(id -u)/io.shellgate.ttyd" 2>/dev/null || true
launchctl bootstrap "gui/$(id -u)" "$plist"
launchctl enable "gui/$(id -u)/io.shellgate.ttyd"
launchctl kickstart -k "gui/$(id -u)/io.shellgate.ttyd"
echo 'Terminal installed. Keep it private; see docs/MAC_UPSTREAM_GUIDE.md.'
