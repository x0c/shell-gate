# Changelog

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
