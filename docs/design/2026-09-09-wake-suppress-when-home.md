# 在家时关闭 Mac 自动唤醒

Current standalone provider defaults and import rules are defined in [deployment](../DEPLOYMENT.md). Infrastructure choices in this earlier design are optional deployment examples, not mandatory dependencies.
> 状态：产品口径已裁定；门禁实现已合入；多实例选路、presence 闸门、Mac pf 与真连验收 1–3 已收口。presence 服务不可达 fail-open 仅单测覆盖，不另排生产掐链路。

## 产品口径（2026-09-09）

- Mac 仍按 [Mac 上游指南](../MAC_UPSTREAM_GUIDE.md) 做按需唤醒：仅用户主动切到 Mac / 开 Mac shell / 点绑定 Mac 的预置时，才允许经中继主机家庭网发魔术包。
- **新增**：若 presence 判定用户「在家」，则 **禁止** 一切 ShellGate 发起的网络唤醒；Mac 想睡就睡。已在线（探测通）仍可直接连，只拦「离线 → 发魔术包」这一步。
- 在家时点 Mac 应得到明确提示（不要伪装成「未接电 / 关机」）。

## 在家信号

| 候选 | 家庭自动化侧 | presence 字段 | 含义 |
|---|---|---|---|
| **采用** | 在家开关（示例） | `comfort_home` | 以围栏进出意图为准的"在家" |
| 不用作本闸 | 局域网在线信号（示例） | 其它字段 | 仅反映设备是否连着家里局域网 |

选前者的原因：

1. 离开围栏后该开关会关 → 人已离开、手机忘在家也能远程唤醒。
2. 进入围栏后打开 → 与「人在家、Mac 随便睡」一致。
3. 门禁不直连家庭控制面；走已有的 presence 服务 `GET /v1/presence`（字段映射由部署方在 presence 服务侧定义）。

若日后要改成「仅手机在家里网时抑制」，只换字段即可，不必改唤醒链路。

## 行为

```
用户连接 Mac
  → TCP 探测 ttyd
    → 已通：直接 ready（不查在家、不唤醒）
    → 不通：
        → 查 presence 服务的 comfort_home
          → true：拒绝唤醒，返回「在家…」文案
          → false / 查询失败：照旧执行 SHELL_WAKE_COMMANDS（查询失败 fail-open，打日志，避免 presence 服务挂掉时人在外叫不醒）
```

约束不变：健康探测、隐藏 Tab 重连、定时任务仍不得调用唤醒。

## 配置（各网关实例各一份，示例）

```bash
# 可选；未配置则行为与改造前相同
SHELL_WAKE_HOME_PRESENCE_URL=http://terminal.example.com:18780/v1/presence
SHELL_WAKE_HOME_PRESENCE_TOKEN=<presence 服务长期令牌，勿入库>
SHELL_WAKE_HOME_PRESENCE_MACHINES=mac
```

- URL + Token 必须成对；只配一半启动失败。
- `MACHINES` 逗号分隔，须已在 `SHELL_TTYD_TARGETS` / 唤醒表里；未知名 fail-fast。
- presence 服务不要挂公网；示例走隧道到中继侧的服务地址。

## 实现落点

- 门禁：`upstreamWaker.ensureReady` 在执行唤醒命令前查 presence；`errors.Is(..., ErrWakeSuppressedAtHome)` 映射到 `/ready` 与建 Tab 的明确 `message`。
- 不改中继侧魔术包脚本本身（门禁已拦）；日后若要双重保险，再在唤醒脚本侧加本地 presence 读。
- 前端继续展示 API `message`，无需新交互层。

## 验收

1. [x] 在家=off、Mac 不可达：点 Mac → 仍自动唤醒（真连：`POST /api/machines/mac/ready` → `woke:true`）
2. [x] 在家=on、Mac 不可达：点 Mac → 不发魔术包，提示在家已关唤醒（真连：0.4s 内 503，文案含「在家」）
3. [x] 在家=on、Mac 已醒：点 Mac → 直接连上（真连：`ready:true,woke:false`）
4. [ ] presence 服务不可达：打日志后仍尝试唤醒（fail-open）— 单测已覆盖；生产未刻意掐链路
5. [x] 单测覆盖：在家抑制 / 不在家放行 / 查询失败放行 / 已在线不查

## 上线检查表（部署方按实际环境执行）

- [ ] presence 令牌写入各网关实例的 `SHELL_WAKE_HOME_PRESENCE_*`（勿入库）
- [ ] presence 服务放行各网关出口（否则查询超时、fail-open 等于没闸）
- [ ] 双入口重启后 `edge-probe`/`healthz` 与 presence 读通
- [ ] Mac 上线后按上表 1–3 真连验收（停 ttyd 模拟离线，未整机睡眠）
- [ ] Mac pf 本机锚点含各网关出口（管理员重装后，两实例 TCP 探 7681 均通）
