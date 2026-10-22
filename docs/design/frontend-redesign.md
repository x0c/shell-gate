# 前端视觉重绘

## 产品要求

用户要求重绘前端，消除现有界面的 AI 模板感，授权自由设计。覆盖登录页与终端工作台的桌面、手机视觉。保留既有终端、通行密钥、多机器、主题切换与快捷操作行为。

## 设计方向

采用中性石墨与纸白、细分隔线、紧凑工具栏、有限圆角和清晰字阶。删除光晕、扫描线、渐变文字与重复说明；终端内容占据主体。宽屏启动器继续一点即开，窄屏收纳规则保持。网络面板仍是不阻断输入的小窗。

**【裁定 2026-09-05】桌面顶栏单行**：用户反馈启动区与 Tab 条两行占高过大。宽屏改为同一行——左机器/启动器、中 Tab（吃掉余量）、右主题/网络/预置；控件再压矮一截。禁止为「整齐两行」把顶栏重新拆开。窄屏仍是 ☰ + Tab + ⋯。Tab 增多时只横向滚动，**禁止**把顶栏撑高或换行（`.top` 行内 `overflow: hidden` + Tab 区 `flex: 1 1 0%`）；新开 Tab 后高度抖动即违反本条。**下拉菜单不得放进该 `overflow:hidden` 行内**（会裁掉，手机 ☰/⋯ 像失灵）；菜单挂在 `.top-shell` 兄弟层。

键盘焦点保留可见样式，依照 [MDN focus-visible](https://developer.mozilla.org/en-US/docs/Web/CSS/:focus-visible)；深浅色仍由父页统一决定，依照 [MDN prefers-color-scheme](https://developer.mozilla.org/en-US/docs/Web/CSS/@media/prefers-color-scheme)。

## 验收口径

桌面顶栏单行紧凑；手机保持单行导航（左右菜单收纳）。登录页桌面采用左右排版，手机上下排列，保持固定深色。自动登录、命令包装、重连、终端配色与键盘桥接脚本不因视觉重绘改写。手机终端会话保持软键盘打开、键盘升起后锁高度，见根 `AGENTS.md` 2026-09-14 裁定。

已用本机真实 Go 门禁与 ttyd、Chrome 验证终端输入、浮窗内继续输入、主题切换、预置创建、开关标签、手机菜单及 390px 无横向溢出；已检查桌面/手机截图，Go race 测试通过。手机验证是浏览器尺寸模拟，不等同于实机软键盘验收；通行密钥代码保持原样，未代替用户完成生物识别。

<!-- 该文档整理/压缩于 2026-09-05 -->

## Generic SSO login review

Reuse the existing `internal/server/web/login.html` identity column, fixed graphite palette, thin separators and 48px controls. Keep desktop side-by-side and mobile stacked geometry. The actual password submit button and local setup disclosure were inspected as the component references; SSO is the only primary action in its mode and uses the same public HTML button behavior, with stronger filled contrast. Do not add unrelated account controls, provider logos or a settings surface.

Load authentication settings before showing mode-specific actions: never flash a passkey action in SSO mode. Group the sign-in heading, provider context, primary action and actionable error in one column. Show immediate redirect feedback, prevent duplicate activation, and restore the control on browser back/forward-cache return. Configuration failure replaces loading with a working retry action. Preserve keyboard focus visibility, live error announcements, 320px layout and enlarged text. SSO errors offer recovery without exposing provider diagnostics or retaining OAuth parameters in the page URL. The local password, setup and passkey paths remain available in local mode. Reference behavior: [MDN pageshow](https://developer.mozilla.org/en-US/docs/Web/API/Window/pageshow_event) and [AbortSignal.timeout](https://developer.mozilla.org/en-US/docs/Web/API/AbortSignal/timeout_static).

Validation boundary for the SSO review: page-script regression tests exercise initial loading, SSO redirect feedback, back/forward-cache restoration, configuration failure/retry, error-parameter cleanup and the local-mode controls. The current execution environment exposes no browser surfaces; Chrome and the in-app browser both report unavailable, and window enumeration found no Chrome window. Desktop/mobile screenshots, actual keyboard focus and physical-device interaction remain unverified. Do not describe these script tests as visual acceptance. A future acceptance pass should open `/login` and its error states at desktop, 390px and 320px widths and enlarged text, then inspect captures and the real sign-in/return path.
