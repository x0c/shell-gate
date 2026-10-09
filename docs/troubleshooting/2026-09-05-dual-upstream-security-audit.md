# 双上游安全审计（Linux 上游 ttyd + Mac SSH/ttyd，示例）

> 文档定位：双上游（linux / mac）及关联 SSH 的安全审计结论、实锤与加固验收示例。
> 不覆盖：渗透测试、门禁二进制 CVE 逐条、WoL 可靠性压测。
> 以下主机名均为示例（`*.example.com`），地址与账号请按实际部署替换。

## 结论摘要

| 项 | 严重度 | 状态 |
|---|---|---|
| Mac SSH 默认允许密码 + keyboard-interactive | 高 | **已关闭**（密钥登录；复验仅剩 `publickey`） |
| Mac:22 对隧道全员可达（其它隧道节点实测 TCP 通） | 高 | **已限制**（pf 仅放行中继主机 + 家庭网段；其它节点复验超时） |
| Mac:7681 / 上游:7681 非网关可达 | — | 已拦（上游 iptables / Mac pf 实测） |
| 公网 DNAT 到 Mac 22/7681 | — | 路由器无对应转发 |
| 门禁伪造身份头 / 命令注入 | — | 反代先删客户端头与 `arg` 再注入 |
| 网关管理端口 18765 | — | 仅本机 + 容器网段，其余 DROP |
| 通行密钥 + 邮箱白名单 + Secure Cookie | — | 已配置 |

加固执行：`sudo bash deploy/mac/harden-upstream-security.sh`（管理员已跑；已复验通过）。

## 攻击面模型（简）

```text
浏览器 --HTTPS--> 网关反代 --> shell-gate:18765 --隧道--> 上游 ttyd:7681
中继助手 --钥匙 SSH--> Mac:22（应仅中继主机/家庭局域网）
```

ttyd 一旦收到非空 `X-Webauth-User` 即信任——**防火墙是硬前提**，不是加分项。

## 实锤（审计时）

1. Mac `sshd` 提供：`publickey,password,keyboard-interactive`。
2. 其它隧道节点：`nc` 连上游 SSH 端口 **成功**；`:7681` **超时**。
3. 上游主机：网关出口→7681 ACCEPT 计数很大，其余 DROP 有计数；本机打自己的隧道地址:7681 失败（符合规则）。
4. 路由器：无 WAN→Mac 的 22/7681 DNAT。
5. 门禁：`Rewrite` 删除客户端身份头与 `arg` 后写入已校验邮箱；Tab 绑定邮箱，跨用户 Tab 404。
6. Mac 远程登录 ACL：仅含示例运维账号，并嵌套 `admin`。
7. `authorized_keys` 仅 1 把中继钥匙。
8. 系统「应用程序防火墙」总开关为关——对 22 的约束改由 pf 承担，不依赖它。

## 加固动作

```bash
sudo bash deploy/mac/harden-upstream-security.sh
```

验收：

- 中继主机：SSH 钥匙成功；禁用公钥后应无法用密码完成登录。
- 其它隧道节点：`nc` 到上游 `:22` 应失败/超时；`:7681` 仍失败。
- 网关：`curl` Mac:7681 仍 200；网页开 Mac Tab 仍 101。

## 残留与不做

- 未做合盖深睡 WoL 可靠性压测；偶发叫不醒按上游文档处理。
- 未扫 ttyd / OpenSSH CVE 清单；版本随包管理 / 系统更新。
- 不关家庭局域网对 Mac:22（本机运维需要）；若外出只靠隧道，依赖「禁密码 + 仅中继钥匙」。
- 上游 `ttyd-shellgate.service` 曾显示 inactive 而进程仍在（keepalive）——可用性项，非本轮机密性主线。

<!-- 该文档整理/压缩于 2026-09-05 -->
