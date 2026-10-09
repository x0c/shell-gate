#!/usr/bin/env bash
# Explicit operator action: disable SSH password login after testing key access.
set -euo pipefail
[[ "$#" -eq 1 && "$1" == --key-access-verified ]] || { echo 'Verify a separate SSH key login first, then use --key-access-verified.' >&2; exit 64; }
[[ "$EUID" -eq 0 ]] || { echo 'Administrator privileges are required.' >&2; exit 1; }
mkdir -p /etc/ssh/sshd_config.d
install -m 644 /dev/stdin /etc/ssh/sshd_config.d/110-shellgate-key-only.conf <<'CONFIG'
PasswordAuthentication no
KbdInteractiveAuthentication no
PermitRootLogin no
PubkeyAuthentication yes
CONFIG
/usr/sbin/sshd -t
launchctl kickstart -k system/com.openssh.sshd
