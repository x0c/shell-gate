#!/usr/bin/env bash
# Gateway callers may request only two actions：空命令唤醒 Mac，或以严格格式创建/释放一个 Tab 租约。
# 绝不解释 SSH_ORIGINAL_COMMAND，避免这把桥接钥匙退化成通用的中继主机 shell。
set -euo pipefail

readonly wake_script="${HOME}/.local/bin/shell-gate-wake-mac"

if [[ -z "${SSH_ORIGINAL_COMMAND:-}" ]]; then
  exec "$wake_script"
fi

read -r kind action tab_id extra <<< "$SSH_ORIGINAL_COMMAND"
if [[ "$kind" != "lease" || -n "${extra:-}" || ! "$action" =~ ^(acquire|release)$ || ! "$tab_id" =~ ^[0-9a-f]{32}$ ]]; then
  echo "拒绝未授权的桥接请求" >&2
  exit 64
fi

# 租约经点对点隧道下发（示例经 ZeroTier 到 Mac）：离家在家都通；唤醒仍走家庭网，见 MAC_UPSTREAM_GUIDE。
exec /usr/bin/ssh \
  -F "${HOME}/.ssh/config" \
  -o BatchMode=yes \
  -o ConnectTimeout=8 \
  "${SHELL_GATE_MAC_HOST:?Set SHELL_GATE_MAC_HOST}" "${SHELL_GATE_MAC_LEASE_PATH:?Set the Mac lease script path}" "$action" "$tab_id"
