#!/usr/bin/env bash
# 卡顿当下在上游主机只读采样：ttyd↔网关积压、会话写出速率、RTT。
# 用法：bash deploy/sample-ttyd-lag.sh
set -euo pipefail

SEOL_ZT="${SHELL_GATE_PEER:?Set SHELL_GATE_PEER to the gateway hostname}"
TTYD_ADDR="${TTYD_ADDR:-127.0.0.1:7681}"
INTERVAL_SEC="${INTERVAL_SEC:-2}"

# 收集 ttyd 进程树（含 shell → TUI/agent 等后代），只读。
collect_tree_pids() {
  local root="$1"
  local -a queue=("${root}")
  local -A seen=()
  local pid child
  while ((${#queue[@]})); do
    pid="${queue[0]}"
    queue=("${queue[@]:1}")
    [[ -n "${seen[$pid]:-}" ]] && continue
    seen["${pid}"]=1
    if [[ "${pid}" != "${root}" ]]; then
      echo "${pid}"
    fi
    while read -r child; do
      [[ -n "${child}" ]] && queue+=("${child}")
    done < <(pgrep -P "${pid}" 2>/dev/null || true)
  done
}

echo "=== ping ${SEOL_ZT} ==="
ping -c 5 -W 1 "${SEOL_ZT}" || true

echo
echo "=== ss ${TTYD_ADDR} (Send-Q / notsent / retrans) ==="
ss -tnpi 2>/dev/null | grep -A1 "${TTYD_ADDR}" || echo "(no established connections)"

echo
echo "=== ttyd process tree wchar @ t0 ==="
ttyd_pid="$(pgrep -x ttyd | head -n1 || true)"
if [[ -z "${ttyd_pid}" ]]; then
  echo "ttyd not running"
  exit 0
fi

declare -A wchar0=()
while read -r pid; do
  [[ -z "${pid}" ]] && continue
  w="$(awk '/^wchar/{print $2}' "/proc/${pid}/io" 2>/dev/null || echo 0)"
  cmd="$(tr '\0' ' ' <"/proc/${pid}/cmdline" 2>/dev/null | cut -c1-72)"
  wchar0["${pid}"]="${w}"
  printf 'pid=%s wchar=%s cmd=%s\n' "${pid}" "${w}" "${cmd}"
done < <(collect_tree_pids "${ttyd_pid}")

echo
echo "=== sleep ${INTERVAL_SEC}s ==="
sleep "${INTERVAL_SEC}"

echo
echo "=== ttyd process tree wchar delta /${INTERVAL_SEC}s ==="
for pid in "${!wchar0[@]}"; do
  if [[ ! -r "/proc/${pid}/io" ]]; then
    printf 'pid=%s gone\n' "${pid}"
    continue
  fi
  w="$(awk '/^wchar/{print $2}' "/proc/${pid}/io" 2>/dev/null || echo 0)"
  delta=$((w - wchar0["${pid}"]))
  bps=$((delta / INTERVAL_SEC))
  cmd="$(tr '\0' ' ' <"/proc/${pid}/cmdline" 2>/dev/null | cut -c1-72)"
  printf 'pid=%s delta=%s (~%s B/s) cmd=%s\n' "${pid}" "${delta}" "${bps}" "${cmd}"
done

echo
echo "=== ss ${TTYD_ADDR} again ==="
ss -tnpi 2>/dev/null | grep -A1 "${TTYD_ADDR}" || echo "(no established connections)"

echo
echo "判读：Send-Q/notsent 升高或 wchar≈100KB/s → 上行排队；接近 0 且浏览器 perf-sample 高 render/longtask → 偏前端渲染。"
