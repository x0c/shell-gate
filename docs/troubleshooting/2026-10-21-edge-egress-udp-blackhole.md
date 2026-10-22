# 单边缘全部机器同时变红——边缘出口方向覆盖网单向中断

Workspace 现象：某边缘的机器目录里**全部**机器变红（`healthy=false`），而另一边缘看同一组上游正常；各上游在本机查都活着（ttyd 在听、探测位正常回包）。

## Ruling（2026-10-21，open：丢包点未 isolate 到单侧）

一次实测中毒位：边缘 A 到两台上游（中继主机上的 ttyd、家里的 Mac ttyd）的探测在同一秒内一起 `i/o timeout`，随后变 `no route to host`，而边缘 B 到两台的探测全程正常。两台上游之间互访正常，上游各自的反代探测位也正常。

- 不是上游宕机：两台上游进程、监听端口、探测鉴权头全部正常；其中一台在本机 `curl` 探测位直接回 200。
- 不是门禁进程故障：边缘 A 的网关进程、`/healthz`、公网 HTTPS、Caddy 全正常。
- 不是覆盖网控制面故障：边缘 A 的 ZeroTier 显示 `ONLINE`、接口有地址、路由表完整，重启 `zerotier-one` 后仍不恢复。
- 传输证据（两端同时抓包）：上游侧能收到边缘 A 发来的包；边缘 A 侧 20 秒内**一个**来自家庭上行 IP 的 UDP 包都收不到；路由器 conntrack 显示上游发往边缘 A 的 UDP 计数在涨——包离家正常，死在半路。同一源到同一目的 IP 的 ICMP 与 TCP 直连全部正常，**只有 UDP 过不去**，且只针对该边缘一个目的 IP（到另一边缘/其它云节点的同协议包正常）。
- 候选丢包点（二选一，未定）：家庭光猫/ISP 到该边缘方向的 UDP 过滤，或边缘云入口的 Anti-DDoS/安全组按源 IP 丢 UDP。两侧主机防火墙都是干净的（INPUT 全放行、无 fail2ban/ipset），可排除主机侧。

## Verified diagnostic order

1. **分边**：两个边缘的 `/healthz` 与机器目录各看一眼。若只有一边全红、另一边全绿，**先判该边缘出口问题**，不要逐台折腾上游。
2. **上游本机**：`pgrep ttyd`、`ss -ltn | grep 7681`、带 `X-Webauth-User` 头 `curl` 探测位。都正常就排除上游。
3. **网关日志**：看失败是单台还是多台同秒开始（`反代 ttyd 失败: dial tcp …: i/o timeout` → `no route to host`）。多台同秒=公共原因。
4. **覆盖网控制 vs 数据**：`zerotier-cli info`（ONLINE 只证明控制面）、`listnetworks`（地址/路由）、`ip neigh`（`INCOMPLETE` 对端不可达）。控制面正常不能证明数据面正常。
5. **按对端看 JSON**：`zerotier-cli -j listpeers` 取对端节点的 `paths`（`lastReceive`/`lastSend` 是否单向停滞、`preferred` 是哪条）。
6. **两端同时抓包**：两边 `tcpdump udp port 9993` 同一窗口，看包到底死在哪一侧。这是本轮定案的关键证据。
7. **路由器 conntrack 双采样**：同一流表项隔 10 秒读两次计数。注意硬件 OFFLOAD 表项的计数会冻结——要看非 offload/`UNREPLIED` 项的变化，不能只看绝对值。
8. **Underlay 协议矩阵**：同源到同目的分别打 ICMP / TCP / UDP。ICMP 通 + TCP 通 + UDP 不通 = 按协议过滤；结合 traceroute 看死在第几跳、靠近哪一侧。
9. **DNS 劫持坑**：先 `getent hosts <domain>`。家宽路由器可能把域名劫到代理 TUN 网段（如 `198.18.x.x`），此时按域名 `curl` 走的是代理、不是直连——直连验证必须用 `--resolve <domain>:443:<公网IP>` 强制。
10. **看本轮新增的网关日志**：全红进出时会各记一行（见下），直接给出方向。

## 本轮新增的观测手段（代码）

- 任一上游健康翻转时服务端重估全局：进入「全部不可用」记一行（含机器名），恢复再记一行。单台抖动不打扰。守卫测试 `TestRecordProbeFlipsHealthHook`、`TestAllUpstreamsDownNotifiesOnce`。
- `GET /api/machines` 条目新增 `lastError`（最近探测错误文本，健康时为空）：面板或 `curl` 里直接区分超时 / 拒绝 / 407。守卫测试 `TestMachinesEndpointShowsLastError`。

## Workaround（恢复使用）

切到健康的另一边缘域名登录使用。本轮是单边缘出口故障，换边即恢复，不需要动任何上游。

## 已知可用性缺口（未修，设计层面）

- 登录时竞速选边后就粘在该边：该边出口一断，全部机器变红，哪怕另一边完全正常——没有边缘级自动故障转移。
- Mac 的唤醒/租约/presence 全走中继单路径：中继 overlay 一断，唤醒与租约释放一起失败（`exit status 255` 刷屏），与终端流量同命。
- 覆盖网无中继回退：直连路径一死就一直死，直到打洞重新成功；本轮 15 分钟以上未自愈。

## Limits

- 丢包点未 isolate：需边缘云控制台查 Anti-DDoS/安全组事件，或查家庭光猫在故障时刻是否重拨/更新，二者都需要运维侧配合。
- 文中地址均为占位写法；原始诊断含私有 overlay 与公网地址，按仓库隐私规则不得入库。

## 部署验证（2026-10-21 14:17–14:19，v0.1.5）

- 两边缘网关均已升级到 v0.1.5 并重启验证（`/healthz` 正常）：重启后约 30–60 秒两边各记一行「上游机器全部不可用（n=2）」，证明该信号在生产生效。
- 当时两边缘到两台上游的覆盖网仍全断（家宽上行方向批量吞吐接近于零：直传与 SOCKS 代理都发不出去 1MB，最终经由与两边都互通的第三台云节点中转才把 7MB 包送达）。这进一步支持「故障在家庭上行/光猫到云方向，而非网关或上游进程」。

## Related

- Domain KB：[终端反代与多上游接入](../TERMINAL_PROXY_KNOWLEDGE_BASE.md)（§5.5 探测身份、§6 禁令）。
- 上一条同类：[Mac 唤醒后仍连不上](2026-10-15-mac-unreachable-after-wake.md)（那次是本机资源耗尽，这次是边缘出口；症状都是单边红，根因完全不同）。
- 双边缘设计：[双入口共享存储](../design/2026-09-08-dual-edge-shared-store.md)。
