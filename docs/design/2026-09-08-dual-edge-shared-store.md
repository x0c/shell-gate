# 双入口门禁 + 共享 Postgres（2026-09-08；选路 2026-09-09 改）

Current standalone provider defaults and import rules are defined in [deployment](../DEPLOYMENT.md). Infrastructure choices in this earlier design are optional deployment examples, not mandatory dependencies.
> 实现、评审或排查「备用门禁/门禁节点双门禁、按延迟选路、Tab/预置跨入口一致」前**必读**。

## 产品意图

1. **双活门禁**：备用门禁与门禁节点各跑一份 shell-gate；浏览器走更近的那台。
2. **同域 Passkey**：通行密钥只在 `https://shell.example.com`（WebAuthn origin 名额已满，禁止再拆登录子域）。
3. **选路（免费，不用 Cloudflare LB）**：登录后在 apex 竞速探测 `hk.shell.example.com` 与 `edge-b.shell.example.com` 的 `/api/edge-probe`，跳到 RTT 更低的边缘；`localStorage.shellgate_edge` 记住偏好，刷新不抖边。长连接靠主机名粘滞，不做按键级换入口。
4. **账号记忆共享**：Tab 目录、预置、默认预置种籽记账落在 **备用门禁 `shared-postgres` 库 `shell_gate`**；两地门禁读同一库。本机 SQLite 不再作为生产真相源。
5. **不用 Redis**：不新建 Redis，也不占用备用门禁 App 那份会 LRU 淘汰的共用 Redis。
6. **跨机连库例外**：门禁节点门禁经 ZeroTier 连备用门禁 Postgres 明确破例（时延可接受）。其它服务仍禁止跨机连库。
7. **网络面板**：反映当前粘滞到的门禁视角，不做跨门禁合并。
8. **上游放行**：终端节点 / Mac 的 ttyd 防火墙同时放行 `terminal.example.com`（门禁节点）与 `terminal.example.com`（备用门禁）。

## 主机名与 Cookie

| 主机 | 职责 |
|------|------|
| `shell.example.com` | 仅登录 + 选路页；Passkey origin |
| `hk.shell.example.com` | 备用门禁门禁工作台 |
| `edge-b.shell.example.com` | 门禁节点门禁工作台 |

- 环境变量：`SHELL_COOKIE_DOMAIN=shell.example.com`，`SHELL_EDGE_HOSTS=hk=hk.shell.example.com,edge-b=edge-b.shell.example.com`（两机同配）。
- 会话 Cookie 带 `Domain=shell.example.com`，apex 签发后边缘可读；登出清除必须同 Domain。
- 未配置 `SHELL_EDGE_HOSTS` 时行为与单机一致（本地开发不用改）。

## 存储与进程模型

- 环境变量 `SHELL_DATABASE_URL`（Postgres DSN）生产必填；非法则启动失败。
- Tab/预置以数据库为唯一真相；进程内不再缓存 Tab 目录当跨请求真相。
- Mac 唤醒去重：Postgres advisory lock（按机器名）；单测 / SQLite 回退路径用进程内锁。
- `GET /healthz`：库不可达时返回 503。
- `GET /api/edge-probe`：返回 204；CORS 仅放行 apex 与已配置边缘 origin；不鉴权。

## 与 SQLite 时代的关系

- 门禁节点若仍有 `SHELL_DATA_DIR/shell-gate.db`，启动时可一次性导入空库（见迁移逻辑），之后生产只读 Postgres。
- 单测可用本地 SQLite 文件路径打开 store，避免强依赖外置库。

## 现网落地状态

- 备用门禁 `shared-postgres` 已建库 `shell_gate` / 用户 `shell_gate_app`；Postgres **仅**绑定 `terminal.example.com:5432`（ZeroTier）。
- 门禁节点与备用门禁均已跑 shell-gate，共用同一 `SHELL_DATABASE_URL`；旧 SQLite 已从门禁节点导入一次。
- 终端节点 iptables 与唤醒钥匙 `from=` 已放行 `terminal.example.com` + `terminal.example.com`。
- 备用门禁 Caddy 已挂 `shell.example.com` → `127.0.0.1:18765`。
- **选路（2026-09-09 已启用）**：放弃付费 Cloudflare Load Balancing。DNS：`shell.example.com` / `hk.shell.example.com` → 备用门禁；`edge-b.shell.example.com` → 门禁节点（灰云）。TLS：东京签发 `shell.example.com` + `*.shell.example.com`，备用门禁 Caddy 用该证；门禁节点边缘由 Caddy 自管 ACME。两机 `.env` 已写 `SHELL_COOKIE_DOMAIN` / `SHELL_EDGE_HOSTS`。
- Mac pf 仓库规则已含备用门禁；本机需管理员重装锚点后生效（`deploy/mac` 安装脚本）。

## 验收

1. 公司网登录后多数落到更近边缘；`localStorage.shellgate_edge` 有值后刷新不换边；偏好边宕机则竞速另一边。
2. 在 A 门禁开/关 Tab，B 门禁列表一致（共享库已具备）。
3. 未授权门禁主机直连上游 `:7681` 仍拒绝。
4. Passkey 只在 apex；边缘未登录 302 到 `https://shell.example.com/login`。
5. `go test -race ./...`；真机 WebSocket 握手 101。
