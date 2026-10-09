# 开发设计：多上游机器支持

Current standalone provider defaults and import rules are defined in [deployment](../DEPLOYMENT.md). Infrastructure choices in this earlier design are optional deployment examples, not mandatory dependencies.
> 状态：已实现（v0.6.0，环境变量方案）· 2026-08-24 定稿，2026-08-27 实施
> 目标读者：实现本功能的 Agent / 未来排查多机器链路的人。

## 1. 背景与目标

当前 shell-gate 只反代**一台**终端主机（linux 的 ttyd，`SHELL_TTYD_URL` 单值）。所有 Tab、预置、网络监控、增强脚本注入都硬绑这一个上游。目标是支持**多台上游终端主机**：

- 每个 Tab 可以开在任意一台已配置的机器上；
- 顶栏启动器选择目标机器，一键开 Tab 的体验不变；
- 预置程序可固定绑定某台机器，也可跟随当前选择；
- 网络监控面板按机器分段展示上行段与端到端指标；
- 单机部署（现状）行为完全不变，界面不多出任何东西。

## 2. 现状盘点：单上游假设都在哪

| 位置 | 单上游假设 | 改动 |
| --- | --- | --- |
| `internal/server/config.go` | `SHELL_TTYD_URL` 单值 -> `Config.TTYDURL` | 改为机器列表解析，保留单值回退 |
| `internal/server/server.go` | `Server.proxy` 单个 ReverseProxy；`netmon` 单个监控器 | 每机器一个 proxy + netmon |
| `internal/server/tabs.go` | `TabSession` 无机器维度 | 加 `Machine` 字段 |
| `internal/server/netmon.go` | 探测/字节统计/快照全是一份 | netMonitor 按机器实例化，快照分机器 |
| `internal/server/store/presets.go` | 预置无机器绑定 | 迁移加 `machine` 列 |
| `web/workspace.html` | 开 Tab 不带机器；面板单条上行链 | 机器选择器 + Tab 徽标 + 面板分段 |

## 3. 配置模型

新增环境变量 `SHELL_TTYD_TARGETS`，逗号分隔的 `名称=地址` 列表：

```
SHELL_TTYD_TARGETS=linux=http://terminal.example.com:7681,tokyo=http://extra-terminal.example.com:7681
```

- **机器名**：`[a-zA-Z0-9_-]{1,16}`，重复或非法即启动失败（fail-fast，不让拼错的机器静默丢掉）。
- **顺序即展示顺序，第一台是默认机器**（「＋ shell」未选择时的落点）。
- **向后兼容**：未配置 `SHELL_TTYD_TARGETS` 时回退现有 `SHELL_TTYD_URL`，机器名为 `default`。现部署不改任何环境变量也能升到新版本。
- 机器是**部署级配置**（新增一台要先装 ttyd + 鉴权 + keepalive，见 §8），放环境变量而非界面管理；列表变化重启门禁即生效，频率极低。

**机器目录必须由服务端下发**（遵守全局「服务端下发展示元数据」规范）：前端不内置任何机器名或地址，只从 `GET /api/machines` 拉取；服务端新增机器后所有客户端不刷新页面即可见到（Tab 徽标配色除外，见 §6）。

## 4. 服务端设计

### 4.1 上游抽象

```go
type upstream struct {
    name   string
    target *url.URL
    proxy  *httputil.ReverseProxy
    netmon *netMonitor
}
// Server 持有 ordered []*upstream + map[string]*upstream
```

- `newTTYDProxy` / `newNetMonitor` 逻辑不变，只是按机器各实例化一份：代理重写、增强脚本注入、`interactiveLoginCommand` 包装、身份头处理全部复用现实现，**逐字不动**。
- 每个 netMonitor 独立跑探测循环；`Shutdown` 停全部 loop。探测流量随机器数线性增长，间隔仍由 `SHELL_NETMON_INTERVAL` 统一控制（机器多时可调大）。
- `probeIdentity` 维持全局一份（白名单字典序最小邮箱），各机器探测口径一致。

### 4.2 Tab 与接口

- `TabSession` 加 `Machine string`；`POST /api/tabs` 请求体加 `machine`（缺省 = 默认机器；不存在的机器 -> 400）。
- `handleTermProxy` 按 `tab.Machine` 查 upstream：
  - 机器仍在配置里 -> 走它的 proxy（`/t/{id}/` 路由结构不变，URL 不含机器名，鉴权仍走 Tab 归属邮箱）；
  - 机器已被从配置里删掉 -> 502「终端上游不可用」，与上游宕机同表现，不额外做路由迁移。
- 新增 `GET /api/machines`：

```json
{ "machines": [
  { "name": "linux", "target": "terminal.example.com:7681", "default": true,
    "healthy": true, "failStreak": 0, "lastProbeMs": 33.2 }
] }
```

  `healthy` = 最近一次探测成功且连续失败 < 3。前端在选择器上给不健康的机器一个弱化标记，仍允许选择（可能只是暂时抖动）。

