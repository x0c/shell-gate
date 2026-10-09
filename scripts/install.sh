#!/usr/bin/env bash
# Install a published gateway binary; terminal backend is configured separately.
set -euo pipefail
repository="x0c/shell-gate"
version="${SHELL_GATE_VERSION:-latest}"
case "$(uname -s)" in Darwin) platform=darwin;; Linux) platform=linux;; *) echo 'Use install.ps1 on Windows.' >&2; exit 1;; esac
case "$(uname -m)" in x86_64|amd64) arch=amd64;; arm64|aarch64) arch=arm64;; *) echo 'Unsupported architecture.' >&2; exit 1;; esac
if [[ "$version" == latest ]]; then
  version="$(curl -fsSL "https://api.github.com/repos/$repository/releases/latest" | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -1)"
fi
[[ "$version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[a-z0-9.]+)?$ ]] || { echo 'Invalid release version.' >&2; exit 1; }
task_tmp="$(mktemp -d)"
trap 'rm -rf "$task_tmp"' EXIT
asset="shell-gate_${version}_${platform}_${arch}.tar.gz"
base="https://github.com/$repository/releases/download/$version"
curl -fsSL "$base/$asset" -o "$task_tmp/$asset"
curl -fsSL "$base/SHA256SUMS" -o "$task_tmp/SHA256SUMS"
expected="$(awk -v asset="$asset" '$2 == asset {print $1}' "$task_tmp/SHA256SUMS")"
[[ "$expected" =~ ^[0-9a-f]{64}$ ]] || { echo 'Missing release checksum.' >&2; exit 1; }
if command -v sha256sum >/dev/null 2>&1; then actual="$(sha256sum "$task_tmp/$asset" | cut -d ' ' -f1)";
else actual="$(shasum -a 256 "$task_tmp/$asset" | cut -d ' ' -f1)"; fi
[[ "$expected" == "$actual" ]] || { echo 'Release checksum mismatch.' >&2; exit 1; }
tar -xzf "$task_tmp/$asset" -C "$task_tmp"
[[ "$("$task_tmp/shell-gate" version)" == "$version" ]] || { echo 'Release version mismatch.' >&2; exit 1; }
destination="${SHELL_GATE_INSTALL_DIR:-${HOME}/.local/bin}"
mkdir -p "$destination"
install -m 755 "$task_tmp/shell-gate" "$destination/shell-gate"
printf 'Installed %s to %s/shell-gate. Add this directory to PATH if needed.\n' "$version" "$destination"
