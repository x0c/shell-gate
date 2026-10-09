# 在家时关闭 Mac 自动唤醒

Current standalone provider defaults and import rules are defined in [deployment](../DEPLOYMENT.md). Infrastructure choices in this earlier design are optional deployment examples, not mandatory dependencies.
> 状态：产品口径已裁定；门禁实现已合入；**生产选路、presence 闸门、Mac pf（含备用门禁）与真连验收 1–3 已于 2026-09-09 收口**。Harbor 不可达 fail-open 仅单测覆盖，不另排生产掐链路。

## 产品口径（2026-09-09）

- Mac 仍按 [Mac 上游指南](../MAC_UPSTREAM_GUIDE.md) 做按需唤醒：仅用户主动切到 Mac / 开 Mac shell / 点绑定 Mac 的预置时，才允许经终端节点家庭网发魔术包。
- **新增**：若家里判定用户「在家」，则 **禁止** 一切 ShellGate 发起的网络唤醒；Mac 想睡就睡。已在线（探测通）仍可直接连，只拦「离线 → 发魔术包」这一步。
- 在家时点 Mac 应得到明确提示（不要伪装成「未接电 / 关机」）。

## 在家信号

| 候选 | HA 实体 | Harbor 字段 | 含义 |
|---|---|---|---|
| **采用** | `input_boolean.shu_gua_zai_jia` | `comfort_home` | 舒适在家：围栏进/出家半径意图 |
| 不用作本闸 | `input_boolean.max_shi_fou_zai_jia` | `network_home` | 网络在家：手机局域网 Ping |

选舒适在家的原因：

1. 出家半径后该开关会关 → 人已离开、手机忘在家也能远程唤醒。
2. 进家半径后打开 → 与「人在家、Mac 随便睡」一致。
3. 门禁不直连家庭控制面；走已有 Harbor `GET /v1/presence`（权威说明见 Harbor `PRESENCE_HOME_ASSISTANT_GUIDE.md`）。

若日后要改成「仅手机在家里网时抑制」，只换字段即可，不必改唤醒链路。

## 行为

```
用户连接 Mac
  → TCP 探测 ttyd
    → 已通：直接 ready（不查在家、不唤醒）
    → 不通：
        → 查 Harbor presence.comfort_home
          → true：拒绝唤醒，返回「在家…」文案
          → false / 查询失败：照旧执行 SHELL_WAKE_COMMANDS（查询失败 fail-open，打日志，避免 Harbor 挂掉时人在外叫不醒）
```

约束不变：健康探测、隐藏 Tab 重连、定时任务仍不得调用唤醒。

## 配置（门禁节点 / 备用门禁门禁各一份）

```bash
# 可选；未配置则行为与改造前相同
SHELL_WAKE_HOME_PRESENCE_URL=http://terminal.example.com:18780/v1/presence
SHELL_WAKE_HOME_PRESENCE_TOKEN=<Harbor 长期令牌，勿入库>
SHELL_WAKE_HOME_PRESENCE_MACHINES=mac
```

- URL + Token 必须成对；只配一半启动失败。
- `MACHINES` 逗号分隔，须已在 `SHELL_TTYD_TARGETS` / 唤醒表里；未知名 fail-fast。
- 拨号走 ZeroTier 到终端节点 Harbor（`terminal.example.com:18780`），不要挂公网家庭控制面。

## 实现落点

- 门禁：`upstreamWaker.ensureReady` 在执行唤醒命令前查 presence；`errors.Is(..., ErrWakeSuppressedAtHome)` 映射到 `/ready` 与建 Tab 的明确 `message`。
- 不改终端节点魔术包脚本本身（门禁已拦）；日后若要双重保险，再在 `shell-gate-wake-mac` 加本地 HA 读。
- 前端继续展示 API `message`，无需新交互层。

## 验收

1. [x] 舒适在家=off、Mac 不可达：点 Mac → 仍自动唤醒（2026-09-09 真连：`POST /api/machines/mac/ready` → `woke:true`）
2. [x] 舒适在家=on、Mac 不可达：点 Mac → 不发魔术包，提示在家已关唤醒（同日真连：0.4s 内 503，文案含「在家」）
3. [x] 舒适在家=on、Mac 已醒：点 Mac → 直接连上（同日真连：`ready:true,woke:false`）
4. [ ] Harbor 不可达：打日志后仍尝试唤醒（fail-open）— 单测已覆盖；生产未刻意掐链路
5. [x] 单测覆盖：在家抑制 / 不在家放行 / 查询失败放行 / 已在线不查

## 生产收尾

- [x] 复用 Harbor `HARBOR_TOKEN`，写入门禁节点与备用门禁 `SHELL_WAKE_HOME_PRESENCE_*`（2026-09-09）
- [x] Harbor `:18780` 白名单已放行门禁节点 `terminal.example.com` / 备用门禁 `terminal.example.com`（否则 presence 超时、fail-open 等于没闸）
- [x] 双入口已重启；本机 `edge-probe`/`healthz` 与 presence 读通
- [x] Mac 上线后按上表 1–3 真连验收（停 ttyd 模拟离线，未整机睡眠）
- [x] Mac pf 本机锚点已含备用门禁 `terminal.example.com`（2026-09-09 管理员重装；门禁节点/备用门禁 TCP 探 7681 均通）
