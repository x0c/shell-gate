#!/usr/bin/env bash
# Apply an operator-reviewed firewall policy; never infer machine addresses.
set -euo pipefail
[[ "$#" -eq 1 && -f "$1" ]] || { echo 'Usage: sudo enable-remote-and-pf.sh REVIEWED_PF_CONFIG' >&2; exit 64; }
[[ "$EUID" -eq 0 ]] || { echo 'Administrator privileges are required.' >&2; exit 1; }
pfctl -nf "$1"
install -m 644 "$1" /etc/pf.anchors/shellgate.ttyd
if ! grep -q 'anchor "shellgate.ttyd"' /etc/pf.conf; then
  printf '\nanchor "shellgate.ttyd"\nload anchor "shellgate.ttyd" from "/etc/pf.anchors/shellgate.ttyd"\n' >> /etc/pf.conf
fi
pfctl -f /etc/pf.conf
pfctl -e 2>/dev/null || true
pfctl -a shellgate.ttyd -s rules
