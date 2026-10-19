# shell-gate 排查索引

> **是否权威源**：本索引只作入口与跳过门控；数字、裁定与验收命令以各篇正文为准，不在此复述。
> **何时跳过**：纯功能开发且不涉及卡顿/回显、TUI 乱码/渲染器、双上游或 Mac/SSH/ttyd 安全边界、Mac 上 Corral 一行行刷 / ttyd 压缩、Mac 唤醒后仍连不上 / 路由器 ARP、双层顶栏/工作台嵌套时，跳过本索引及下属排障文。

| 文档 | 何时该读 |
|---|---|
| [2026-08-05-terminal-echo-latency.md](2026-08-05-terminal-echo-latency.md) | 排查「终端卡 / 打字慢 / 回显 P95 虚高」、区分 CPU vs 网络、改回显/网络监控计时口径，或接入全屏 TUI 预置前 |
| [2026-09-10-mac-corral-line-by-line-refresh.md](2026-09-10-mac-corral-line-by-line-refresh.md) | 排查或回归「外网经网关连 Mac 跑全屏 TUI 一行行刷 / 秒断 reconnect / 中继侧丝滑 Mac 撕帧」、改 Mac ttyd/lws 构建或 `shell-gate-ttyd`、怀疑缺 `permessage-deflate`、误想降刷新/拼帧/换有线前**必读**。不读会退回 Homebrew 无压缩 ttyd、用 lws 5.0+extensions 弄断粘贴，或把 Wi-Fi 当根因 |
| [2026-10-15-mac-unreachable-after-wake.md](2026-10-15-mac-unreachable-after-wake.md) | Mac 唤醒后仍连接失败的路由器与本机诊断；2026-10-15 磁盘耗尽、交换文件失败、严重内存压力和手动强制重启的证据及归因边界 |
| [2026-08-20-tui-display-corruption.md](2026-08-20-tui-display-corruption.md) | 排查「TUI 乱码 / 中文消失 / 怪字 / 调窗口才恢复」、改渲染器，或评估是否改回 WebGL 前 |
| [2026-09-05-dual-upstream-security-audit.md](2026-09-05-dual-upstream-security-audit.md) | 评审或排查双上游 / Mac SSH / ttyd 边界、组网谁可达 22、密码登录与 pf/iptables 加固前 |
| [2026-09-05-gate-policy-hardening.md](2026-09-05-gate-policy-hardening.md) | 改反代 Transport、上游 URL 解析、会话 TTL / 探测间隔，或评审门禁放行/配置 fail-fast 前**必读**。不读会把内网 ttyd 流量送进环境代理，或让非法 TTL 静默回落 |
| [2026-09-08-double-title-bar-nested-workspace.md](2026-09-08-double-title-bar-nested-workspace.md) | 排查「双层顶栏 / 双层 title bar / 顶栏叠两套机器与启动器」、改 iframe 挂载或工作台/登录页防嵌套前**必读**。不读会把工作台再嵌进终端 iframe |

<!-- 该文档整理/压缩于 2026-10-15 -->
