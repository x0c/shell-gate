#!/usr/bin/env python3
"""Scan the releasable tree without printing matched private values."""
import ipaddress
import re
import subprocess
import sys
from pathlib import Path

root = Path(__file__).resolve().parent.parent
paths = subprocess.check_output(["git", "ls-files", "-co", "--exclude-standard", "-z"], cwd=root).decode().split("\0")
failures = []
for name in sorted(set(filter(None, paths))):
    path = root / name
    if not path.is_file() or name.startswith((".git/", "dist/")):
        continue
    raw = path.read_bytes()
    if b"\0" in raw:
        continue
    text = raw.decode("utf-8", errors="replace")
    reasons = []
    if re.search(r"/(?:Users|home)/[A-Za-z0-9_.-]+/", text):
        reasons.append("personal absolute home path")
    for candidate in re.findall(r"(?<![\w.])(?:\d{1,3}\.){3}\d{1,3}(?![\w.])", text):
        try:
            ip = ipaddress.ip_address(candidate)
        except ValueError:
            continue
        if any(ip in ipaddress.ip_network(net) for net in ((167772160, 8), (2886729728, 12), (3232235520, 16))):
            reasons.append("private network address")
            break
    if re.search(r"-----BEGIN (?:RSA |EC |OPENSSH )?PRIVATE KEY-----", text):
        reasons.append("private key")
    if reasons:
        failures.append((name, sorted(set(reasons))))
for name, reasons in failures:
    print(f"{name}: {', '.join(reasons)}", file=sys.stderr)
print(f"Reviewed {len(set(paths)) - 1} paths; {len(failures)} privacy failures.")
sys.exit(bool(failures))
