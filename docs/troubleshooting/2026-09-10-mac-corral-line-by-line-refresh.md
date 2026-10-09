# Mac 上 Corral「一行行刷」/ 连上立刻断联（外网经门禁）

2026-09-10：用户**人在外面**，经 ShellGate 连 **mac** 跑 Corral：刷新一行行刷；随后变成连上立刻断、要 reconnect。同入口连 **linux** 正常。用户能明确感到终端节点滚动丝滑、Mac 一行行刷——**以体感为准，禁止用「同一网页终端所以两边该一样」否定差异。**

> 勘误：同日早先一版误写成「人在 Mac 上绕路回本机」。以用户纠正为准——场景是外网经门禁连 Mac。

## 产品硬约束（2026-09-10 用户裁定）

- **禁止降低刷新率**（不得为「顺滑」限帧、降 Corral/Textual 刷新、加「两帧相同才显示」一类降载）。
- **禁止合并帧**（不得在浏览器/门禁侧做 rAF 拼帧、DEC 2026 缓冲冒充原子帧、或把多段刷新合成更少次上屏）。
- 要对齐的是终端节点那条链路的**送达质量**，不是靠少画/晚画掩盖半帧。
- **【用户确认】修法就是传输压缩，没有别的黑科技**：刷新次数与内容不变，只少搬路上字节。

## 一行行刷：已确认根因与修复（2026-09-10）

**根因**：终端节点 ttyd 对浏览器协商的 `permessage-deflate` 会启用；Mac 当时跑的 Homebrew ttyd 链到 **libwebsockets 5.0 `LWS_WITHOUT_EXTENSIONS`**，握手**不回**该扩展。全屏 Corral 刷新高度可压缩，Mac 一路塞满明文，外网经门禁就撕帧；终端节点同入口正常。

**不是根因（已否决 / 勿再当首要修法）**：

- Wi‑Fi vs 有线：当时确有差异，但未做有线对照，且本机 loopback 同速率已够快——禁止先改网线/关隔空投送。
- 首页 HTTP 体下载间隙：曾误测成终端节点更差；那不是终端输出路径。
- 接收端临时 `TCP_QUICKACK`：未消除 Mac 滞后。
- 降刷新 / 拼帧 / 客户端攒整帧：产品硬否决。

**修法**：Mac 改用 ShellGate 自建二进制——**ttyd 1.7.7 + libwebsockets 4.3.10**，显式打开 extensions/zlib，静态链 lws，装到 `~/.local/share/shell-gate/ttyd-1.7.7-lws-4.3.10-deflate/bin/ttyd`，软链 `~/.local/bin/shell-gate-ttyd`。`ttyd-keepalive.sh` **优先**该路径，禁止再默认落到 brew。构建与门禁：`deploy/mac/build-ttyd.sh` + `deploy/mac/check-ttyd-compression.py`（协商压缩 + 大段粘贴双向字节一致）。

**禁止**：给 libwebsockets **5.0.0** 开 extensions 就上线——会复现 [ttyd #1532](https://github.com/tsl0922/ttyd/issues/1532)（压缩粘贴 `rx buffer underflow` 断线）。已在临时实例复现；未部署该组合。

### 备用门禁门禁对照（120 次更新 / 60 Hz，临时 Python 源，非 Corral）

| 条件 | Mac 4 KiB | Mac 16 KiB | 终端节点 4 KiB（对照） |
|---|---|---|---|
| 切换前（无 deflate）+ 压缩提议 | ~15 s / 收不全（约 102/120） | ~15 s / 约 30/120 | **1.981 s / 120** |
| 切换后（ShellGate ttyd）+ 压缩 | **1.996 s / 120**，span p95 ~2 ms | **2.606 s / 120** | 1.984 s / 120 |

用户在真实 Corral 里确认「变流畅」后，认定上线生效。本机仍可 `ps` 见 `shell-gate-ttyd` + 日志 `libwebsockets 4.3.10`；从备用门禁对 `mac.example.com:7681` 的 `/ws` 升级应带回 `Sec-WebSocket-Extensions: permessage-deflate`。

## 立刻断联（并行症状）

Mac `~/Library/Logs/shell-gate/ttyd.err.log` 在断联窗口反复出现：

```text
VALIDITY TIMEOUT EXPIRED ON (protocol tty, peer terminal.example.com)!
Server is closing connection. (ping=5, hangup=10)
WS closed from terminal.example.com
killing process
```

含义：备用门禁门禁（`terminal.example.com`）到 Mac 的 WebSocket 在默认 **5s ping / 10s hangup** 内没通过双向保活，ttyd 主动踢线。同段常有 `lws_issue_raw: partial completed`（上行写堵）。

已落地缓解：`deploy/mac/ttyd-keepalive.sh` 使用 **`-m 0`** + **`-P 30`（hangup≈37s）**。拉长保活**不能代替**压缩；拥塞减轻后 validity timeout 应一并少见。若仍秒断，先核是否又退回 brew（无 deflate）或参数丢了 `-P 30`。

## 曾核实的环境差异（背景，不是结案）

当日对照（同一备用门禁门禁）：

| 项 | 终端节点 | Mac |
|---|---|---|
| 门禁↔主机小包 RTT | ~31 ms | ~35 ms（接近） |
| ZeroTier 面 MTU | 1280 | 1280（相同） |
| 物理上网 | **有线 ens18** | **Wi‑Fi en0**（当时） |
| Corral / Textual | 同代 | 同代 |
| WebSocket `permessage-deflate` | **有** | 修复前 **无** → 修复后 **有** |

## 明确否决

- 浏览器侧 rAF / 拼 WebSocket 段再 `term.write`
- 假装支持 DEC 2026 只为在客户端攒整帧
- 降 Corral 抓帧/刷新频率、或「两帧相同才显示」
- 把「换有线 / 关隔空投送」当成一行行刷的首要修法
- 用 Homebrew ttyd / lws 5.0 无扩展构建冒充已修复
- 用 lws 5.0 + extensions 上线（粘贴断线）

## 排查顺序

1. 确认用户在外网、对比机是 mac vs linux；**以体感为准**。
2. 秒断 → 看 `ttyd.err.log` 的 `VALIDITY TIMEOUT`；核对 `-m 0 -P 30`。
3. 一行行刷 → 从门禁节点对两端 `/ws` 带 `Sec-WebSocket-Extensions: permessage-deflate`：Mac 必须协商成功。失败则重跑 `deploy/mac/build-ttyd.sh`、确认 keepalive 指向 `shell-gate-ttyd`，再 `launchctl kickstart -k gui/$(id -u)/io.shellgate.ttyd`。
4. 仍差 → 同负载对照（临时进程、固定 60 Hz），隔离 PTY / 传输 / 浏览器；禁止空口结案「网络没问题、是用户错觉」。
5. 改 ttyd / lws 后必须跑 `check-ttyd-compression.py`（含 ≥8 KiB 与 1 MiB 压缩粘贴往返）。

References: [ttyd extension registration](https://github.com/tsl0922/ttyd/blob/1.7.7/src/server.c),
[libwebsockets 5.0.0 options](https://github.com/warmcat/libwebsockets/blob/v5.0.0/CMakeLists.txt),
[ttyd #1532](https://github.com/tsl0922/ttyd/issues/1532).
