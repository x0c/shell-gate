# 前端视觉重绘

## Shared UI kit rewrite (2026-11-26)

The owner requested a frontend rewrite using the shared UI system. Migrate the login, edge selection and workspace surfaces to React 19 + TypeScript + Vite with the public, immutable `@x0c/ui@0.3.1` / `@x0c/tokens` distribution. Do not introduce the private `@caozc/brand` package, remote fonts, a browser CDN, an external runtime service or a neighboring-checkout dependency. The Go binary continues to embed the compiled assets; `enhance.js` remains the standalone compatibility script injected into ttyd rather than part of the React application.

Inspected references and reuse boundary:

- `SharedPlatform/web-ui-kit/templates/admin-starter` and `agent-dbctl/web`: reuse the React 19 + Vite + Tailwind v4 single-binary integration and release build path.
- `@x0c/ui` `Button`, `Input`, `Select`, `DropdownMenu`, `Dialog`, `FormField`, `Notice`, `LoadingButton`, `Panel`, `InsetList`, `Row` and `Toaster`: reuse their public component behavior, semantic tokens, focus handling and portal-based overlays. `AppTopbar`/`AdminShell` do not fit ShellGate's adopted single-line launcher + terminal-tab toolbar, so the workspace keeps its product-specific compact composition instead of adding a sidebar or a second navigation row.
- `agent-dbctl/internal/webui` and its Dockerfile: reuse immutable hashed-asset serving and the Node → Go build-stage pattern; ShellGate keeps its separate login, edge-pick and workspace HTML entry points and existing authentication routes.

The workspace uses Flat Monochrome semantic colors, compact `h-8` controls, `rounded-md` peers and terminal-content-first density. Keep equal-width terminal tabs, one-line horizontal overflow, direct one-click launchers, narrow-screen launch/more menus, non-modal network diagnostics, account security secret cleanup, all authentication modes and the uncommitted copy corrections already present in `workspace.html` (`Theme`, `Follow selection …)`, accessible `×` delete and the corrected empty state). Login and edge selection retain their adopted fixed dark appearance; workspace `auto`/`light`/`dark` remains the only theme source and continues to broadcast to mounted terminal iframes without reloading them.

Source lives under `web/`; generated HTML and hashed assets under `internal/server/web/` are committed so ordinary Go tests and direct source builds do not need Node. Docker and release builds run the reproducible `npm ci` + typecheck + Vite build path. Existing browser-state regression coverage was migrated from inline-HTML script extraction to executable TypeScript/component checks (Vitest) and real-browser Playwright checks against `ttyd` + the Go gateway. Key integration fixes applied during verification: `[data-radix-popper-content-wrapper]` is raised (`z-index: 50`) above `.terminal-stage` and `.menu-scrim`, mobile `pointerdown` terminal-focus preservation runs in the bubble phase with non-modal menu `onFocusOutside` and guarded `onCloseAutoFocus` focus handoff, `PresetDialog` avoids duplicate close buttons and ignores clicks on `[data-sonner-toaster]` Undo actions, and the Vitest `HTMLIFrameElement.prototype.contentWindow` stub avoids circular parent-window focus loops on unmount. Physical mobile keyboard and biometric passkey behavior remain separate coverage limits.

External research limitation: initial shared-kit selection relied on the named local authoritative source (`SharedPlatform/web-ui-kit`) because the prior session's Tavily lookup failed with HTTP 432. `go.mod` and `Dockerfile` were subsequently updated to Go 1.26.11 (`GO-2026-6859` resolved), leaving 0 reachable vulnerabilities under `govulncheck ./...`.

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

Reuse the existing `internal/server/web/login.html` identity column, fixed graphite palette, thin separators and 48px controls. Keep desktop side-by-side and mobile stacked geometry. The actual password submit button and local setup disclosure were inspected as the component references; SSO is the only primary action only in an explicitly configured SSO-only deployment and uses the same public HTML button behavior, with stronger filled contrast. Do not add unrelated account controls, provider logos or a settings surface.