### 4.4 可睡眠上游的按需唤醒（2026-09-06）

- 可选配置 `SHELL_WAKE_COMMANDS=mac=/绝对路径/wake-mac`。每项只允许绑定已配置机器和固定的绝对可执行文件；门禁不拼 shell 参数，启动时确认文件存在且可执行。
- `GET /api/machines` 对配置了唤醒入口的机器返回 `wakeable: true`；用户主动连接时，前端先调用 `POST /api/machines/{name}/ready`。门禁先快速探测：已在线直接返回；离线才执行固定入口，并等待该机 ttyd 的 TCP 端口恢复。
- **在家抑制唤醒（2026-09-09）**：可选 `SHELL_WAKE_HOME_PRESENCE_URL` / `_TOKEN` / `_MACHINES`。离线且机器在名单内时，先查 presence 服务的 `comfort_home`；为真则拒绝发魔术包并返回在家文案。查询失败 fail-open。详见 [在家时关闭 Mac 自动唤醒](2026-09-09-wake-suppress-when-home.md)。
- **唤醒属于用户动作，不属于健康维护**：仅切换到该机器、打开 shell、点击绑定该机器的预置或直接创建该机 Tab 可以触发。网络监控、健康探测、页面后台刷新、隐藏 Tab 的断线重连不得调用唤醒；终端反代路径本身也不隐式唤醒。
- 同一时刻的重复唤醒请求由服务端合并。等待期间页面显示「正在连接；如在睡眠会自动唤醒」，失败则说明机器可能未接电或已关机（在家抑制时另有明确文案），不再只报笼统的上游不可用。
- Mac 必须保留正常系统睡眠。每个 ShellGate Mac Tab 创建时取得独立租约；租约必须阻止系统睡眠，不能只阻止空闲睡眠——否则 Maintenance Sleep 仍会断开网关到 Mac 的虚拟网络。浏览器切后台、锁屏、网络暂断、被系统暂停或刷新 ShellGate 都不会释放，避免暂时离开前台或重载界面就睡着。刷新从门禁读取当前账号仍存活的 Tab，并仅恢复这个浏览器窗口此前那组 Tab、顺序与当前选择；绝不把刷新实现成新开空 shell，也不因此唤醒任何机器。用户在 ShellGate 内明确关闭 Tab 时释放；异常关闭无法通知时租约最多 8 小时自毁。禁止给整个 ttyd 服务或登录用户安装永久常驻 `caffeinate`、关闭系统睡眠。

### 4.3 预置绑定（SQLite 迁移）

- `presets` 表加列：`ALTER TABLE presets ADD COLUMN machine TEXT NOT NULL DEFAULT ''`（`migrate()` 里做幂等迁移，检测 `PRAGMA table_info`）。
- 空串 = 跟随当前选择；非空 = 固定机器（创建/更新时校验机器存在）。
- 打开 Tab 时机器优先级：**预置绑定 > 请求指定 > 默认机器**。
- `EnsureDefaults` 不动（默认预置跟随选择）。

## 5. 网络监控多机器化

三段口径不变，只按机器拆分上行段：

- **下行段（浏览器 ↔ 门禁）**：与机器无关，仍是全局一份，`/api/netmon/ping` 不变。
- **上行段（门禁 ↔ 终端主机）**：`GET /api/netmon` 快照的 `upstream` 单对象改为 `upstreams: { "<机器名>": netmonUpstream }`；`gate`、`clients` 保持全局。
- **端到端（按键 -> 回显）**：enhance.js 照旧按 tabId 用 `postMessage` 递给父页，**不改**；父页知道 Tab -> 机器的映射，在 `/api/netmon/sample` 的每个 Tab 条目里补 `machine` 字段，服务端照旧整体收存。面板按机器聚合展示。

面板 UI：上行段与端到端按机器分成小节（机器名做小标题），一台机器时与现状视觉完全一致。

## 6. 前端设计（workspace.html）

- **机器选择器**：顶栏启动器左侧加一排小 pill（每台机器一个，选中态描边加亮）。遵循既有裁定：描边 + 透明底、不用渐变填充。窄屏（≤760px）收进 `☰` 菜单顶部，与现有收纳规则一致。**只有一台机器时选择器整体不渲染**——现部署升级后界面零变化。
- **切换机器 = 切换 Tab 集合（2026-09-05 裁定；2026-10-09 补记按机激活记忆）**：Tab 条只渲染当前选中机器上的 Tab；其它机器的 Tab 与 iframe 留在 DOM 里保活，不关上游进程。切机时若当前激活 Tab 不属于新机器，**优先恢复该机上次激活的 Tab**（本窗口 `sessionStorage` 按机器名记忆；关掉或已不存在则回落该机最近一个 Tab）；没有则空态。在某机上点选 / 新开 / 关掉后留下的当前 Tab，都要写入该机记忆。单机部署仍展示全部 Tab（无选择器）。
- **开 Tab**：「＋ shell」与未绑定机器的预置在当前选中机器上开；绑定了机器的预置按绑定开，按钮 hint 尾部显示机器名（如 `example-tui · linux`）。若预置落到另一台机器，同时把选择器切到那台，保证新 Tab 出现在当前条上。
- **Tab 徽标**：Tab 条已按机器过滤后，同条内不必再出色点（机器由左侧 pill 表达）。历史「混排时用色点区分」不再适用。
- **预置管理**：编辑表单加「机器」下拉（`跟随选择` / 各机器名）。
- **网络监控面板**：见 §5；小窗形态、悬浮非模态等 2026-08-03 裁定全部保持。

