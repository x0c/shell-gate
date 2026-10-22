# Changelog

## v0.3.0

### English

- Add optional per-account TOTP 2FA with local QR/manual setup, confirmation before activation, ten one-use recovery codes, regeneration and disable controls in Account security.
- Require the second step across password, passkey, OIDC and external-token sign-ins once enabled. Protect against replay and concurrent reuse with database-backed consumption, lockouts and version-bound sessions. Password recovery preserves 2FA.
- Encrypt authenticator seeds, audit factor changes and provide a local operator reset command. Document key backup and shared-database operation. Return a temporary service error for authentication-storage outages and reject unsupported local IP origins during configuration checks.

### 简体中文

- 新增用户自行开启的 TOTP 两步验证，支持本地生成二维码和手动密钥、确认动态码后开启、十个一次性恢复码，以及账号安全窗口内的恢复码更新和关闭操作。
- 开启后，密码、通行密钥、OIDC 和外部令牌登录均需第二步验证。数据库统一防重复使用与并发消费，持久化失败次数，并通过版本校验撤销旧会话；密码恢复保留两步验证。
- 加密认证器密钥、审计安全变更，提供本地管理员重置命令，补充密钥备份和共享数据库说明。认证存储故障返回服务暂不可用，配置检查提前拒绝本地登录不支持的 IP 访问地址。

## v0.2.0

### English

- Add optional generic OIDC SSO with Discovery, Authorization Code + S256 PKCE, maintained token verification, exact email allowlists and subject-bound UserInfo. Local password and passkey login remain available in local mode.
- Harden email verification, authorized-party/subject checks, provider HTTP transport, login-state cleanup and provider-bound gateway sessions; support apex callbacks and same-edge origins. Pass SSO settings through Compose.
- Give SSO a clear primary sign-in action, visible provider context, redirect feedback, back-navigation recovery and retryable configuration errors; retain the existing desktop/mobile layout.
- Require Go 1.26.10 for the crypto/x509 certificate-distrust security fix.

### 简体中文

- 新增可选通用 OIDC SSO，支持 Discovery、授权码与 S256 PKCE、成熟库令牌校验、精确邮箱白名单及绑定同一账号的 UserInfo。本地模式继续提供密码和通行密钥登录。
- 加固邮箱验证、授权方与账号声明、身份源 HTTP 传输、登录状态清理和绑定身份源的门禁会话；支持 apex 回调和边缘同站请求，Compose 传递 SSO 配置。
- 突出 SSO 主登录入口，显示身份源信息，提供跳转反馈、浏览器返回恢复及配置加载失败重试，保留现有桌面与手机布局。
- 构建要求升级至 Go 1.26.10，包含 crypto/x509 证书信任安全修复。

## v0.1.5

### English

- Log once when all upstream machines become unreachable at the same time (and once on recovery), pointing at the edge egress path instead of each upstream; expose the latest probe error per machine in `GET /api/machines` so timeouts, refusals and auth rejections are distinguishable without reading gateway logs.
- Add an edge-egress troubleshooting record for a single edge losing all machines at once while another edge stays healthy (two-ended packet capture, conntrack double sampling, underlay protocol matrix).

### 简体中文

- 全部上游机器同时不可用时记一行日志（含机器名，恢复时再记一行），直接指向边缘出口方向而非逐台排查上游；`GET /api/machines` 每个条目新增最近探测错误文本，超时、拒绝、鉴权失败无需翻网关日志即可区分。
- 新增单边缘全部机器同时变红、另一边缘正常的排障记录（两端抓包、conntrack 双采样、underlay 协议矩阵）。

## v0.1.4

### English

- Keep embedded terminals fitted after browser zoom and asynchronous renderer changes; align mouse clicks and text selection with the stretched character grid.
- Clarify Mac wake-failure diagnostics and the storage/swap exhaustion incident.

### 简体中文

- 修复网页缩放及渲染尺寸异步变化后的终端填充，确保点击和文字选区与画面中的字符对齐。
- 补充 Mac 唤醒失败的诊断方法及磁盘、交换空间耗尽事件的归因。

## v0.1.3

### English

- Generalize deployment documentation: site-specific topology names, home-automation entity names, production paths and account details are replaced with generic gateway/relay/presence roles and example paths. No behavior change.

### 简体中文

- 泛化部署文档：站点拓扑实名、家庭自动化实体名、生产路径和账号细节全部换成通用网关/中继/presence 角色与示例路径。无行为变更。

## v0.1.2

### English

- Normalize release archive metadata to exclude the build account and make package timestamps reproducible.

- Keep valid zero-duration TCP samples and enforce portable source checkouts and executable-hook validation.

- Standalone email/password sign-in, optional passkeys, one-use operator invitations, password recovery and persistent sessions.
- Default SQLite storage, optional PostgreSQL and external GoTrue adapters; no private module checkout or browser CDN.
- Optional Docker Compose gateway + ttyd deployment and HTTPS override.
- Account-isolated workspace/diagnostics, transactional audit records, stricter origins and upstream credential stripping.
- Modular server boundaries, reusable deployment scripts, privacy-reviewed public source, platform installers and checksummed local release packages.
- Preserve per-machine tab selection, persistent tabs, interactive login shells, canvas rendering and mobile keyboard behavior.

### 简体中文

- 清理发行包中的构建账号信息，并固定打包时间，便于复现与核验。

- 保留有效的零耗时 TCP 样本，并统一跨平台源码换行与可执行脚本校验。

- 提供本地邮箱密码登录、可选通行密钥、一次性邀请、密码恢复和持久会话，可以独立部署。
- 默认使用 SQLite，可选 PostgreSQL 和外部 GoTrue 适配器；不依赖私有模块或浏览器 CDN。
- 提供可选的 Docker Compose 门禁与 ttyd 完整部署，以及 HTTPS 配置。
- 隔离账号工作台和诊断数据，数据修改同步记录审计，严格校验来源并移除上游转发中的门禁凭据。
- 拆分服务端职责，提供可复用部署脚本、经过隐私审查的公开源码、跨平台安装器和带校验和的本地发行包。
- 保留按机器切换与恢复 Tab、持久 Tab、交互登录壳、canvas 渲染和手机键盘行为。