Load authentication settings before showing mode-specific actions: never flash a passkey action in SSO mode. Group the sign-in heading, provider context, primary action and actionable error in one column. Show immediate redirect feedback, prevent duplicate activation, and restore the control on browser back/forward-cache return. Configuration failure replaces loading with a working retry action. Preserve keyboard focus visibility, live error announcements, 320px layout and enlarged text. SSO errors offer recovery without exposing provider diagnostics or retaining OAuth parameters in the page URL. The local password, setup and passkey paths remain available in local mode. Reference behavior: [MDN pageshow](https://developer.mozilla.org/en-US/docs/Web/API/Window/pageshow_event) and [AbortSignal.timeout](https://developer.mozilla.org/en-US/docs/Web/API/AbortSignal/timeout_static).

Browser acceptance uses owner-authorized local Playwright when the in-app browser is unavailable. The login page and Account security were opened in Chromium at desktop, 390px and 320px widths, with screenshots inspected. Keep the real provider login and terminal path separate from isolated fixture checks; physical mobile keyboard/hardware behavior still requires device acceptance.

When SSO is added to an existing deployment, keep the original Passkey action visible and working. Add a distinct “Sign in with SSO” button using the inspected login page's existing secondary button style and spacing; both remain in the same authentication column on desktop and mobile. Do not reuse or relabel the Passkey button for this optional channel. Provider failure must leave the independent Passkey route available.

Deployed additive-channel acceptance completed the existing external GoTrue passkey ceremony with a browser virtual authenticator, a real configured OIDC provider login, second-factor recovery proofs after both routes, and the resulting shared factor state read from both gateway edges. The installed release binary also passed the isolated QR/enrollment and desktop/mobile UI journey. Physical-phone biometric prompts remain outside virtual-authenticator acceptance. Gateway-session acceptance does not establish the health of an independent ttyd upstream.

## Account security and TOTP

Account security is an explicit user-opened surface, reachable from the desktop toolbar and narrow-screen More menu. Reuse the inspected workspace preset panel's `.dialog`, field and action styling, with a standard HTML `<dialog>` opened by `showModal()` so the browser owns focus containment, inert background and Escape dismissal while terminal iframes remain mounted. The inspected [MDN modal dialog example](https://developer.mozilla.org/en-US/docs/Web/HTML/Reference/Elements/dialog#creating_a_modal_dialog) supplies the public open/close behavior; retain ShellGate's palette and geometry, not its example decoration. The surface never opens proactively. Provide a visible Close action, Escape dismissal and focus restoration; keep keyboard input inside the modal while it is open. Add no unrelated account settings.

Show one enrollment stage at a time: disabled status → local QR/manual setup key plus code confirmation → one-time recovery-code display → enabled status. Keep QR and manual key visible together for cross-device and same-device setup. Activation is complete only after code confirmation; recovery codes can be copied or downloaded locally and are not retrievable later. Enabled status shows recovery-code count and explicit regenerate/disable actions requiring a current code. Expired or invalidated authentication offers an explicit Sign in again action; a session error is not a retry loop. Clear sensitive DOM content when closing the surface.

The second login step reuses the inspected login form and primary button styles. Show one code field with `autocomplete="one-time-code"`, a numeric keyboard for authenticator codes and an explicit recovery-code alternative. Do not issue a usable gateway cookie before this step succeeds. Show invalid/expired/locked states and an explicit return to sign-in without automatically dismissing errors or auto-submitting partial input. Never keep setup keys, OTPs or recovery codes in browser storage.

TOTP browser acceptance covers explicit modal opening, confirmed enrollment, one-time recovery display and copy/download, Escape dismissal, focus containment/restoration, narrow-screen menu access, secret cleanup, invalid-code feedback and recovery login. These checks exercise the real gateway binary and browser; rendered screenshots were inspected at 1280px, 390px and 320px widths, including a 568px-high viewport and light/dark appearance. The displayed QR was decoded and its six-digit/30-second otpauth parameters matched the successful enrollment proof. Physical-phone authenticator and keyboard behavior are separate from viewport and protocol acceptance.

Account-security status loading text must clear when status arrives; a completed status must not retain a loading announcement. Account-security actions on narrow screens use at least 44 CSS pixels of hit height, while retaining the workspace palette and existing field/action styling. Native dialog close events are asynchronous; acceptance waits for the close event before asserting cleared secrets and restored focus.

The Account security title and Close action remain visible while a short viewport scrolls the enrollment or recovery content. Keep them in the same native dialog using a sticky panel-colored header.
