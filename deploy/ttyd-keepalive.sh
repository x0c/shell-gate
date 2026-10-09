#!/usr/bin/env bash
# 终端节点终端主机（ttyd）保活包装脚本。
#
# 背景：root 托管的 ttyd-shellgate.service 用的是 Restart=on-failure，
# 一旦进程被 SIGTERM「干净地」停掉（退出码 0），systemd 会认为这是管理员的正常
# 操作而**不再拉起**——2026-08-04 18:23 就是这样停机 5 小时无人发现，网页终端
# 全程显示「终端上游不可用」。开发账号没有 root，改不动那个单元文件，因此另建
# 一个用户级常驻服务（见同目录 ttyd-shellgate-keepalive.service）做兜底。
#
# 关键保护：绝不与 root 的单元抢端口。启动前先看 7681 有没有被别人占着，
# 占着就原地等待（而不是失败退出触发 systemd 无限快速重启）；等对方释放了再接管。
# 这样两套监管可以共存：root 的在跑就让它跑，它没在跑才由本脚本顶上。
set -uo pipefail

ADDR="${TTYD_ADDR:-127.0.0.1}"
PORT="${TTYD_PORT:-7681}"
TTYD_BIN="${TTYD_BIN:-/usr/local/bin/ttyd}"
SHELL_BIN="${TTYD_SHELL:-${HOME}/.local/bin/terminal-shell}"
WAIT_SEC="${TTYD_WAIT_SEC:-15}"

# 端口被别的进程占用时待命。注意本脚本自己的 ttyd 还没起来，这里探到的一定是别人。
while ss -ltn "sport = :${PORT}" 2>/dev/null | grep -q ":${PORT}"; do
  echo "[keepalive] ${PORT} 端口已被其它实例占用（多半是 root 的 systemd 单元），等待 ${WAIT_SEC}s 后重试"
  sleep "${WAIT_SEC}"
done

echo "[keepalive] 启动 ttyd：${ADDR}:${PORT}"
# 参数与 /etc/systemd/system/ttyd-shellgate.service 的 ExecStart 保持一致。
# 注意 -t rendererType=canvas 必须是「一个」值参数，写成 -t rendererType canvas
# 会被 ttyd 判为 invalid client option 直接退出。
exec "${TTYD_BIN}" \
  -W -a -m 12 \
  -i "${ADDR}" -p "${PORT}" \
  -H X-Webauth-User \
  -t rendererType=canvas \
  -- "${SHELL_BIN}"