## 7. 上游部署前提（每台新机器，写入部署清单）

门禁侧协议不变，但每台新机器必须具备（这些是 linux 已有的配套，新机器要照抄）：

1. ttyd（≥1.7.7）以 `-H X-Webauth-User` 鉴权方式启动，收到 `arg` 后经登录脚本拉起 shell（对齐已有 Linux 机型的上游登录脚本模式）。
2. systemd 托管 + keepalive 兜底（复制 `deploy/ttyd-keepalive.sh` + 用户级 service 的模式；「干净 SIGTERM 不自愈」教训直接适用）。
3. 账号登录壳按本机习惯加载提示符 / 行编辑 / 指令预测等插件；网页会话跟 `$SHELL`，**不要**再为网页单独跳过（2026-09-05 裁定，见根 `AGENTS.md`）。接受回显可能变慢。
4. 网关到该机器的网络可达且延迟可接受（>100ms 时网络面板会直接暴露）。

实施时把这份清单落进本文件 `deploy/` 或 AGENTS.md（以最终实现为准）。

## 8. 测试与验收

单测（新增/调整）：

- 配置解析：多目标、重名、非法名、单值回退、顺序保留。
- `POST /api/tabs` 机器校验；`handleTermProxy` 按机器路由；删除机器后存量 Tab 502。
- 可睡眠机器：已在线不执行唤醒；离线执行一次并等端口恢复；重复请求合并；命令失败/等待超时返回明确错误；后台探测与终端反代不触发唤醒。
- presets 迁移幂等 + 绑定校验 + 优先级（预置 > 请求 > 默认）。
- netmon 快照多机器结构；`/api/machines` healthy 判定。
- 既有测试全部保持绿（`go test -race ./...`），尤其 `TestInteractiveLoginCommandHasNoControlChars`、增强脚本守护测试。

实跑验收（不许只跑单测）：

1. 本机起两个 ttyd 实例（127.0.0.1 不同端口，注意 `-t rendererType=canvas` 必须写成一个参数）作两台机器，配置 `SHELL_TTYD_TARGETS` 启动门禁。
2. **两台机器各真连一次验 101 握手**：`curl` 带 Upgrade 头打 `/ws?arg=…`，确认握手码 101 且 arg 无控制字符（libwebsockets 的 `%0A` 教训）。
3. 浏览器真开两台机器的 Tab，各自敲命令确认落回 `$SHELL` 交互壳，且本机插件都在（如提示符主题、行编辑增强等）。
4. **切换机器 pill 时 Tab 条只显示该机集合**，另一台的会话仍保活，切回去还在，且**恢复离开前该机激活的那个 Tab**（不是一律落到最近开的）；预置绑定到其它机器时选择器跟着切；窄屏收纳进 ☰ 菜单。
5. 网络监控面板按机器分段出数；`perf-log.ndjson` 里 netmon-sample 带 machine 字段。
6. **单机部署回归**：只配 `SHELL_TTYD_URL` 启动，确认界面与现状零差异（无选择器、Tab 不过滤）。

## 9. 待用户确认的产品决策

1. **机器列表来源**：本设计选环境变量（部署级、重启生效、改错会 fail-fast）。备选是像预置一样进 SQLite 由界面管理——不推荐，机器本质是基础设施，界面上加错一个 URL 只会得到一堆连不上的死机器。
2. **交互形态**：顶栏机器 pill 选择器 + 预置可绑定。备选是每个预置必须绑定机器、不提供全局选择器——不推荐，「＋ shell」就没地方选机器了。

## 10. 实施顺序（每阶段独立可验证可发布）

1. **服务端**：upstream 抽象 + 配置解析 + `/api/machines` + Tab.Machine + presets 迁移（UI 暂不动，单机部署零感知）。
2. **前端**：机器选择器 + Tab 徽标 + 预置绑定编辑。
3. **监控**：netmon 快照/样本/面板多机器化。
4. **文档**：AGENTS.md 登记多机器约定与部署清单，本文件状态改为「已实施」。

<!-- 该文档整理/压缩于 2026-09-05 -->
