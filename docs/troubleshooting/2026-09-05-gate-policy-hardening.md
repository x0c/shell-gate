# 2026-09-05 shell-gate 门禁放行/配置/鉴权加固

> 何时该读：改反代 Transport、上游 URL 解析、会话 TTL / 探测间隔环境变量，或评审「放行谁、配错会不会静默变宽」前。

## 结论（已修）

1. **反代上游不得走环境 HTTP(S)_PROXY**
   `newTTYDProxy` 的 Transport 曾用 `http.ProxyFromEnvironment`。进程若带出站代理，ZeroTier 内网 ttyd 流量（含门禁注入的 `X-Webauth-User`）会被送到外部代理——放行策略错误。已改为 `Proxy: nil`；探针 Client 本来就不走环境代理。

2. **上游地址只允许 http/https，禁止 URL 内嵌账号**
   `parseMachines` 原先只要求「有 scheme + Host」，`file://` / `ftp://` / `user:pass@host` 也能启动。现由 `parseUpstreamURL` fail-fast；scheme 规范化为小写。

3. **会话 TTL / 探测间隔配错必须启动失败**
   旧 `getEnvDuration` 对非法值静默回落默认；`SHELL_SESSION_TTL=0` / 负值也能进进程（Cookie/JWT 边界错乱）。现：`parsePositiveDuration`（会话）与 `parseNonNegativeDuration`（探测，`0` 仍表示只被动统计）。

   `mintSessionToken` 在已有 `emailAllowed` 之外再拒空邮箱，避免无邮箱身份写进 Cookie。

## 已核对、未改（非缺陷或产品边界）

- 登录换发长寿命会话 JWT、Cookie 路径再验邮箱白名单、Tab 按邮箱归属、反代剥客户端伪造头再注入、剥客户端 `arg` 后服务端注入——行为正确。
- `/api/config` 公开 anon key：通行密钥登录页需要，属预期。
- 已登录用户可自选启动命令：单用户白名单门禁的产品能力，不是越权。

## 验证

`GOSUMDB=sum.golang.org go test -race ./...` 全绿（含方案/协议/TTL/Transport 新测）。

禁止在评审或文档里写利用步骤 / PoC；只写加固口径。
