# 双层顶栏：终端 iframe 嵌进了工作台

> 2026-09-08。排查「登录后点预置出现两层顶栏 / 双层 title bar」前读。

## 症状

工作台顶栏下方再叠一整条同样的机器 pill + 启动器 + Tab 条；上下两层选中的 Tab 可以不同（例如外层 `corral`、内层 `shell`），内层内容往往是普通 shell 提示符。

## 根因

终端 iframe 实际加载的是**工作台自己**（`/`），不是 `/t/{id}/` 反代的 ttyd。

已证实：`GET /?rendererType=canvas&disableReconnect=true`（带登录 Cookie）会返回完整 `workspace.html`。若 iframe 的 `src` 变成相对地址 `?rendererType=…`（典型原因：`tab.path` 为空，拼接 `tab.path + "?rendererType=…"`），浏览器相对当前页 `/` 解析成 `/?…`，于是工作台嵌进 iframe，再画一套顶栏。

另一条次要路径：iframe 里走到 `/login`，而已登录时登录页会 302 到 `/`，同样嵌进工作台。

## 不是

- 顶栏 `flex-wrap` 换行（换行不会复制一整排机器/启动器）
- 桌面顶栏被拆成「启动一行 + Tab 一行」（那是另一条已裁定禁止的视觉回归）

## 修法（已落地）

1. 工作台 `/`、登录页：`Content-Security-Policy: frame-ancestors 'none'` + `X-Frame-Options: DENY`；带 `Sec-Fetch-Dest: iframe|embed|object` 的请求直接 403，不再下发工作台 HTML。
2. `workspace.html`：若 `window.top !== window.self` 立即中止，禁止在 iframe 里再启工作台。
3. `mountTabPane`：只接受 `/t/{id}/` 形态的 `path`，否则 toast 并拒绝挂载。

守卫：`TestRootRejectsIframeEmbedding`、`TestLoginDeniesFraming`、`TestWorkspaceHTMLGuardsAgainstNestedShellAndInvalidTabPath`。

## 验收

1. 顶层打开 `/` 仍是正常工作台。
2. 用带 Cookie 的请求模拟 iframe 拉 `/?rendererType=canvas` → 403，正文无启动器。
3. 正常 `/t/{id}/` 仍返回 ttyd HTML（可含 enhance.js）。
4. 真机：登录 → 点 corral → 只有一层顶栏。
