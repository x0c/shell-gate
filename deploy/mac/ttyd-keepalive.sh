#!/usr/bin/env bash
# Mac 网页终端（ttyd）保活：等 ZeroTier 地址就绪后启动。
# 由 LaunchAgent KeepAlive 托管；干净退出也会被拉起。
set -uo pipefail

ADDR="${TTYD_ADDR:-127.0.0.1}"
PORT="${TTYD_PORT:-7681}"
TTYD_BIN="${TTYD_BIN:-}"
SHELL_BIN="${TTYD_SHELL:-${HOME}/.local/bin/terminal-shell}"
WAIT_SEC="${TTYD_WAIT_SEC:-5}"
LOG_DIR="${XDG_CONFIG_HOME:-${HOME}/.config}/shell-gate/logs"
mkdir -p "${LOG_DIR}"

if [[ -z "${TTYD_BIN}" ]]; then
  # Prefer the ShellGate-built binary (WebSocket compression). Homebrew
  # libwebsockets 5.0 ships LWS_WITHOUT_EXTENSIONS and cannot negotiate
  # permessage-deflate — that mismatch caused Mac Corral line-by-line refresh
  # over the gate while Suzhou stayed smooth (2026-09-10).
  for candidate in \
    "${HOME}/.local/bin/shell-gate-ttyd" \
    "${HOME}/.local/share/shell-gate/ttyd-1.7.7-lws-4.3.10-deflate/bin/ttyd" \
    /opt/homebrew/bin/ttyd \
    /usr/local/bin/ttyd \
    "$(command -v ttyd 2>/dev/null || true)"; do
    if [[ -n "${candidate}" && -x "${candidate}" ]]; then
      TTYD_BIN="${candidate}"
      break
    fi
  done
fi

if [[ -z "${TTYD_BIN}" || ! -x "${TTYD_BIN}" ]]; then
  echo "[keepalive] 找不到 ttyd，${WAIT_SEC}s 后退出等 LaunchAgent 重试" >&2
  sleep "${WAIT_SEC}"
  exit 1
fi

if [[ ! -x "${SHELL_BIN}" ]]; then
  echo "[keepalive] 找不到登录脚本 ${SHELL_BIN}" >&2
  sleep "${WAIT_SEC}"
  exit 1
fi

IFCONFIG="${IFCONFIG_BIN:-/sbin/ifconfig}"
LSOF="${LSOF_BIN:-/usr/sbin/lsof}"

# 等 ZeroTier 地址出现在本机（睡眠醒来后接口可能晚几秒）
for _ in $(seq 1 60); do
  if "${IFCONFIG}" 2>/dev/null | grep -q "inet ${ADDR} "; then
    break
  fi
  echo "[keepalive] 等待 ${ADDR} 就绪…"
  sleep "${WAIT_SEC}"
done

if ! "${IFCONFIG}" 2>/dev/null | grep -q "inet ${ADDR} "; then
  echo "[keepalive] ${ADDR} 仍不可用，退出重试" >&2
  exit 1
fi

# 端口已被占用则待命（不抢）
while "${LSOF}" -nP -iTCP:"${PORT}" -sTCP:LISTEN 2>/dev/null | grep -q .; do
  # 若占用者就是本机目标地址上的 ttyd，直接退出让 LaunchAgent 别狂刷
  if "${LSOF}" -nP -iTCP:@"${ADDR}":"${PORT}" -sTCP:LISTEN 2>/dev/null | grep -q ttyd; then
    echo "[keepalive] ${ADDR}:${PORT} 已有 ttyd 在听，退出"
    exit 0
  fi
  echo "[keepalive] ${PORT} 被占用，等待 ${WAIT_SEC}s"
  sleep "${WAIT_SEC}"
done

echo "[keepalive] 启动 ttyd：${TTYD_BIN} ${ADDR}:${PORT}"
# -t rendererType=canvas 必须是一个参数值
# -m 0：不设客户端上限（旧值 12 对多 Corral Tab 太紧，外网重连风暴时直接拒连/秒断）。
# -P 30：默认 ping=5 / hangup=10。Mac 上 Corral 全屏刷新时 ZT→门禁上行易堵，
# libwebsockets 会在 10s 内因 VALIDITY TIMEOUT 踢掉 WS（日志 peer 为门禁节点），
# 浏览器立刻断联要 reconnect，并留下孤儿 shell。拉长到 30/37 扛住突发拥堵。
# 2026-09-10 外网连 Mac 实测：备用门禁门禁路径反复 validity timeout；终端节点同入口正常。
exec "${TTYD_BIN}" \
  -W -a -m 0 \
  -P 30 \
  -i "${ADDR}" -p "${PORT}" \
  -H X-Webauth-User \
  -t rendererType=canvas \
  -- "${SHELL_BIN}"
