# 双上游安全审计（终端节点 ttyd + Mac SSH/ttyd）

> 文档定位：2026-09-05 双上游（linux / mac）及关联 SSH 的安全审计结论、实锤与加固验收。
> 不覆盖：渗透测试、门禁二进制 CVE 逐条、WoL 可靠性压测。

## 结论摘要

| 项 | 严重度 | 状态 |
|---|---|---|
| Mac SSH 默认允许密码 + keyboard-interactive | 高 | **已关闭**（`110-key-only.conf`；2026-09-05 复验仅剩 `publickey`） |
| Mac:22 对 ZeroTier 全员可达（oregon/tokyo 实测 TCP 通） | 高 | **已限制**（pf 仅终端节点 `terminal.example.com` + 家庭网 `the home LAN subnet (address: see AgentSync INFRA_KNOWLEDGE_BASE)`；oregon/tokyo 复验超时） |
| Mac:7681 / 终端节点:7681 非门禁节点可达 | — | 已拦（终端节点 iptables / Mac pf 实测） |
| 公网 DNAT 到 Mac 22/7681 | — | 路由器无对应转发 |
| 门禁伪造身份头 / 命令注入 | — | 反代先删客户端头与 `arg` 再注入 |
| 门禁节点门禁 18765 | — | 仅本机 + Docker 网段，其余 DROP |
| 通行密钥 + 邮箱白名单 + Secure Cookie | — | 已配置 |

加固执行：`sudo bash ~/Codes/shell-gate/deploy/mac/harden-upstream-security.sh`（用户 2026-09-05 已跑；Agent 同日复验通过）。

## 攻击面模型（简）

```text
浏览器 --HTTPS--> 门禁节点 Caddy --> shell-gate:18765 --ZT--> 上游 ttyd:7681
终端节点助手 --钥匙 SSH--> Mac:22（应仅终端节点/家里局域网）
```

ttyd 一旦收到非空 `X-Webauth-User` 即信任——**防火墙是硬前提**，不是加分项。

## 实锤（审计时）

1. Mac `sshd` 提供：`publickey,password,keyboard-interactive`。
2. oregon / tokyo：`nc` 连 `mac.example.com:22` **成功**；`:7681` **超时**。
3. 终端节点：`terminal.example.com→7681` ACCEPT 计数很大，其余 DROP 有计数；本机打自己的 ZT:7681 失败（符合规则）。
4. 路由器：无 WAN→Mac 的 22/7681 DNAT；存在 BT `58291`→Mac（与本审计无关，另册）。
5. 门禁：`Rewrite` 删除客户端身份头与 `arg` 后写入已校验邮箱；Tab 绑定邮箱，跨用户 Tab 404。
6. Mac 远程登录 ACL：`com.apple.access_ssh` 含 `the Mac login user`，并嵌套 `admin`（本机 admin 成员为 root + the Mac login user）。
7. `authorized_keys` 仅 1 把终端节点 shellgate 钥匙。
8. 系统「应用程序防火墙」总开关为关——对 22 的约束改由 pf 承担，不依赖它。

## 加固动作

```bash
sudo bash ~/Codes/shell-gate/deploy/mac/harden-upstream-security.sh
```

验收：

- 终端节点：`ssh mac` 钥匙成功；`ssh -o PubkeyAuthentication=no mac` 应无法用密码完成登录。
- oregon/tokyo：`nc` 到 `mac.example.com:22` 应失败/超时；`:7681` 仍失败。
- 门禁节点：`curl` Mac:7681 仍 200；网页开 Mac Tab 仍 101。

## 残留与不做

- 未做合盖深睡 WoL 可靠性压测；偶发叫不醒仍按基建文档处理。
- 未扫 ttyd / OpenSSH CVE 清单；版本随 Homebrew / 系统更新。
- 不关家里局域网对 Mac:22（本机运维需要）；若外出只靠组网，依赖「禁密码 + 仅终端节点钥匙」。
- 终端节点 `ttyd-shellgate.service` 曾显示 inactive 而进程仍在（keepalive）——可用性项，非本轮机密性主线。

<!-- 该文档整理/压缩于 2026-09-05 -->
