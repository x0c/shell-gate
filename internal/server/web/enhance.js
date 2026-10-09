// shell-gate Terminal增强脚本：由反代注入进 ttyd 页面。
// 职责：修复中文全角标点丢失、截图存入剪贴板、图片Paste进内嵌 agent、
// OSC 52 / 手动选区Copy桥接、手机端键盘避让/触摸滚动/快捷键栏，
// WebGL 截图兼容（preserveDrawingBuffer）、TUI 乱码时重建字符贴图、
// 以及诊断版卡顿采样（perf-sample → /__shellgate/debug-log）。
// 只在真正的Terminal页面（存在 window.term）里生效；登录页等其他 HTML 会静默跳过。
(function () {
  "use strict";

  // WebGL 默认合成后清空绘制缓冲，drawImage/toDataURL 会得到全黑；
  // 截图依赖读回缓冲，所以必须在 xterm 创建 WebGL 上下文之前打开
  // preserveDrawingBuffer。脚本插在 </body> 前同步执行，早于 ttyd 拿完
  // WS 握手配置后才建渲染器——时序够用。对照实验（2026-08-05）：
  // canvas 渲染 P50 70.7ms → webgl+此开关 26.5ms，截图非黑正常；
  // 与裸 webgl（27.0ms）无差别，Terminal尺寸下「每帧多拷一次」的理论代价测不到。
  (function forcePreserveDrawingBufferForWebGL() {
    var orig = HTMLCanvasElement.prototype.getContext;
    HTMLCanvasElement.prototype.getContext = function (type, attrs) {
      if (type === "webgl" || type === "webgl2" || type === "experimental-webgl") {
        attrs = Object.assign({}, attrs || {}, { preserveDrawingBuffer: true });
      }
      return orig.call(this, type, attrs);
    };
  })();

  // 与 Corral embed 的跨仓库约定：发出 CORRAL_ 这对（U+241E 包裹）。
  // Corral 解析时仍认旧的 PICKUP_ 对，所以旧 CLI + 新门禁也能通；反向则靠 CLI 回退。
  var IMG_SENTINEL_BEGIN = "␞CORRAL_IMG_BEGIN␞";
  var IMG_SENTINEL_END = "␞CORRAL_IMG_END␞";
  var MAX_IMAGE_BYTES = 500 * 1024;
  // 两行按键（36*2）+ 行间距4 + 上下内边距12，正好等于工具栏实际高度，不额外留底部空白。
  var MOBILE_TOOLBAR_MIN_HEIGHT = 88;
  var PERF_SAMPLE_INTERVAL_MS = 5000;
  // 一次按键的回显最多认这么久：超过就认定「这次按键没有回显」，Samples丢弃而不是记一个
  // 错值（见 isTerminalQueryOnlyFrame）。600ms 的依据：纯Network往返实测约 30ms，
  // Terminal host重度排队时也在 400ms 以内，留一倍余量足够容纳真实劣化。
  var ECHO_ATTRIBUTION_MS = 600;

  // ---------- 深Light主题：运行时换肤，不重连、不打断正在跑的程序 ----------
  // 父页（workspace.html）是配色唯一真相源。这里只做两件事：
  //   1. 首帧前从自己 URL 上已带的 theme 查询参数（父页拼 iframe 地址时写进去的）
  //      解出初始配色，避免先按Dark默认画一帧再被 postMessage 改成Light的闪烁；
  //   2. 之后收到父页 postMessage 广播的新配色时，直接改 term.options.theme——
  //      xterm 5.4.0 支持运行时换肤，颜色立即重绘，不需要重载整个Terminal。
  // "背景色" 单独用一对 CSS 变量驱动Terminal网格之外的贴边留白（installTerminalBleedStyles/
  // installMobileStyles 里那几处 background:#000!important），否则Light下右侧/底部会露黑边。
  var CHROME_VARS_DARK = {
    "--sg-chip-bg": "rgba(12,14,13,0.94)",
    "--sg-chip-border": "rgba(255,255,255,0.12)",
    "--sg-key-bg": "rgba(255,255,255,0.08)",
    "--sg-key-border": "rgba(255,255,255,0.18)",
    "--sg-key-fg": "#e9f1ec",
    "--sg-key-active-bg": "rgba(111,214,166,0.28)",
    "--sg-key-active-border": "rgba(111,214,166,0.55)",
    "--sg-key-on-bg": "rgba(111,214,166,0.35)",
    "--sg-key-on-border": "rgba(111,214,166,0.7)",
    "--sg-key-on-fg": "#ffffff",
    "--sg-panel-bg": "rgba(20,20,24,0.8)",
    "--sg-panel-border": "rgba(255,255,255,0.25)",
    "--sg-panel-fg": "#ffffff",
    "--sg-panel-solid-bg": "#121816",
  };
  var CHROME_VARS_LIGHT = {
    "--sg-chip-bg": "rgba(255,255,255,0.92)",
    "--sg-chip-border": "rgba(36,41,47,0.14)",
    "--sg-key-bg": "rgba(36,41,47,0.05)",
    "--sg-key-border": "rgba(36,41,47,0.16)",
    "--sg-key-fg": "#24292f",
    "--sg-key-active-bg": "rgba(47,143,99,0.22)",
    "--sg-key-active-border": "rgba(47,143,99,0.5)",
    "--sg-key-on-bg": "rgba(47,143,99,0.3)",
    "--sg-key-on-border": "rgba(47,143,99,0.65)",
    "--sg-key-on-fg": "#24292f",
    "--sg-panel-bg": "rgba(255,255,255,0.88)",
    "--sg-panel-border": "rgba(36,41,47,0.2)",
    "--sg-panel-fg": "#24292f",
    "--sg-panel-solid-bg": "#ffffff",
  };
  function setChromeVars(mode) {
    var vars = mode === "light" ? CHROME_VARS_LIGHT : CHROME_VARS_DARK;
    for (var k in vars) {
      if (Object.prototype.hasOwnProperty.call(vars, k)) {
        document.documentElement.style.setProperty(k, vars[k]);
      }
    }
  }

  // 只能从「是否带了 background 键」推断初始模式：Dark主题对象刻意不带 background
  // （回落到 xterm 默认色，与改动前观感一致），Light主题对象一定带。解析失败一律
  // 按Dark处理，不引入新的默认行为。
  function parseInitialTheme() {
    try {
      var raw = new URLSearchParams(location.search).get("theme");
      if (raw) {
        var obj = JSON.parse(raw);
        if (obj && typeof obj.background === "string") {
          return { mode: "light", bg: obj.background };
        }
      }
    } catch (_e) {
      // 解析失败落到下面的Dark默认值
    }
    return { mode: "dark", bg: "#000" };
  }

  var _initialTheme = parseInitialTheme();
  document.documentElement.style.setProperty("--sg-term-bg", _initialTheme.bg);
  setChromeVars(_initialTheme.mode);

  // term 尚未就绪时先攒住最新一条，waitForTerm 回调里补做一次；term 就绪后
  // applyThemeToTerm 会被替换成真正调用 term.options.theme 的实现。
  var _pendingTermTheme = null;
  var applyThemeToTerm = function (theme) {
    _pendingTermTheme = theme;
  };

  // 显示修复：顶栏「⋯ -> Repair display」经 postMessage 广播进来，重建渲染层字符贴图。
  // term 未就绪时与主题广播同样先攒住，waitForTerm 里补做。
  var _pendingRepairReason = null;
  var _lastRepairAt = 0;
  var repairDisplay = function (reason) {
    _pendingRepairReason = reason;
  };

  var keyboardHeldOpen = false;
  var lastMobileVvWidth = 0;

  window.addEventListener("message", function (e) {
    if (e.origin !== location.origin) return;
    var d = e.data;
    if (!d) return;
    if (d.__shellgate === "repair-display") {
      repairDisplay(d.reason || "menu");
      return;
    }
    if (d.__shellgate === "keyboard") {
      keyboardHeldOpen = !!d.open;
      return;
    }
    if (d.__shellgate !== "theme") return;
    if (d.bg) document.documentElement.style.setProperty("--sg-term-bg", d.bg);
    setChromeVars(d.mode);
    if (d.theme) applyThemeToTerm(d.theme);
  });

  // 诊断版卡顿观测：在 ttyd 建连前包一层 WebSocket，统计 /ws 上下行字节与消息数。
  // 脚本由反代插在 </body> 前同步执行，时机早于 ttyd 的 DOMContentLoaded 建连。
  //
  // downMsgs/downBytes/... 这几个是「本采样窗口内」的计数，installPerfSampler 每 5s 清零；
  // total* 是永不清零的累计值，给Network diagnostics算速率用（两边各算各的，互不干扰）。
  var wsStats = {
    downMsgs: 0, downBytes: 0, upMsgs: 0, upBytes: 0,
    totalDownBytes: 0, totalUpBytes: 0, totalDownMsgs: 0, totalUpMsgs: 0,
    totalRenders: 0, totalLongTaskMs: 0,
    lastCloseCode: 0,
    echo: null, // createRttTracker()，等下面函数定义后再赋值
  };
  // 追踪当前活跃的 ttyd /ws 连接，供断线自动恢复层判断连接是否还活着、
  // 并在Close时回调（installConnectionKeeper 注册 onClose）。
  var liveConn = { ws: null, onClose: null };
  wsStats.echo = createRttTracker(120);
  installWebSocketProbe(wsStats);

  waitForTerm(function (term) {
    // term 就绪后才真正能改 term.options.theme；换成真实实现，并补做一次在此之前
    // 攒下的最新一条（用户在Terminal刚建连的这几百毫秒内点了主题切换属于小概率，但要接住）。
    applyThemeToTerm = function (theme) {
      try {
        term.options.theme = theme;
      } catch (_e) {
        // 极老版本 xterm 不认这个 setter，忽略即可，不影响Terminal本身可用
      }
    };
    if (_pendingTermTheme) {
      applyThemeToTerm(_pendingTermTheme);
      _pendingTermTheme = null;
    }
    // 真正的修复实现：xterm 5.4 起公开的贴图重建入口，重建后强制全屏重绘。
    // 只动画面层，不惊动Terminal里正跑的程序（见 installDisplayRepair 注释）。
    // opts.silentReport：整屏重绘路径可能几秒一次，埋点限流，避免刷满 debug-log。
    repairDisplay = function (reason, opts) {
      var manual = reason === "menu" || reason === "hotkey";
      try {
        if (typeof term.clearTextureAtlas !== "function") {
          if (manual) showHintToast("This renderer cannot repair live. Reload the page.");
          return;
        }
        term.clearTextureAtlas();
        if (typeof term.refresh === "function") {
          term.refresh(0, Math.max(0, (term.rows || 1) - 1));
        }
        _lastRepairAt = Date.now();
        if (!opts || !opts.silentReport) {
          report("display-repair", { reason: reason });
        }
        if (manual) showHintToast("Display cache rebuilt");
      } catch (_e) {
        if (manual) showHintToast("Repair failed. Reload the page to try again.");
      }
    };
    if (_pendingRepairReason) {
      repairDisplay(_pendingRepairReason);
      _pendingRepairReason = null;
    }
    installDisplayRepair(term);
    // ttyd 默认 CSS：#terminal-container .terminal { padding:5px; height:calc(100% - 10px) }
    // 这就是上下左右一圈“边框”的真凶；与是否开手机栏无关，Terminal页一律清掉。
    installTerminalBleedStyles();
    installTightFitOnResize(term);
    installPunctuationFix(term);
    installScreenshotButton(term);
    installImagePasteBridge(term);
    installClipboardBridge(term);
    installManualSelectionCopy(term);
    installCommandAsCtrl(term);
    installSpaceFix(term);
    installSpaceProbe(term);
    installPerfSampler(term, wsStats);
    installNetmonBridge(term, wsStats);
    installConnectionKeeper(term);
    if (shouldEnableMobileUX()) {
      installMobileChrome(term);
    }
  });

  // 诊断埋点：fire-and-forget 上报到登录态保护的调试日志路由，失败静默吞掉，
  // 不影响正常功能。只报事件元信息，不带实际剪贴板内容（隐私考虑）。
  function report(event, detail) {
    try {
      fetch("/__shellgate/debug-log", {
        method: "POST",
        credentials: "same-origin",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ event: event, detail: detail || {} }),
      }).catch(function () {});
    } catch (_err) {
      // ignore
    }
  }

  function payloadByteLength(data) {
    if (data == null) return 0;
    if (typeof data === "string") {
      return typeof TextEncoder !== "undefined" ? new TextEncoder().encode(data).length : data.length;
    }
    if (data instanceof ArrayBuffer) return data.byteLength;
    if (typeof ArrayBuffer !== "undefined" && ArrayBuffer.isView && ArrayBuffer.isView(data)) {
      return data.byteLength;
    }
    if (typeof Blob !== "undefined" && data instanceof Blob) return data.size;
    return 0;
  }

  function nowMs() {
    return typeof performance !== "undefined" && performance.now ? performance.now() : Date.now();
  }

  function round1(v) {
    return Math.round(v * 10) / 10;
  }

  // 往返时延Samples窗口。口径与门禁侧（internal/server/netmon.go 的 rttWindow）保持一致：
  // 同样的 p50/p95 与 RFC 3550 式Jitter（相邻Samples差值的指数滑动平均），这样面板上
  // 「浏览器测的下行段」和「门禁测的上行段」两列数字可以直接横向比较。
  function createRttTracker(size) {
    var buf = [];
    var jitter = 0;
    var prev = null;
    var total = 0;
    var last = 0;
    return {
      add: function (ms) {
        if (!(ms >= 0) || !isFinite(ms)) return;
        buf.push(ms);
        if (buf.length > size) buf.shift();
        if (prev !== null) jitter += (Math.abs(ms - prev) - jitter) / 16;
        prev = ms;
        last = ms;
        total += 1;
      },
      stats: function () {
        if (!buf.length) {
          return { count: total, lastMs: 0, minMs: 0, maxMs: 0, avgMs: 0, p50Ms: 0, p95Ms: 0, jitterMs: 0 };
        }
        var s = buf.slice().sort(function (a, b) { return a - b; });
        var sum = 0;
        for (var i = 0; i < s.length; i++) sum += s[i];
        function pct(p) {
          return s[Math.min(s.length - 1, Math.max(0, Math.ceil(p * s.length) - 1))];
        }
        return {
          count: total,
          lastMs: round1(last),
          minMs: round1(s[0]),
          maxMs: round1(s[s.length - 1]),
          avgMs: round1(sum / s.length),
          p50Ms: round1(pct(0.5)),
          p95Ms: round1(pct(0.95)),
          jitterMs: round1(jitter),
        };
      },
    };
  }

  // ttyd 的客户端Command是「1 字节Command码 + 载荷」：'0' 是键盘输入，'1' 改窗口大小，'2'/'3' 暂停恢复。
  // 关键坑：ttyd 1.7.x 用 TextEncoder 把它编码成 Uint8Array 再发，**不是字符串**——
  // 2026-08-03 真机（headless）实测，只判 typeof data === "string" 会一条输入帧都认不出来，
  // 回显时延Samples恒为 0。这里两种形态都认，取不到就返回 -1。
  function ttydInputFrameSize(data) {
    if (data == null) return -1;
    if (typeof data === "string") return data.charCodeAt(0) === 0x30 ? data.length : -1;
    var bytes = null;
    if (typeof ArrayBuffer !== "undefined") {
      if (data instanceof ArrayBuffer) {
        bytes = new Uint8Array(data);
      } else if (ArrayBuffer.isView && ArrayBuffer.isView(data)) {
        bytes = new Uint8Array(data.buffer, data.byteOffset, data.byteLength);
      }
    }
    if (!bytes || !bytes.length) return -1;
    return bytes[0] === 0x30 ? bytes.length : -1;
  }

  // 把下行帧的载荷取成字符串（去掉 ttyd 的 1 字节Command码），拿不到就返回空串。
  function ttydOutputPayload(data) {
    try {
      if (typeof data === "string") return data.length > 1 ? data.slice(1) : "";
      var bytes = null;
      if (data instanceof ArrayBuffer) bytes = new Uint8Array(data);
      else if (ArrayBuffer.isView && ArrayBuffer.isView(data)) bytes = new Uint8Array(data.buffer, data.byteOffset, data.byteLength);
      if (!bytes || bytes.length < 2) return "";
      var out = "";
      for (var i = 1; i < bytes.length; i++) out += String.fromCharCode(bytes[i]);
      return out;
    } catch (_e) {
      return "";
    }
  }

  // 判断一帧下行数据是不是「Terminal能力查询」而非真正的画面输出。
  //
  // 为什么必须区分（2026-08-05 排查「Corral 标签页回显 P95 500ms+」的结论）：回显计时器
  // 是「发出按键 → 收到下一帧下行数据」算差值，默认下一帧就是这次按键的回显。但全屏 TUI
  // 会定时向Terminal发查询——实测 Corral 空闲时**每 2.00 秒**发一帧 48 字节的
  // `\x1b[?996n` + `\x1b]11;?`（问配色方案与背景色，见 AGENTS.md 里与 Corral 的跨仓库约定）。
  // 而 TUI 里大量按键根本不产生重绘（方向键、Esc、焦点不在输入框、程序正忙时的按键——
  // 本机实测 30 次按键有 23 次完全没有回显），计时器就一路等到那个 2 秒的查询帧，
  // 把它记成一次「回显」，Samples值可达 2000ms。120 个Samples里只要有 6 次这种误记，
  // P95 就被顶到 500ms+，而用户实际打字并不卡。这不是链路慢，是口径错。
  //
  // 判据：整帧去掉Terminal查询序列后不再剩任何内容，就认定它不是画面输出。只匹配「查询」
  // （以 `?` 结尾的 OSC、以 `n` 结尾的 CSI DSR），不碰正常的重绘转义序列。
  function isTerminalQueryOnlyFrame(payload) {
    if (!payload) return false;
    var rest = payload
      // tmux passthrough 包装：内部转义序列的 ESC 是成对写的，所以不能用「不含 ESC」
      // 来界定范围（2026-08-05 实测踩过），只能非贪婪匹配到第一个 ESC \ 终止符。
      .replace(/\x1bP[\s\S]*?\x1b\\/g, "")
      .replace(/\x1b\][0-9]+;\?(\x07|\x1b\\)/g, "") // OSC 颜色查询，如 ]11;?
      .replace(/\x1b\[\?[0-9;]*n/g, "") // CSI DSR 查询，如 [?996n
      .replace(/[\x00-\x1f\x7f]/g, "");
    return rest.length === 0;
  }

  function installWebSocketProbe(stats) {
    var Original = window.WebSocket;
    if (!Original || Original.__shellgatePerfWrapped) return;
    function WrappedWebSocket(url, protocols) {
      var ws = protocols !== undefined ? new Original(url, protocols) : new Original(url);
      var href = String(url || "");
      if (href.indexOf("/ws") === -1) return ws;
      liveConn.ws = ws; // 记录为当前Active connections，供断线恢复层判断存活
      // End to end「按键 → 回显」计时：发出一次输入后记下时刻，等下一帧Terminal输出回来算差值。
      // 全程只用浏览器自己这一个时钟，不受两端时钟不同步影响（WebSocket 没有请求-响应
      // 语义，发送方打时间戳算不出可信的单向时延）。这个值含远端应用的处理时间，
      // 正因如此它才是「卡不卡」的直接指标，而不是纯Network RTT。
      var pendingEchoAt = 0;
      var prevDownPerf = 0;
      ws.addEventListener("message", function (ev) {
        stats.downMsgs += 1;
        stats.totalDownMsgs += 1;
        var n = payloadByteLength(ev.data);
        stats.downBytes += n;
        stats.totalDownBytes += n;
        stats.lastDownTs = Date.now(); // 诊断：最近一次Terminal输出时刻
        stats.lastDownPerf = nowMs();
        // Terminal能力查询帧不是画面输出，不能用来停表（否则 TUI 的定时查询会被误记成回显，
        // 详见 isTerminalQueryOnlyFrame 的说明）。也不刷新「安静」判定的基准时刻。
        if (isTerminalQueryOnlyFrame(ttydOutputPayload(ev.data))) {
          stats.lastDownPerf = prevDownPerf;
          return;
        }
        prevDownPerf = stats.lastDownPerf;
        if (pendingEchoAt) {
          var dt = stats.lastDownPerf - pendingEchoAt;
          pendingEchoAt = 0;
          // 只认得上「这一帧确实是这次按键的回显」的Samples。超过 ECHO_ATTRIBUTION_MS
          // 就无法与「这次按键压根没有回显、等到的是后面某帧无关输出」区分开，
          // 宁可丢弃也不要记一个错的值——错值恰好落在 P95 上，会掩盖真实劣化。
          if (dt >= 0 && dt < ECHO_ATTRIBUTION_MS) stats.echo.add(dt);
        }
      });
      ws.addEventListener("close", function (ev) {
        stats.lastCloseCode = (ev && ev.code) || 0;
        if (liveConn.ws === ws) liveConn.ws = null;
        if (typeof liveConn.onClose === "function") {
          try { liveConn.onClose(ev); } catch (_e) { /* 恢复逻辑异常不影响页面 */ }
        }
      });
      var originalSend = ws.send;
      ws.send = function (data) {
        stats.upMsgs += 1;
        stats.totalUpMsgs += 1;
        var n = payloadByteLength(data);
        stats.upBytes += n;
        stats.totalUpBytes += n;
        // 上一次起表已经超时还没等到回显，说明那次按键压根没触发重绘（TUI 里很常见）。
        // 直接作废，好让这一次按键能重新起表；否则一个没有回显的按键会一直占着计时器。
        if (pendingEchoAt && nowMs() - pendingEchoAt >= ECHO_ATTRIBUTION_MS) pendingEchoAt = 0;
        // 只对键盘输入帧起表，且只认小载荷：大段Paste的回显本来就慢，混进来会把分位数带偏。
        // 另外只在Terminal安静时起表——正在刷屏时收到的下一帧未必是这次按键的回显。
        var frameSize = pendingEchoAt ? -1 : ttydInputFrameSize(data);
        if (frameSize > 0 && frameSize <= 40) {
          // 安静门槛只用来挡「屏幕正在连续刷」的场景（那时下一帧未必是这次按键的回显，
          // 会把时延测得偏低）。门槛不能定高：定成 200ms 时，正常打字速度下几乎每一击都被
          // 跳过，实测 20 次按键只落 1 个Samples。80ms 既挡得住刷屏，也留得住日常打字。
          var quiet = !stats.lastDownPerf || nowMs() - stats.lastDownPerf > 80;
          if (quiet) pendingEchoAt = nowMs();
        }
        return originalSend.call(ws, data);
      };
      return ws;
    }
	WrappedWebSocket.prototype = Original.prototype;
    WrappedWebSocket.CONNECTING = Original.CONNECTING;
    WrappedWebSocket.OPEN = Original.OPEN;
    WrappedWebSocket.CLOSING = Original.CLOSING;
    WrappedWebSocket.CLOSED = Original.CLOSED;
    // 构造函数返回的是原生实例；补 hasInstance，避免 ttyd 的 instanceof WebSocket 误判。
    if (typeof Symbol !== "undefined" && Symbol.hasInstance) {
      try {
        Object.defineProperty(WrappedWebSocket, Symbol.hasInstance, {
          value: function (instance) {
            return instance instanceof Original;
          },
        });
      } catch (_err) {
        // ignore
      }
    }
    WrappedWebSocket.__shellgatePerfWrapped = true;
    window.WebSocket = WrappedWebSocket;
  }

  // 每 5s 上报一条 perf-sample：渲染次数、WS 吞吐、主线程长任务。
  // 隐藏 Tab 或窗口无活动时不上报，避免空转刷盘。
  function installPerfSampler(term, stats) {
    var renderCount = 0;
    var longTaskCount = 0;
    var longTaskMs = 0;

    if (term.onRender) {
      term.onRender(function () {
        renderCount += 1;
        stats.totalRenders += 1;
      });
    }

    if (typeof PerformanceObserver !== "undefined") {
      try {
        var po = new PerformanceObserver(function (list) {
          var entries = list.getEntries();
          for (var i = 0; i < entries.length; i++) {
            longTaskCount += 1;
            longTaskMs += entries[i].duration || 0;
            stats.totalLongTaskMs += entries[i].duration || 0;
          }
        });
        po.observe({ type: "longtask", buffered: true });
      } catch (_err) {
        // Safari 等不支持 longtask 时静默跳过
      }
    }

    setInterval(function () {
      if (typeof document !== "undefined" && document.visibilityState !== "visible") {
        renderCount = 0;
        longTaskCount = 0;
        longTaskMs = 0;
        stats.downMsgs = 0;
        stats.downBytes = 0;
        stats.upMsgs = 0;
        stats.upBytes = 0;
        return;
      }
      var detail = {
        intervalMs: PERF_SAMPLE_INTERVAL_MS,
        renderCount: renderCount,
        wsDownMsgs: stats.downMsgs,
        wsDownBytes: stats.downBytes,
        wsUpMsgs: stats.upMsgs,
        wsUpBytes: stats.upBytes,
        longTaskCount: longTaskCount,
        longTaskMs: Math.round(longTaskMs),
        cols: term.cols || 0,
        rows: term.rows || 0,
        echoP50Ms: stats.echo.stats().p50Ms, // End to end回显时延，判读卡顿时和渲染/吞吐一起看
      };
      renderCount = 0;
      longTaskCount = 0;
      longTaskMs = 0;
      stats.downMsgs = 0;
      stats.downBytes = 0;
      stats.upMsgs = 0;
      stats.upBytes = 0;
      if (detail.renderCount <= 0 && detail.wsDownMsgs <= 0) return;
      report("perf-sample", detail);
    }, PERF_SAMPLE_INTERVAL_MS);
  }

  // ---------- Network diagnostics：把本 Tab 的观测推给工作台父页 ----------
  // Terminal的 WebSocket 在 iframe 这一层，End to end回显时延、Terminal吞吐、断线码只有这里测得到；
  // 而监控面板在父页（顶栏入口）。同源 iframe 用 postMessage 把Samples递上去，父页汇总所有
  // Tab 后再和门禁侧快照拼成完整链路视图。父页不在（直接打开Terminal页）时退化成自发自收，无副作用。
  function installNetmonBridge(term, stats) {
    var m = location.pathname.match(/^\/t\/([^/]+)/);
    var tabId = m ? m[1] : "";
    var prevAt = nowMs();
    var prevUp = 0;
    var prevDown = 0;
    var prevRenders = 0;
    var prevLongTaskMs = 0;

    function reconnectCount() {
      try {
        var raw = sessionStorage.getItem("__shellgate_reloads");
        return raw ? JSON.parse(raw).length : 0;
      } catch (_e) {
        return 0;
      }
    }

    setInterval(function () {
      var t = nowMs();
      var secs = Math.max(0.001, (t - prevAt) / 1000);
      var payload = {
        __shellgate: "netmon",
        tabId: tabId,
        connected: !!(liveConn.ws && liveConn.ws.readyState === 1),
        closeCode: stats.lastCloseCode || 0,
        reconnects: reconnectCount(),
        downBytes: stats.totalDownBytes,
        upBytes: stats.totalUpBytes,
        downBps: Math.round((stats.totalDownBytes - prevDown) / secs),
        upBps: Math.round((stats.totalUpBytes - prevUp) / secs),
        echo: stats.echo.stats(),
        renderPerSec: round1((stats.totalRenders - prevRenders) / secs),
        longTaskMs: Math.round(stats.totalLongTaskMs - prevLongTaskMs),
        idleSeconds: stats.lastDownTs ? round1((Date.now() - stats.lastDownTs) / 1000) : -1,
        cols: term.cols || 0,
        rows: term.rows || 0,
      };
      prevAt = t;
      prevUp = stats.totalUpBytes;
      prevDown = stats.totalDownBytes;
      prevRenders = stats.totalRenders;
      prevLongTaskMs = stats.totalLongTaskMs;
      try {
        window.parent.postMessage(payload, location.origin);
      } catch (_e) {
        // 父页不可达不影响Terminal本身
      }
    }, 2000);
  }

  // ---------- 断线自动恢复 ----------
  // ttyd 1.7.7 的重连行为坑在两点：正常Close(code 1000)只显示 "Press ⏎ to Reconnect"
  // 等用户手点，不自动重连；异常Close也只在断开当下立刻试一次。手机锁屏/切后台回来
  // 时常卡在「要手点」或「那次自动重连趁着还没联网就失败了然后放弃」。
  //
  // 这里由 shell-gate 统一接管恢复层：连接一断，只要页面回到前台且已联网就自动重连。
  // 重连方式是重载 ttyd 页面——反代会重新注入LaunchCommand，Corral 之类Presets程序会自动接回
  // 原会话，用户基本无感。配合 iframe 上的 disableReconnect=true 关掉 ttyd 自带重连，
  // 避免两套重连打架。60s 内自动重连超过上限则停手（防止Command秒退时无限刷屏），
  // 改为提示用户点击/回车手动重连。
  function installConnectionKeeper(term) {
    var GUARD_KEY = "__shellgate_reloads";
    var GUARD_WINDOW_MS = 60000;
    var GUARD_MAX = 5;
    var timer = null;
    var stopped = false;

    function online() { return typeof navigator === "undefined" || navigator.onLine !== false; }
    function visible() { return typeof document === "undefined" || document.visibilityState === "visible"; }
    function dead() {
      var ws = liveConn.ws;
      return !ws || ws.readyState === 2 /* CLOSING */ || ws.readyState === 3 /* CLOSED */;
    }

    function recentReloads() {
      try {
        var raw = sessionStorage.getItem(GUARD_KEY);
        var arr = raw ? JSON.parse(raw) : [];
        var now = Date.now();
        return arr.filter(function (t) { return now - t < GUARD_WINDOW_MS; });
      } catch (_e) {
        return [];
      }
    }

    function doReload() {
      try {
        var arr = recentReloads();
        arr.push(Date.now());
        sessionStorage.setItem(GUARD_KEY, JSON.stringify(arr));
      } catch (_e) { /* 无 sessionStorage 也不阻塞重连 */ }
      report("reconnect-reload", {});
      location.reload();
    }

    function giveUp() {
      stopped = true;
      try {
        term.write("\r\n\x1b[33m[shell-gate] Disconnected. Click or press any key to reconnect\x1b[0m\r\n");
      } catch (_e) { /* Terminal已不可写则忽略 */ }
      function manualRetry() {
        try { sessionStorage.removeItem(GUARD_KEY); } catch (_e) {}
        stopped = false;
        doReload();
      }
      document.addEventListener("pointerdown", manualRetry, { once: true });
      document.addEventListener("keydown", manualRetry, { once: true });
    }

    function attempt() {
      timer = null;
      if (stopped || !dead()) return; // 已停手或已自行恢复
      if (!visible() || !online()) return; // 等前台+联网，事件回调会再触发
      if (recentReloads().length >= GUARD_MAX) { giveUp(); return; }
      doReload();
    }

    function schedule(delay) {
      if (timer || stopped) return;
      timer = setTimeout(attempt, delay);
    }

    liveConn.onClose = function (ev) {
      report("ws-close", { code: ev && ev.code, wasClean: !!(ev && ev.wasClean) });
      schedule(visible() && online() ? 400 : 3000);
    };

    // 回到前台 / Network恢复 / bfcache 恢复：连接已死就尽快重连。
    function nudge() {
      if (dead()) { stopped = false; schedule(200); }
    }
    document.addEventListener("visibilitychange", function () { if (visible()) nudge(); });
    window.addEventListener("online", nudge);
    window.addEventListener("pageshow", nudge);
  }

  // ttyd 的 xterm 实例是异步初始化的，轮询等它出现；非Terminal页面永远等不到，
  // 超时后放弃即可，不影响该页面本身。
  function waitForTerm(cb) {
    var tries = 0;
    var timer = setInterval(function () {
      tries += 1;
      if (window.term && window.term.textarea) {
        clearInterval(timer);
        cb(window.term);
      } else if (tries > 200) {
        clearInterval(timer); // 200 * 100ms = 20s 仍未出现Terminal，判定为非Terminal页面
      }
    }, 100);
  }

  // ---------- 中文全角标点修复 ----------
  // 全角标点（，。？！等）是输入法在「无候选框」的直接上屏模式提交的，此时
  // isComposing=false，浏览器会给隐藏 textarea 派发一次 beforeinput(insertText),
  // 携带真正的全角字符——这里拦下默认插入，改用 term.paste() 把字符正确送到Terminal。
  // 汉字走标准候选框（composition）流程，不受影响。
  //
  // 2026-07-20 曾在这里加过「先退格 N 次再Paste」的补偿，是照抄其它项目
  // （Orca/Tabby 等）遇到的另一种 bug（以为 xterm 的 keydown 已经抢先发了错误
  // 半角字符）写的，当时没有这台Machine的真实证据。用户实测反馈：标点字符本身
  // 已经正确送达，退格反而会误删上一次按键已经打好的字符——证明这台Machine的
  // 场景里根本没有需要退格纠正的半角字符，那段补偿纯属误伤，已Delete。
  function installPunctuationFix(term) {
    var textarea = term.textarea;
    textarea.addEventListener(
      "beforeinput",
      function (e) {
        if (e.isComposing) return;
        if (e.inputType !== "insertText" || !e.data) return;
        if (/^[\x00-\x7f]*$/.test(e.data)) return; // 纯 ASCII，不是本 bug 场景
        e.preventDefault();
        term.paste(e.data);
        report("punct-fix", { data: e.data, inputType: e.inputType });
      },
      true,
    );
  }

  // 诊断：空格在手机端无法正常输入。纯记录空格从「按键 → beforeinput → input →
  // 送达 PTY」四个环节各自到没到，用于定位空格到底在哪一步被吞掉。只记录不改行为。
  function installSpaceProbe(term) {
    var ta = term && term.textarea;
    if (!ta) return;
    function isSpace(v) { return v === " " || (typeof v === "string" && v.indexOf(" ") >= 0); }
    ta.addEventListener("keydown", function (e) {
      if (e.code === "Space" || e.key === " " || e.keyCode === 32) {
        report("space-keydown", { key: e.key, code: e.code, keyCode: e.keyCode, comp: e.isComposing });
      }
    }, true);
    ta.addEventListener("beforeinput", function (e) {
      if (isSpace(e.data) || e.inputType === "insertText") {
        report("space-beforeinput", { it: e.inputType, data: JSON.stringify(e.data), comp: e.isComposing });
      }
    }, true);
    ta.addEventListener("input", function (e) {
      if (isSpace(e.data) || e.inputType === "insertText") {
        report("space-input", { it: e.inputType, data: JSON.stringify(e.data) });
      }
    }, true);
    try {
      if (typeof term.onData === "function") {
        term.onData(function (d) {
          if (isSpace(d)) report("space-ondata", { d: JSON.stringify(d) });
        });
      }
    } catch (_err) {
      // ignore
    }
  }

  // ---------- 手机端空格修复 ----------
  // 现象（真机埋点实证）：手机键盘处于「滑行输入 / 输入法联想（合成态）」时，
  // 空格以 keydown keyCode=229 上报，xterm 判定为「合成中」直接跳过，不会把空格
  // 送进 PTY（onData 不触发），空格被吞；字母走 input 读取链路照常送达，所以只有
  // 空格失效。物理空格键走 keyCode=32，xterm 自己能正确送达，不受此影响。
  //
  // 修复：拦下「非合成态、单个空格」的 beforeinput(insertText)，直接经 onData 送
  // 一个空格并阻止默认插入。为避免物理空格（keyCode=32，xterm 已送一份）被这里
  // 再送成双份，若刚发生过真实的 keyCode=32 空格 keydown（80ms 内）则放行不插手。
  function installSpaceFix(term) {
    var ta = term && term.textarea;
    if (!ta) return;
    var lastRealSpaceKeydown = 0;
    ta.addEventListener("keydown", function (e) {
      if (e.keyCode === 32 && e.key === " ") lastRealSpaceKeydown = Date.now();
    }, true);
    ta.addEventListener("beforeinput", function (e) {
      if (e.isComposing) return;
      if (e.inputType !== "insertText" || e.data !== " ") return;
      if (Date.now() - lastRealSpaceKeydown < 80) return; // 物理空格，xterm 已处理
      e.preventDefault();
      sendToPty(term, " ");
      report("space-fix-inject", {});
    }, true);
  }

  // ---------- 截图按钮：把当前Terminal画面压缩后放进浏览器剪贴板 ----------
  var _screenshotBtn = null;

  function installScreenshotButton(term) {
    var btn = document.createElement("button");
    _screenshotBtn = btn;
    btn.type = "button";
    btn.textContent = "📷"; // 📷
    btn.title = "Copy terminal screenshot";
    btn.style.cssText = [
      "position:fixed",
      "right:12px",
      "bottom:12px",
      "z-index:2147483646",
      "width:36px",
      "height:36px",
      "border-radius:999px",
      "border:1px solid var(--sg-panel-border,rgba(255,255,255,0.25))",
      "background:var(--sg-panel-bg,rgba(20,20,24,0.72))",
      "color:var(--sg-panel-fg,#fff)",
      "font-size:16px",
      "line-height:34px",
      "padding:0",
      "cursor:pointer",
      "box-shadow:0 2px 8px rgba(0,0,0,0.35)",
    ].join(";");
    btn.addEventListener("mousedown", function (e) {
      // 与快捷键栏相同：避免点截图时 iOS 收起键盘
      e.preventDefault();
    });
    btn.addEventListener("touchstart", function (e) {
      e.preventDefault();
    }, { passive: false });
    btn.addEventListener("click", function () {
      var capturedCanvas = null;
      captureTerminalCanvas(term)
        .then(function (canvas) {
          capturedCanvas = canvas;
          report("screenshot-capture", { layers: canvas._layerCount, w: canvas.width, h: canvas.height });
          return compressCanvasToBlob(canvas, MAX_IMAGE_BYTES);
        })
        .then(function (jpegBlob) {
          // 部分浏览器（尤其 Safari）对 navigator.clipboard.write() 只认
          // image/png，不认 image/jpeg——同一个 ClipboardItem 里带两种格式，
          // 目标 App 会自己挑它支持的那个，不用猜用户在用哪个浏览器。
          return canvasToBlob(capturedCanvas, "image/png").then(function (pngBlob) {
            var parts = { "image/jpeg": jpegBlob };
            if (pngBlob) parts["image/png"] = pngBlob;
            return navigator.clipboard.write([new ClipboardItem(parts)]).then(function () {
              return { jpegBytes: jpegBlob.size, pngBytes: pngBlob && pngBlob.size };
            });
          });
        })
        .then(function (sizes) {
          report("screenshot-write", { ok: true, jpegBytes: sizes.jpegBytes, pngBytes: sizes.pngBytes });
          flashButton(btn, "✅"); // ✅
        })
        .catch(function (err) {
          console.error("[shell-gate] Screenshot failed", err);
          report("screenshot-write", { ok: false, errorName: err && err.name, errorMessage: err && err.message });
          flashButton(btn, "⚠️"); // ⚠️
          // 按钮上那一下 ⚠️ 只闪 1.2 秒且不说明原因，用户无从判断该怎么办；
          // 把原因和补救动作显式说出来（控制台日志用户是看不到的）。
          showHintToast((err && err.message) || "Screenshot failed");
        });
    });
    document.body.appendChild(btn);
  }

  function placeScreenshotAboveToolbar(toolbarH) {
    if (!_screenshotBtn) return;
    _screenshotBtn.style.top = "";
    _screenshotBtn.style.bottom = 12 + (toolbarH || 0) + "px";
  }

  // 一次性的纯文字提示条（不可点，几秒后自动消失）。与剪贴板那条提示条的区别是：
  // 那条要承载可点击的Copy动作，这条只负责把失败原因和补救动作告诉用户。
  var _hintToastEl = null;
  function showHintToast(message) {
    if (_hintToastEl) {
      _hintToastEl.remove();
      _hintToastEl = null;
    }
    var el = document.createElement("div");
    el.textContent = message;
    el.style.cssText = [
      "position:fixed",
      "right:12px",
      "bottom:56px",
      "z-index:2147483647",
      "max-width:280px",
      "padding:8px 12px",
      "border-radius:8px",
      "border:1px solid var(--sg-panel-border,rgba(255,255,255,0.25))",
      "background:var(--sg-panel-bg,rgba(20,20,24,0.88))",
      "color:var(--sg-panel-fg,#fff)",
      "font-size:12px",
      "line-height:1.4",
      "box-shadow:0 2px 8px rgba(0,0,0,0.35)",
      "pointer-events:none",
    ].join(";");
    document.body.appendChild(el);
    _hintToastEl = el;
    setTimeout(function () {
      if (_hintToastEl === el) {
        el.remove();
        _hintToastEl = null;
      }
    }, 5000);
  }

  function flashButton(btn, text) {
    var original = btn.textContent;
    btn.textContent = text;
    setTimeout(function () {
      btn.textContent = original;
    }, 1200);
  }

  // 依次把 xterm 各渲染层（WebGL 下通常是文字层 + 光标层；canvas 渲染器下
  // 还有选区/链接）按 DOM 顺序合成到一张离屏 canvas，得到当前Terminal完整画面。
  // WebGL 能读到非黑像素，靠的是文件顶部的 preserveDrawingBuffer 补丁。
  function captureTerminalCanvas(term) {
    return new Promise(function (resolve, reject) {
      var screen = term.element && term.element.querySelector(".xterm-screen");
      var layers = screen ? screen.querySelectorAll("canvas") : [];
      if (!layers.length) {
        // 几乎只有一种到法：WebGL 上下文丢失后 ttyd 把 WebglAddon dispose 掉、
        // 回落到 DOM 渲染器（见 AGENTS.md 渲染器那条）。此时Terminal本身照常能用，
        // 用户不会想到要刷新，只会看到截图按钮一直失败——所以必须给出可执行的提示。
        reject(new Error("The renderer has fallen back. Reload the page to restore screenshots."));
        return;
      }
      var w = layers[0].width;
      var h = layers[0].height;
      var out = document.createElement("canvas");
      out.width = w;
      out.height = h;
      var ctx = out.getContext("2d");
      for (var i = 0; i < layers.length; i += 1) {
        ctx.drawImage(layers[i], 0, 0);
      }
      out._layerCount = layers.length; // 诊断用：确认真的抓到了画面图层，不是空画布
      resolve(out);
    });
  }

  // canvas.toBlob 的 Promise 封装；编码失败（比如某些浏览器对巨图编 PNG 报错）
  // resolve(null) 而不是 reject，方便调用方把它当"可选的兼容格式"处理。
  function canvasToBlob(canvas, mime) {
    return new Promise(function (resolve) {
      try {
        canvas.toBlob(function (blob) {
          resolve(blob || null);
        }, mime);
      } catch (_err) {
        resolve(null);
      }
    });
  }

  // 用 JPEG 质量做二分查找，把图片压到体积上限以内；质量已到下限仍超限时缩小尺寸重试。
  function compressCanvasToBlob(canvas, maxBytes) {
    return new Promise(function (resolve, reject) {
      var lo = 0.1;
      var hi = 0.92;
      var attempts = 0;
      var best = null;

      function tryQuality(q) {
        canvas.toBlob(
          function (blob) {
            attempts += 1;
            if (!blob) {
              reject(new Error("Could not encode image"));
              return;
            }
            if (blob.size <= maxBytes) {
              best = blob;
              if (hi - lo < 0.05 || attempts >= 8) {
                resolve(best);
                return;
              }
              lo = q;
              tryQuality((lo + hi) / 2);
            } else {
              hi = q;
              if (attempts >= 8) {
                if (best) {
                  resolve(best);
                } else {
                  downscaleAndRetry();
                }
                return;
              }
              tryQuality((lo + hi) / 2);
            }
          },
          "image/jpeg",
          q,
        );
      }

      function downscaleAndRetry() {
        var scaled = document.createElement("canvas");
        scaled.width = Math.max(1, Math.round(canvas.width * 0.7));
        scaled.height = Math.max(1, Math.round(canvas.height * 0.7));
        scaled.getContext("2d").drawImage(canvas, 0, 0, scaled.width, scaled.height);
        if (scaled.width < 200 || scaled.height < 100) {
          reject(new Error("The image still exceeds the size limit."));
          return;
        }
        canvas = scaled;
        lo = 0.1;
        hi = 0.92;
        attempts = 0;
        tryQuality((lo + hi) / 2);
      }

      tryQuality(hi);
    });
  }

  // ---------- Paste图片：压缩后经括号Paste通道送进内嵌的 agent ----------
  function installImagePasteBridge(term) {
    document.addEventListener(
      "paste",
      function (e) {
        var items = e.clipboardData && e.clipboardData.items;
        var text = "";
        try {
          text = (e.clipboardData && e.clipboardData.getData("text/plain")) || "";
        } catch (_err) {
          // ignore
        }
        var types = [];
        if (items) {
          for (var j = 0; j < items.length; j += 1) types.push(items[j].type);
        }
        report("paste-event", { hasClipboardData: !!e.clipboardData, types: types, target: e.target && e.target.tagName });
        var hasImageItem = false;
        var file = null;
        var pickedType = null;
        if (items) {
          for (var i = 0; i < items.length; i += 1) {
            if (!items[i].type || items[i].type.indexOf("image/") !== 0) continue;
            hasImageItem = true;
            // 同一份截图可能同时以多种格式（如 JPEG+PNG）出现在系统剪贴板里
            // （见 installScreenshotButton 的兼容性说明）；某些浏览器对其中某个
            // 格式的 getAsFile() 会静默返回 null（2026-07-20 真机日志实测：
            // image/jpeg 稳定返回 null，image/png 正常），只取第一个格式会在
            // 命中「坏」格式时直接判定Paste失败——依次尝试直到拿到真正的 File。
            var candidate = items[i].getAsFile();
            if (candidate) {
              file = candidate;
              pickedType = items[i].type;
              break;
            }
          }
        }
        if (!hasImageItem) {
          if (!text) return;
          // macOS 浏览器把 Command+V 表达为标准 paste 事件；直接走 xterm 的
          // 整段Paste入口，不再依赖隐藏 textarea 的浏览器默认行为。
          e.preventDefault();
          e.stopPropagation();
          term.paste(text);
          report("paste-text-sent", { len: text.length });
          return;
        }
        e.preventDefault();
        e.stopPropagation();
        report("paste-image-detected", { types: types, pickedType: pickedType, fileBytes: file && file.size });
        if (!file) return;
        loadImageFile(file)
          .then(function (img) {
            report("paste-image-loaded", { w: img.naturalWidth, h: img.naturalHeight });
            return drawToCanvas(img);
          })
          .then(function (canvas) {
            return compressCanvasToBlob(canvas, MAX_IMAGE_BYTES);
          })
          .then(function (blob) {
            report("paste-image-compressed", { bytes: blob.size });
            return blobToBase64(blob);
          })
          .then(function (base64) {
            term.paste(IMG_SENTINEL_BEGIN + base64 + IMG_SENTINEL_END);
            report("paste-image-sent", { base64Len: base64.length });
          })
          .catch(function (err) {
            console.error("[shell-gate] Image paste failed", err);
            report("paste-image-failed", { errorName: err && err.name, errorMessage: err && err.message });
          });
      },
      true,
    );
  }

  function loadImageFile(file) {
    return new Promise(function (resolve, reject) {
      var img = new Image();
      var url = URL.createObjectURL(file);
      img.onload = function () {
        URL.revokeObjectURL(url);
        resolve(img);
      };
      img.onerror = function (err) {
        URL.revokeObjectURL(url);
        reject(err);
      };
      img.src = url;
    });
  }

  function drawToCanvas(img) {
    var canvas = document.createElement("canvas");
    canvas.width = img.naturalWidth || img.width;
    canvas.height = img.naturalHeight || img.height;
    canvas.getContext("2d").drawImage(img, 0, 0);
    return canvas;
  }

  function blobToBase64(blob) {
    return new Promise(function (resolve, reject) {
      var reader = new FileReader();
      reader.onload = function () {
        var result = String(reader.result || "");
        var comma = result.indexOf(",");
        resolve(comma >= 0 ? result.slice(comma + 1) : result);
      };
      reader.onerror = reject;
      reader.readAsDataURL(blob);
    });
  }

  // ---------- agent Copy内容同步到浏览器剪贴板 ----------
  // 根因（2026-07-20 排查确认）：agent/Corral Copy文本时发的 OSC 52 序列本身
  // 一路干净地到达了浏览器；但 ttyd 内置的处理是在 WebSocket message 回调里
  // 直接调 navigator.clipboard.writeText()——这个回调不带"用户手势"，现代浏览器
  // （尤其 Safari/Firefox，Chrome 也在收紧）会静默拒绝没有手势的剪贴板写入，
  // 这是网页平台的安全限制，不是这条链路哪一层的实现 bug。
  //
  // 用 xterm.js 公开的 registerOscHandler 接管 OSC 52（后注册的 handler 先跑，
  // 返回 true 后 ttyd 内置处理就不会再重复处理）：先尝试静默写入（部分场景下
  // 手势要求较松，能成功就零感知）；不管是否成功都缓存内容并给一个可点击的
  // 提示条，点击发生在真实点击事件里，这是唯一能在所有浏览器上稳定生效的方式。
  function installClipboardBridge(term) {
    if (!term.parser || typeof term.parser.registerOscHandler !== "function") return;
    term.parser.registerOscHandler(52, function (data) {
      var text = decodeOsc52(data);
      if (text === null) return false; // 解析失败交还给 ttyd 内置处理兜底
      navigator.clipboard.writeText(text).then(
        function () {
          report("clipboard-write", { mode: "silent", ok: true });
          showClipboardToast(text, true);
        },
        function (err) {
          report("clipboard-write", { mode: "silent", ok: false, errorName: err && err.name });
          showClipboardToast(text, false);
        },
      );
      report("osc52-received", { bytes: text.length });
      return true;
    });
  }

  // OSC 52 payload 格式为 "Pc;Pd"（Pc 选区目标，Pd 是 base64），Pd 解码后按
  // UTF-8 还原；解析失败（如 Pd 不是合法 base64）返回 null。
  function decodeOsc52(data) {
    var sep = data.indexOf(";");
    if (sep < 0) return null;
    var b64 = data.slice(sep + 1);
    if (!b64 || b64 === "?") return null; // "?" 是查询剪贴板内容的请求，不是写入
    try {
      var binary = atob(b64);
      var bytes = new Uint8Array(binary.length);
      for (var i = 0; i < binary.length; i += 1) bytes[i] = binary.charCodeAt(i);
      return new TextDecoder("utf-8").decode(bytes);
    } catch (_err) {
      return null;
    }
  }

  var _clipboardToastEl = null;

  function showClipboardToast(text, silentOk) {
    if (_clipboardToastEl) {
      _clipboardToastEl.remove();
      _clipboardToastEl = null;
    }
    var el = document.createElement("div");
    var preview = text.length > 40 ? text.slice(0, 40) + "…" : text;
    el.style.cssText = [
      "position:fixed",
      "right:12px",
      "bottom:56px",
      "z-index:2147483647",
      "max-width:280px",
      "padding:8px 12px",
      "border-radius:8px",
      "border:1px solid var(--sg-panel-border,rgba(255,255,255,0.25))",
      "background:var(--sg-panel-bg,rgba(20,20,24,0.88))",
      "color:var(--sg-panel-fg,#fff)",
      "font-size:12px",
      "line-height:1.4",
      "cursor:pointer",
      "box-shadow:0 2px 8px rgba(0,0,0,0.35)",
      "word-break:break-all",
    ].join(";");
    function render(label) {
      el.textContent = label + "：" + preview;
    }
    render(silentOk ? "✅ Copied to clipboard" : "📋 Remote copy ready; click to copy");
    el.addEventListener("click", function () {
      navigator.clipboard.writeText(text).then(
        function () {
          report("clipboard-write", { mode: "click", ok: true });
          render("✅ Copied to clipboard");
          setTimeout(dismiss, 1200);
        },
        function (err) {
          report("clipboard-write", { mode: "click", ok: false, errorName: err && err.name });
        },
      );
    });
    function dismiss() {
      if (_clipboardToastEl === el) {
        el.remove();
        _clipboardToastEl = null;
      }
    }
    document.body.appendChild(el);
    _clipboardToastEl = el;
    if (silentOk) setTimeout(dismiss, 2000);
  }

  // ---------- 手动选中画面文字（含内嵌 agent 的聊天内容）后Copy ----------
  // 2026-07-20 排查记录（诊断日志实测证据，不是推测）：
  //   - term.getSelection()（xterm 自己的选区模型）在这套用法下**始终是空的**——
  //     因为 Corral 是全屏 TUI，自己接管了鼠标、自己在内部维护选区Status，画面上
  //     看到的"拖选高亮"其实是 Corral 画出来的，不是浏览器/xterm 的原生选区。
  //   - 真正生效的路径是：物理 Ctrl+C 这个按键被 xterm 正常编码发到了远端，
  //     Corral 收到后自己判断"有没有选区"，有则触发 OSC 52 Copy（走上面的
  //     installClipboardBridge），这条路径实测是通的。
  //   - 但 Mac 用户习惯按 Command+C：Command+C 是 Mac 上"Copy"的保留组合键，
  //     浏览器/xterm 不会把它编码成Terminal里 Ctrl+C 那个控制字符，所以这个按键
  //     压根到不了 Corral，自然也就触发不了它内部的Copy逻辑。
  // 修复：物理 Ctrl+C 仍按普通Terminal按键处理；Command+C、浏览器菜单Copy统一走
  // 标准 copy 事件。后者是 Safari/Chrome 都承诺派发的剪贴板入口，不依赖浏览器
  // 是否把 Command 键先转成 keydown 事件。
  function installManualSelectionCopy(term) {
    document.addEventListener(
      "keydown",
      function (e) {
        var isCtrlC = e.ctrlKey && !e.metaKey && !e.shiftKey && !e.altKey &&
          (e.key === "c" || e.key === "C");
        if (!isCtrlC) return;
        var text = "";
        try {
          text = term.getSelection() || "";
        } catch (_err) {
          // ignore
        }
        report("ctrlc-keydown", { selLen: text.length, meta: false });
        if (text) {
          e.preventDefault();
          e.stopPropagation();
          navigator.clipboard.writeText(text).then(
            function () {
              report("manual-copy", { ok: true, len: text.length });
              showClipboardToast(text, true);
            },
            function (err) {
              report("manual-copy", { ok: false, len: text.length, errorName: err && err.name });
              showClipboardToast(text, false);
            },
          );
        }
      },
      true,
    );
    document.addEventListener(
      "copy",
      function (e) {
        var text = "";
        try {
          text = term.getSelection() || "";
        } catch (_err) {
          // ignore
        }
        report("copy-event", { selLen: text.length });
        e.preventDefault();
        e.stopPropagation();
        if (text) {
          // copy 事件本身带着浏览器认可的用户手势，直接同步写事件剪贴板；
          // 这比异步 Clipboard API 对 Safari 更可靠。
          if (e.clipboardData) e.clipboardData.setData("text/plain", text);
          report("manual-copy", { ok: true, len: text.length, mode: "copy-event" });
          showClipboardToast(text, true);
          return;
        }
        // Corral 的 Textual 选区不进入 xterm 的内容模型；把浏览器的Copy动作
        // 转成真正的 Ctrl+C，让 Corral 判断“Copy选区”还是“中断当前Command”。
        sendToPty(term, "\x03");
        report("copy-forwarded-as-ctrlc", {});
      },
      true,
    );
  }

  // Command 当 Ctrl 用（只在苹果键盘上装）。
  // Terminal协议里只有 Ctrl / Alt(Meta) / Shift 三种修饰键，Command 没有任何字节表示——
  // 按下去服务端永远收不到。Mac 用户的肌肉记忆却是 Command，所以在浏览器侧把
  // Command+字母 翻译成对应控制字符（Cmd+D → 0x04，与物理 Ctrl+D 完全等价）再送进 PTY。
  //
  // 三条边界，改之前先看清楚：
  //  1. **C / V 必须放行**：Command+C / Command+V 是本页CopyPaste的唯一入口
  //     （installManualSelectionCopy、installImagePasteBridge 接的是浏览器 copy/paste 事件）。
  //     把它们抢过来当控制字符，剪贴板功能整个就没了；而且 Command+C 在没有选区时
  //     本来就会被转成真正的 Ctrl+C 送下去，「中断当前Command」的效果早就有了。
  //  2. **Cmd+W / T / N / Q 拦不住**：Chrome、Safari、Edge 把这几个标签页 / 窗口级快捷键
  //     留给浏览器自己，页面收不到事件、preventDefault 也无效。所以 Ctrl+W（删词）在 Mac 上
  //     只能按物理 Ctrl+W，指望 Command 版本会直接把页面关掉。Safari 另外还独占了
  //     Cmd+R / Cmd+L，这两个在 Safari 里同样落不到这段代码上（Chrome 里正常）。
  //  3. 带 Ctrl、Alt 或 Shift 的组合一律放行：前两者交给 xterm 原样编码，Shift 组合放行是
  //     为了不抢 Cmd+Shift+V 这类浏览器Paste变体。
  //
  // 拦截写在 document 的捕获阶段：捕获期 stopPropagation 后事件到不了 xterm 的隐藏输入框，
  // 不必再动 attachCustomKeyEventHandler，也不会和上面几个剪贴板监听打架。
  // ---------- 显示自愈（WebGL 字符贴图损坏；默认 canvas 下是双保险） ----------
  // xterm.js 的 WebGL 渲染层把字符先画进显卡纹理贴图再复用；CJK 宽字符把图集
  // 挤爆后画面用过期贴图（中文消失/两半字叠在一起），缓冲区内容其实是对的。
  // 2026-08-31 默认改 canvas 从根上避开这条；下面自愈留下，是有人强制 webgl
  // 或上下文丢失后再拉起时的双保险。不要再靠加触发点当产品修法。
  //
  // 上游 #6038 的修复落在 @xterm/xterm 6.1.0-beta.289（2026-07），不是 7.0.0；
  // ttyd 1.7.7 内置的仍是 5.4.0，ttyd master 也还是 5.5.0。门禁侧用官方
  // clearTextureAtlas() + refresh 自愈：只动画面层，不给Terminal里正跑的程序发任何字节。
  //
  // 自动触发必须盖住「使用中」这条主路径。v0.5.0 只在回前台 / DPR 变化时修，
  // Corral 这类 TUI 正在刷屏时乱码会一直挂着，用户只能继续靠调窗口——等于没修。
  // 现在：连续多次大半屏重绘（爆发）安静后再修；爆发持续太久则强制修一次。
  // 一次整屏后若再无爆发（Corral 每 2s 空闲查询），8s 懒修且不重计时，避免被心跳冲掉。
  // 手动：快捷键与顶栏「⋯ -> Repair display」。
  function installDisplayRepair(term) {
    document.addEventListener("visibilitychange", function () {
      if (!document.hidden) repairDisplay("visible");
    });

    // devicePixelRatio 变化后旧贴图全部失配；分辨率媒体查询断点要跟着当前值重新注册。
    function watchDpr() {
      var mq = window.matchMedia("(resolution: " + (window.devicePixelRatio || 1) + "dppx)");
      var onChange = function () {
        try {
          mq.removeEventListener("change", onChange);
        } catch (_e) {
          /* 老浏览器没有 removeEventListener，重建后旧监听自然失效 */
        }
        repairDisplay("dpr");
        watchDpr();
      };
      if (mq.addEventListener) mq.addEventListener("change", onChange);
    }
    watchDpr();

    // 手动修复快捷键：Ctrl+Shift+K / Mac Cmd+Shift+K。捕获阶段拦在 xterm 之前；
    // 带 Alt 或 Ctrl/Cmd 同按的杂拌组合不放行，避免误伤Terminal组合键。
    document.addEventListener("keydown", function (e) {
      if (typeof e.key !== "string" || e.key.toLowerCase() !== "k") return;
      if (!e.shiftKey || e.altKey) return;
      if (!((e.ctrlKey && !e.metaKey) || (e.metaKey && !e.ctrlKey))) return;
      e.preventDefault();
      e.stopPropagation();
      repairDisplay("hotkey");
    }, true);

    installFullRedrawRepair(term);
  }

  // 大半屏一次算整屏。xterm 5.4 的 onRender 回调带 {start,end} 行号。
  function isFullViewportRedraw(term, ev) {
    var rows = term.rows || 0;
    if (rows <= 0) return false;
    var start = ev && typeof ev.start === "number" ? ev.start : 0;
    var end = ev && typeof ev.end === "number" ? ev.end : -1;
    var span = end >= start ? end - start + 1 : rows;
    return span * 2 >= rows;
  }

  function installFullRedrawRepair(term) {
    if (!term.onRender) return;
    var REPAIR_QUIET_MS = 180;
    var REPAIR_LAZY_MS = 8000;
    var REPAIR_MIN_INTERVAL_MS = 800;
    var REPAIR_MAX_WAIT_MS = 2500;
    var REPAIR_AUTO_REPORT_MS = 15000;
    var BURST_WINDOW_MS = 1000;
    var BURST_MIN_REDRAWS = 2;
    var pending = null;
    var burstStart = 0;
    var lastAutoReportAt = 0;
    var redrawTimes = [];

    function runScheduledRepair() {
      pending = null;
      burstStart = 0;
      if (typeof document !== "undefined" && document.hidden) return;
      var now = Date.now();
      if (now - _lastRepairAt < REPAIR_MIN_INTERVAL_MS) {
        pending = setTimeout(runScheduledRepair, REPAIR_MIN_INTERVAL_MS - (now - _lastRepairAt));
        return;
      }
      var silent = now - lastAutoReportAt < REPAIR_AUTO_REPORT_MS;
      if (!silent) lastAutoReportAt = now;
      repairDisplay("full-redraw", { silentReport: silent });
    }

    function scheduleFullRedrawRepair(force) {
      if (typeof document !== "undefined" && document.hidden) return;
      var now = Date.now();
      if (!burstStart) burstStart = now;
      if (!force && now - burstStart >= REPAIR_MAX_WAIT_MS) force = true;
      if (pending) clearTimeout(pending);
      pending = setTimeout(runScheduledRepair, force ? 0 : REPAIR_QUIET_MS);
    }

    term.onRender(function (ev) {
      if (!isFullViewportRedraw(term, ev)) return;
      var now = Date.now();
      redrawTimes.push(now);
      while (redrawTimes.length && now - redrawTimes[0] > BURST_WINDOW_MS) {
        redrawTimes.shift();
      }
      if (redrawTimes.length >= BURST_MIN_REDRAWS) {
        scheduleFullRedrawRepair(false);
        return;
      }
      // 一次整屏就乱码、之后只剩空闲心跳时：已有定时器不重计，8s 内仍会修到。
      if (!pending) {
        burstStart = now;
        pending = setTimeout(runScheduledRepair, REPAIR_LAZY_MS);
      }
    });
  }

  function installCommandAsCtrl(term) {
    if (!isAppleClient()) return;
    // Ctrl 的非字母控制字符：Cmd+[ → ESC（vim 退出插入态）、Cmd+] / Cmd+\ 等同理。
    var EXTRA = { "[": "\x1b", "]": "\x1d", "\\": "\x1c", "-": "\x1f", "/": "\x1f" };
    var reported = 0;

    document.addEventListener(
      "keydown",
      function (e) {
        if (!e.metaKey || e.ctrlKey || e.altKey || e.shiftKey) return;
        var key = e.key;
        if (typeof key !== "string") return;
        var byte = "";
        if (key.length === 1 && key >= "a" && key <= "z") {
          if (key === "c" || key === "v") return; // 让位给剪贴板
          byte = String.fromCharCode(key.charCodeAt(0) - 96);
        } else if (Object.prototype.hasOwnProperty.call(EXTRA, key)) {
          byte = EXTRA[key];
        } else {
          return;
        }
        e.preventDefault();
        e.stopPropagation();
        sendToPty(term, byte);
        // 埋点只留开头几次，避免正常敲键把调试日志刷满。
        if (reported < 10) {
          reported++;
          report("cmd-as-ctrl", { key: key });
        }
      },
      true,
    );
  }

  // Mac / iPad 外接键盘才有 Command 键；其它平台不装映射。
  function isAppleClient() {
    try {
      var uaPlatform = (navigator.userAgentData && navigator.userAgentData.platform) || "";
      var legacy = navigator.platform || "";
      var ua = navigator.userAgent || "";
      return /mac/i.test(uaPlatform) || /Mac|iPhone|iPad|iPod/.test(legacy) || /Macintosh|iPad|iPhone/.test(ua);
    } catch (_err) {
      return false;
    }
  }

  // 清掉 ttyd 自带内边距，并把Terminal盒铺满父级。
  // ttyd 默认：#terminal-container .terminal { padding:5px; height:calc(100% - 10px) }
  // FitAddon 还会按 scrollBarWidth 少算列、按 floor 少算行 → 右/下各留一条空带。
  function installTerminalBleedStyles() {
    if (document.getElementById("shellgate-bleed-style")) return;
    var style = document.createElement("style");
    style.id = "shellgate-bleed-style";
    style.textContent = [
      "html,body{",
      "margin:0!important;padding:0!important;border:0!important;",
      "overflow:hidden!important;background:var(--sg-term-bg,#000)!important;",
      "}",
      "#terminal-container{",
      "position:absolute!important;inset:0!important;",
      "margin:0!important;padding:0!important;border:0!important;",
      "width:auto!important;height:auto!important;max-width:none!important;",
      "background:var(--sg-term-bg,#000)!important;",
      "}",
      "#terminal-container .terminal,",
      "#terminal-container .xterm,",
      ".terminal.xterm,.xterm{",
      "position:absolute!important;inset:0!important;",
      "padding:0!important;margin:0!important;border:0!important;",
      "width:auto!important;height:auto!important;max-width:none!important;",
      "box-sizing:border-box!important;",
      "}",
      ".xterm-viewport{",
      "overflow-y:auto!important;scrollbar-width:none!important;-ms-overflow-style:none!important;",
      "background:var(--sg-term-bg,#000)!important;",
      "}",
      ".xterm-viewport::-webkit-scrollbar{width:0!important;height:0!important;display:none!important;}",
      ".xterm-screen{margin:0!important;padding:0!important;border:0!important;}",
    ].join("");
    document.head.appendChild(style);
  }

  // 冪等化用的Status指纹：既含Terminal「可用像素盒」，也含当前实际渲染出来的字符网格
  // （列行数 + 未经 transform 的屏幕层布局尺寸）。只看像素盒会漏掉「容器没变、但
  // 字号/单元格变了」这类变化——ttyd 是在 WebSocket 握手拿到服务端下发的选项后才
  // 套用字体与字号的，那一刻容器尺寸完全没动，网格却整体重算了。
  function tightFitStateKey(term, guardBox) {
    var gr = guardBox.getBoundingClientRect();
    var screen = term.element && term.element.querySelector(".xterm-screen");
    return [
      Math.round(gr.width),
      Math.round(gr.height),
      term.cols || 0,
      term.rows || 0,
      screen ? screen.offsetWidth : 0,
      screen ? screen.offsetHeight : 0,
    ].join("x");
  }

  // 让字符网格贴满容器：先骗 FitAddon 滚动条宽=0，再按空隙补列/行，最后对半格余量做 scale。
  function tightFitTerminal(term, container) {
    if (!term || !container) return;

    // 冪等化（关键修复）：每次视口事件都无条件 term.fit() 会反复重排 xterm，令隐藏
    // textarea 一瞬失焦，手机软键盘随之反复开合，进而视口Jitter、快捷键栏上下跳、第二行
    // 点不到（手指落到Terminal画面上→收键盘）。Status指纹没变时直接跳过，打破死循环。
    var guardBox = container;
    if (term.element && document.getElementById("shellgate-mobile-bar")) {
      guardBox = term.element;
    }
    if (term.__sgFitDone && term.__sgLastFitKey === tightFitStateKey(term, guardBox) && term.cols && term.rows) {
      return;
    }
    try {
      var vp = term._core && term._core.viewport;
      if (vp) {
        try {
          Object.defineProperty(vp, "scrollBarWidth", {
            configurable: true,
            enumerable: true,
            get: function () {
              return 0;
            },
            set: function () {},
          });
        } catch (_err) {
          try {
            vp.scrollBarWidth = 0;
            vp._scrollBarWidth = 0;
          } catch (_err2) {
            // ignore
          }
        }
      }
    } catch (_err3) {
      // ignore
    }

    try {
      if (typeof term.fit === "function") term.fit();
    } catch (_err4) {
      window.dispatchEvent(new Event("resize"));
    }

    var screen = term.element && term.element.querySelector(".xterm-screen");
    if (!screen || !term.cols || !term.rows) return;
    // 先撤掉上一轮补出来的 scale 再量：getBoundingClientRect 返回的是含 transform 的
    // 视觉尺寸，带着旧 scale 量会得出「已经没有空隙」的假象，于是清空 transform，
    // 黑边又回来——两次事件之间来回翻烧饼。每轮都从真实布局尺寸重新算。
    screen.style.transform = "";

    // 手机栏模式下 xterm 已留出 bottom，若仍按 #terminal-container 量高会把
    // scale 溢出到第二行快捷键上（点第二行=点到 canvas=收键盘）。
    var fitBox = container;
    if (term.element && document.getElementById("shellgate-mobile-bar")) {
      fitBox = term.element;
    }
    var pr = fitBox.getBoundingClientRect();
    var sr = screen.getBoundingClientRect();
    var cellW = sr.width / term.cols;
    var cellH = sr.height / term.rows;
    if (!(cellW > 0) || !(cellH > 0)) return;

    var rightGap = pr.right - sr.right;
    var bottomGap = pr.bottom - sr.bottom;
    var addCols = Math.max(0, Math.floor((rightGap + 0.5) / cellW));
    var addRows = Math.max(0, Math.floor((bottomGap + 0.5) / cellH));
    if (addCols || addRows) {
      try {
        term.resize(term.cols + addCols, term.rows + addRows);
      } catch (_err5) {
        // ignore
      }
      sr = screen.getBoundingClientRect();
      rightGap = pr.right - sr.right;
      bottomGap = pr.bottom - sr.bottom;
    }

    if ((rightGap > 0.5 || bottomGap > 0.5) && sr.width > 0 && sr.height > 0) {
      var sx = (sr.width + Math.max(0, rightGap)) / sr.width;
      var sy = (sr.height + Math.max(0, bottomGap)) / sr.height;
      screen.style.transformOrigin = "0 0";
      screen.style.transform = "scale(" + sx + ", " + sy + ")";
      report("terminal-tight-fit", {
        addCols: addCols,
        addRows: addRows,
        rightGap: Math.round(rightGap),
        bottomGap: Math.round(bottomGap),
        sx: Math.round(sx * 1000) / 1000,
        sy: Math.round(sy * 1000) / 1000,
      });
    } else {
      screen.style.transform = "";
    }

    // 指纹按「贴合完成后」的实际Status记录：下次进来若外界什么都没动，算出来的指纹
    // 与这里一致即跳过；量不到网格（渲染还没就绪）时不写指纹，留给后续重试。
    term.__sgFitDone = true;
    term.__sgLastFitKey = tightFitStateKey(term, guardBox);
  }

  function installTightFitOnResize(term) {
    var container =
      document.getElementById("terminal-container") ||
      (term.element && term.element.parentElement);
    if (!container) return;
    var timer = null;
    var running = false;
    function run() {
      // term.resize() 会同步派发 onResize，防止由此递归重入。
      if (running) return;
      running = true;
      try {
        tightFitTerminal(term, container);
      } finally {
        running = false;
      }
    }
    function schedule(delay) {
      clearTimeout(timer);
      timer = setTimeout(run, delay);
    }
    run();
    // 首屏重试：字体加载、ttyd 握手后套用字号/渲染器都会在建连后改变单元格尺寸，
    // 而容器尺寸一动不动——不重算就会在右侧留下一条黑边，直到用户手动改窗口大小。
    setTimeout(run, 300);
    setTimeout(run, 1000);
    setTimeout(run, 2500);
    if (document.fonts && document.fonts.ready && typeof document.fonts.ready.then === "function") {
      document.fonts.ready
        .then(function () {
          schedule(50);
        })
        .catch(function () {});
    }
    if (typeof term.onResize === "function") {
      try {
        term.onResize(function () {
          schedule(50);
        });
      } catch (_err) {
        // ignore
      }
    }
      window.addEventListener("resize", function () {
        schedule(100);
      });
      if (window.visualViewport) {
        window.visualViewport.addEventListener("resize", function () {
          if (shouldSkipMobileKeyboardViewportFit()) return;
          schedule(100);
        });
      }
  }

  // ---------- 手机端：键盘避让 + 触摸滚动 + 快捷键栏 ----------
  // 工作台父页已用 visualViewport 把 iframe 压到键盘上方，并在键盘第一次升起后锁住高度；
  // 这里在 iframe 内再留出快捷键栏高度、触发 ttyd 的 fit，并把单指滑动转成本地滚动 / SGR 滚轮
  // （canvas 不处理触摸；合成 WheelEvent 在 iOS 上不可靠）。
  //
  // 快捷键栏作为Terminal容器的同级 flex 区域参与排版，绝不覆盖Terminal；按键在 pointerdown
  // 阶段阻止默认聚焦、在 pointerup 阶段执行，既保住系统键盘又避免触摸重复触发。
    function shouldEnableMobileUX() {
      // 只认「触摸优先」设备。桌面端即使窗口变窄、带触控板/触屏、或浏览器暴露
      // 触摸相关全局属性，也绝不能开快捷键栏——否则栏会按手机视口逻辑卡在
      // 画面中间，挡住桌面操作。旧逻辑用窄窗/触摸属性单独判断，桌面误伤严重。
      try {
        var coarse = window.matchMedia("(pointer: coarse)").matches;
        var noHover = window.matchMedia("(hover: none)").matches;
        return coarse && noHover;
      } catch (_err) {
        return false;
      }
    }

    // 键盘开合会打 visualViewport resize，但宽度没变。父页已经锁住工作台高度，
    // iframe 再 fit 只会改行列、闪键盘。转屏（宽度变了）才放行。
    function shouldSkipMobileKeyboardViewportFit() {
      if (!shouldEnableMobileUX()) return false;
      var w = Math.round((window.visualViewport && window.visualViewport.width) || window.innerWidth);
      if (!lastMobileVvWidth) {
        lastMobileVvWidth = w;
        return true;
      }
      if (Math.abs(w - lastMobileVvWidth) > 8) {
        lastMobileVvWidth = w;
        return false;
      }
      return true;
    }

  function toolbarHeightPx(toolbar) {
    return Math.max((toolbar && toolbar.offsetHeight) || 0, MOBILE_TOOLBAR_MIN_HEIGHT);
  }

  function installMobileChrome(term) {
    installMobileStyles();
    var container =
      document.getElementById("terminal-container") ||
      (term.element && term.element.parentElement) ||
      document.body;
    var toolbar = buildMobileToolbar(term);
    if (container.parentElement) {
      container.insertAdjacentElement("afterend", toolbar);
    } else {
      document.body.appendChild(toolbar);
    }
    placeScreenshotAboveToolbar(MOBILE_TOOLBAR_MIN_HEIGHT);
    installTouchScroll(term);
    installTapToFocus(term);
    installMobileLayout(term, toolbar, container);
    report("mobile-chrome-enabled", {
      coarse: !!(window.matchMedia && window.matchMedia("(pointer: coarse)").matches),
      maxTouchPoints: navigator.maxTouchPoints || 0,
    });
  }

  function installMobileStyles() {
    if (document.getElementById("shellgate-mobile-style")) return;
    var style = document.createElement("style");
    style.id = "shellgate-mobile-style";
    style.textContent = [
      "html,body{height:100%;margin:0!important;padding:0!important;overflow:hidden!important;overscroll-behavior:none;border:0!important;background:var(--sg-term-bg,#000)!important;}",
      "body{display:flex!important;flex-direction:column!important;min-height:0!important;}",
      "#terminal-container{",
      "position:relative!important;inset:auto!important;flex:1 1 auto!important;min-height:0!important;",
      "margin:0!important;padding:0!important;border:0!important;box-sizing:border-box!important;",
      "width:100%!important;height:auto!important;max-width:none!important;background:var(--sg-term-bg,#000)!important;overflow:hidden!important;",
      "}",
      "#terminal-container .terminal,#terminal-container .xterm,.terminal.xterm,.xterm{",
      "position:absolute!important;inset:0!important;",
      "padding:0!important;margin:0!important;border:0!important;",
      "width:auto!important;height:auto!important;",
      "}",
      ".xterm-viewport,.xterm-screen{padding:0!important;margin:0!important;border:0!important;}",
      ".xterm-viewport{",
      "overflow-y:auto!important;overscroll-behavior:none;scrollbar-width:none!important;",
      "-ms-overflow-style:none!important;background:var(--sg-term-bg,#000)!important;",
      "}",
      ".xterm-viewport::-webkit-scrollbar{width:0!important;height:0!important;display:none!important;}",
      ".xterm-screen{touch-action:none!important;}",
      "#shellgate-mobile-bar{",
      "position:relative;flex:0 0 auto;z-index:2;pointer-events:auto;",
      "display:flex;flex-direction:column;gap:4px;align-items:stretch;",
      "box-sizing:border-box;width:100%;",
      // 不再叠加 env(safe-area-inset-bottom)：工作台父页已用 visualViewport 把可视区
      // 压到不含系统条的高度，这里再补一次安全区就是白白多出一截底部留白（用户反馈）。
      "min-height:" + MOBILE_TOOLBAR_MIN_HEIGHT + "px;",
      "padding:6px;",
      "background:var(--sg-chip-bg,rgba(12,14,13,0.94));border-top:1px solid var(--sg-chip-border,rgba(255,255,255,0.12));",
      "overflow:hidden;backdrop-filter:blur(10px);touch-action:none;",
      "}",
      "#shellgate-mobile-bar .sg-row{",
      "display:flex;flex-direction:row;gap:4px;width:100%;align-items:stretch;",
      "}",
      "#shellgate-mobile-bar .sg-key{",
      "flex:1 1 0;min-width:0;",
      "box-sizing:border-box;height:36px;padding:0 2px;",
      "display:flex;align-items:center;justify-content:center;",
      "border-radius:8px;border:1px solid var(--sg-key-border,rgba(255,255,255,0.18));",
      "background:var(--sg-key-bg,rgba(255,255,255,0.08));color:var(--sg-key-fg,#e9f1ec);",
      "font:600 12px/1 ui-monospace,SFMono-Regular,Menlo,monospace;",
      "appearance:none;-webkit-appearance:none;-webkit-user-select:none;user-select:none;touch-action:none;",
      "-webkit-tap-highlight-color:transparent;cursor:pointer;",
      "}",
      "#shellgate-mobile-bar .sg-key:active,#shellgate-mobile-bar .sg-key.pressed{",
      "background:var(--sg-key-active-bg,rgba(111,214,166,0.28));border-color:var(--sg-key-active-border,rgba(111,214,166,0.55));",
      "}",
      "#shellgate-mobile-bar .sg-key.on{",
      "background:var(--sg-key-on-bg,rgba(111,214,166,0.35));border-color:var(--sg-key-on-border,rgba(111,214,166,0.7));color:var(--sg-key-on-fg,#fff);",
      "}",
    ].join("");
    document.head.appendChild(style);
  }

  function buildMobileToolbar(term) {
    var bar = document.createElement("div");
    bar.id = "shellgate-mobile-bar";
    var row1 = document.createElement("div");
    row1.className = "sg-row";
    var row2 = document.createElement("div");
    row2.className = "sg-row";
    bar.appendChild(row1);
    bar.appendChild(row2);
    var ctrlArmed = false;
    var ctrlBtn = null;
    var altArmed = false;
    var altBtn = null;

    // 工具栏的任何落点都不能成为新的焦点。只阻止浏览器默认聚焦，不拦截事件继续
    // 到达具体按键；这样系统键盘保持原状，也不需要事后再抢焦点。
    function preserveTerminalFocus(e) {
      e.preventDefault();
    }
    bar.addEventListener("pointerdown", preserveTerminalFocus, true);
    bar.addEventListener("touchstart", preserveTerminalFocus, { passive: false, capture: true });
    bar.addEventListener("mousedown", preserveTerminalFocus, true);

    function addBtn(label, onTap, opts, row) {
      var btn = document.createElement("button");
      btn.type = "button";
      btn.tabIndex = -1;
      btn.className = "sg-key";
      btn.textContent = label;
      btn.setAttribute("aria-label", (opts && opts.title) || label);
      if (opts && opts.title) btn.title = opts.title;

      var activePointer = null;
      var startX = 0;
      var startY = 0;
      var lastPointerFire = 0;

      function fire(via) {
        onTap(btn);
        report("mobile-tap", { label: label, via: via, row: row === row1 ? 1 : 2 });
      }

      btn.addEventListener("pointerdown", function (e) {
        if (!e.isPrimary || (e.button !== undefined && e.button !== 0)) return;
        e.preventDefault();
        e.stopPropagation();
        activePointer = e.pointerId;
        startX = e.clientX;
        startY = e.clientY;
        btn.classList.add("pressed");
        try { btn.setPointerCapture(e.pointerId); } catch (_err) { /* ignore */ }
      });
      btn.addEventListener("pointermove", function (e) {
        if (e.pointerId !== activePointer) return;
        if (Math.abs(e.clientX - startX) > 14 || Math.abs(e.clientY - startY) > 14) {
          activePointer = null;
          btn.classList.remove("pressed");
        }
      });
      btn.addEventListener("pointerup", function (e) {
        if (e.pointerId !== activePointer) return;
        e.preventDefault();
        e.stopPropagation();
        activePointer = null;
        btn.classList.remove("pressed");
        lastPointerFire = Date.now();
        fire("pointerup");
      });
      btn.addEventListener("pointercancel", function () {
        activePointer = null;
        btn.classList.remove("pressed");
      });
      btn.addEventListener("click", function (e) {
        e.preventDefault();
        e.stopPropagation();
        if (Date.now() - lastPointerFire < 500) return;
        fire("click");
      });

      row.appendChild(btn);
      return btn;
    }

    addBtn("/", function () {
      sendToPty(term, "/");
    }, null, row1);
    addBtn("@", function () {
      sendToPty(term, "@");
    }, null, row1);
    addBtn("Esc", function () {
      sendToPty(term, "\x1b");
    }, { title: "Escape" }, row1);
    addBtn("Tab", function () {
      sendToPty(term, "\t");
    }, null, row1);
    addBtn("Paste", function () {
      pasteFromClipboard(term);
    }, null, row1);
    addBtn("^C", function () {
      sendToPty(term, "\x03");
    }, { title: "Ctrl+C" }, row1);
    ctrlBtn = addBtn("Ctrl", function (btn) {
      ctrlArmed = !ctrlArmed;
      btn.classList.toggle("on", ctrlArmed);
      report("mobile-ctrl-toggle", { armed: ctrlArmed });
    }, { title: "Sticky Ctrl: tap, then type a letter" }, row2);
    // 每行固定 6 键，第二行补一个粘滞 Alt（Alt+B/F 按词移动、Alt+. 取上条参数等常用）。
    altBtn = addBtn("Alt", function (btn) {
      altArmed = !altArmed;
      btn.classList.toggle("on", altArmed);
      report("mobile-alt-toggle", { armed: altArmed });
    }, { title: "Sticky Alt: tap, then type a letter" }, row2);
    addBtn("↑", function () {
      sendToPty(term, "\x1b[A");
    }, null, row2);
    addBtn("↓", function () {
      sendToPty(term, "\x1b[B");
    }, null, row2);
    addBtn("←", function () {
      sendToPty(term, "\x1b[D");
    }, null, row2);
    addBtn("→", function () {
      sendToPty(term, "\x1b[C");
    }, null, row2);

    document.addEventListener(
      "keydown",
      function (e) {
        if (!ctrlArmed && !altArmed) return;
        if (e.key === "Control" || e.key === "Shift" || e.key === "Alt" || e.key === "Meta") return;
        if (e.key.length !== 1) return;
        e.preventDefault();
        e.stopPropagation();
        var data = e.key;
        if (ctrlArmed) {
          var code = e.key.toUpperCase().charCodeAt(0);
          if (code >= 0x40 && code <= 0x5f) data = String.fromCharCode(code & 0x1f);
        }
        // Alt+键在Terminal里就是 ESC 前缀（Meta-sends-Escape），可与 Ctrl 叠加。
        if (altArmed) data = "\x1b" + data;
        sendToPty(term, data);
        report("mobile-chord", { key: e.key, ctrl: ctrlArmed, alt: altArmed });
        ctrlArmed = false;
        altArmed = false;
        if (ctrlBtn) ctrlBtn.classList.remove("on");
        if (altBtn) altBtn.classList.remove("on");
      },
      true,
    );

    return bar;
  }

  function pasteFromClipboard(term) {
    if (navigator.clipboard && navigator.clipboard.readText) {
      navigator.clipboard.readText().then(
        function (text) {
          if (!text) {
            report("mobile-paste", { ok: false, reason: "empty" });
            return;
          }
          term.paste(text);
          report("mobile-paste", { ok: true, len: text.length });
        },
        function (err) {
          report("mobile-paste", { ok: false, errorName: err && err.name });
          showPasteFallback(term);
        },
      );
      return;
    }
    showPasteFallback(term);
  }

  function showPasteFallback(term) {
    var ov = document.createElement("textarea");
    ov.setAttribute("aria-label", "Paste into terminal");
    ov.placeholder = "Long press here to paste";
    ov.style.cssText = [
      "position:fixed",
      "left:50%",
      "top:40%",
      "transform:translate(-50%,-50%)",
      "z-index:2147483647",
      "width:min(280px,86vw)",
      "height:88px",
      "padding:10px",
      "border-radius:10px",
      "border:1px solid var(--sg-key-active-border,rgba(111,214,166,0.55))",
      "background:var(--sg-panel-solid-bg,#121816)",
      "color:var(--sg-panel-fg,#e9f1ec)",
      "font-size:16px",
    ].join(";");
    document.body.appendChild(ov);
    ov.focus();
    ov.addEventListener("paste", function (ev) {
      var text = (ev.clipboardData || window.clipboardData).getData("text");
      if (text) term.paste(text);
      report("mobile-paste-fallback", { ok: !!text, len: text ? text.length : 0 });
      ov.remove();
    });
    ov.addEventListener("blur", function () {
      setTimeout(function () {
        if (document.body.contains(ov)) ov.remove();
      }, 350);
    });
  }

  // 把原始按键字节送进 PTY：优先走 xterm 与键盘相同的 onData 通道
  // （不会被括号Paste模式包一层）；失败再退到 ttyd websocket / paste。
  function sendToPty(term, data) {
    try {
      if (term._core && term._core._onData && typeof term._core._onData.fire === "function") {
        term._core._onData.fire(data);
        return;
      }
    } catch (_err) {
      // ignore
    }
    try {
      if (window._S && window._S.readyState === 1) {
        window._S.send("0" + data);
        return;
      }
    } catch (_err) {
      // ignore
    }
    term.paste(data);
  }

  // 手机端：点一下Terminal必须能唤起系统键盘。
  // iOS / Android 只在「用户手势里真的发生了一次聚焦」时才弹键盘，而 ttyd 在页面加载时
  // 就把隐藏输入框程序化聚焦了（那次不算手势，不弹键盘）；此后再点Terminal，焦点没有变化、
  // 不产生新的聚焦事件，键盘就可能一直不出来。这里在干净的点击（未滑动）里主动把焦点
  // 交回隐藏输入框。
  // 2026-09-14：键盘已经打开时禁止再 blur/focus——那会闪键盘并逼父页重算Terminal网格。
  // 只有父页判定键盘已收（keyboardHeldOpen=false）才允许用一次真实聚焦把键盘唤回。
  // 全程不 preventDefault，浏览器原生行为照旧。
  function installTapToFocus(term) {
    var host = term.element || document.querySelector(".xterm");
    if (!host) {
      setTimeout(function () {
        installTapToFocus(term);
      }, 400);
      return;
    }

    var startX = 0;
    var startY = 0;
    var moved = false;

    function onStart(e) {
      if (e.touches.length !== 1) return;
      startX = e.touches[0].clientX;
      startY = e.touches[0].clientY;
      moved = false;
    }

    function onMove(e) {
      if (e.touches.length !== 1) return;
      var t = e.touches[0];
      if (Math.abs(t.clientX - startX) > 8 || Math.abs(t.clientY - startY) > 8) moved = true;
    }

    function onEnd(e) {
      if (moved) return;
      // Terminal容器与画布层层嵌套，同一次 touchend 会在多个监听目标上冒泡；只处理一次，
      // 否则连着 blur/focus 好几轮，键盘会闪。
      if (e.__shellgateTapFocus) return;
      e.__shellgateTapFocus = true;
      if (e.target && e.target.closest && e.target.closest("#shellgate-mobile-bar")) return;
      var ta = term.textarea || document.querySelector(".xterm-helper-textarea");
      if (!ta) return;
      var was = document.activeElement === ta;
      if (was && keyboardHeldOpen) {
        report("mobile-tap-focus", { wasFocused: true, nowFocused: true, kept: true });
        return;
      }
      try {
        if (was) ta.blur();
        ta.focus({ preventScroll: true });
      } catch (_err) {
        try {
          ta.focus();
        } catch (_err2) {
          // ignore
        }
      }
      report("mobile-tap-focus", { wasFocused: was, nowFocused: document.activeElement === ta, kept: false });
    }

    var targets = [host];
    var screen = host.querySelector(".xterm-screen");
    if (screen) targets.push(screen);
    if (term.textarea) targets.push(term.textarea);
    var container = document.getElementById("terminal-container");
    if (container) targets.push(container);

    for (var i = 0; i < targets.length; i++) {
      targets[i].addEventListener("touchstart", onStart, { passive: true });
      targets[i].addEventListener("touchmove", onMove, { passive: true });
      targets[i].addEventListener("touchend", onEnd, { passive: true });
    }
  }

  function installTouchScroll(term) {
    var host = term.element || document.querySelector(".xterm");
    if (!host) {
      setTimeout(function () {
        installTouchScroll(term);
      }, 400);
      return;
    }

    var lastY = 0;
    var scrolling = false;
    var reported = false;

    function viewportEl() {
      return host.querySelector(".xterm-viewport");
    }

    // 普通 shell：本地滚 viewport / scrollLines。
    // 全屏 TUI（无本地滚动余量）：发 SGR 滚轮，Corral 等开了鼠标上报的应用能吃到。
    function applyScroll(deltaY, clientX, clientY) {
      if (!deltaY) return;
      var vp = viewportEl();
      if (vp) {
        var before = vp.scrollTop;
        vp.scrollTop = before + deltaY;
        if (vp.scrollTop !== before) {
          return "viewport";
        }
      }
      if (typeof term.scrollLines === "function") {
        var lines = Math.round(deltaY / 12) || (deltaY > 0 ? 1 : -1);
        var yBefore = 0;
        try {
          yBefore = term.buffer && term.buffer.active ? term.buffer.active.viewportY : 0;
        } catch (_err) {
          // ignore
        }
        term.scrollLines(lines);
        try {
          var yAfter = term.buffer && term.buffer.active ? term.buffer.active.viewportY : yBefore;
          if (yAfter !== yBefore) return "scrollLines";
        } catch (_err2) {
          // fall through to mouse wheel
        }
      }
      sendMouseWheel(term, deltaY, clientX, clientY, host);
      return "sgr-wheel";
    }

    function onStart(e) {
      if (e.touches.length !== 1) return;
      // 快捷键栏上的滑动不要抢
      if (e.target && e.target.closest && e.target.closest("#shellgate-mobile-bar")) return;
      lastY = e.touches[0].clientY;
      scrolling = false;
    }

    function onMove(e) {
      if (e.touches.length !== 1) return;
      if (e.target && e.target.closest && e.target.closest("#shellgate-mobile-bar")) return;
      var touch = e.touches[0];
      var y = touch.clientY;
      var delta = lastY - y;
      if (!scrolling && Math.abs(delta) < 4) return;
      scrolling = true;
      lastY = y;
      e.preventDefault();
      e.stopPropagation();
      // 手指位移到滚动量的放大系数：2.5 → 1.25 仍偏灵敏，2026-08-02 再降到 1（1:1 跟手）。
      var mode = applyScroll(delta * 1, touch.clientX, touch.clientY);
      if (!reported) {
        reported = true;
        report("mobile-scroll", { mode: mode || "none" });
      }
    }

    function onEnd() {
      lastY = 0;
      scrolling = false;
    }

    // 触摸落点多半是盖在画面上的隐藏 textarea / canvas，只听 .xterm 会丢事件。
    var targets = [host];
    var screen = host.querySelector(".xterm-screen");
    if (screen) targets.push(screen);
    if (term.textarea) targets.push(term.textarea);
    var container = document.getElementById("terminal-container");
    if (container) targets.push(container);

    for (var i = 0; i < targets.length; i++) {
      targets[i].addEventListener("touchstart", onStart, { passive: true, capture: true });
      targets[i].addEventListener("touchmove", onMove, { passive: false, capture: true });
      targets[i].addEventListener("touchend", onEnd, { passive: true, capture: true });
      targets[i].addEventListener("touchcancel", onEnd, { passive: true, capture: true });
    }
  }

  // SGR 鼠标滚轮：\x1b[<64;col;rowM 上滚，65 下滚。坐标按 cell 估算。
  function sendMouseWheel(term, deltaY, clientX, clientY, host) {
    var cols = term.cols || 80;
    var rows = term.rows || 24;
    var rect = (host && host.getBoundingClientRect) ? host.getBoundingClientRect() : { left: 0, top: 0, width: 1, height: 1 };
    var col = Math.min(cols, Math.max(1, Math.ceil(((clientX - rect.left) / Math.max(rect.width, 1)) * cols)));
    var row = Math.min(rows, Math.max(1, Math.ceil(((clientY - rect.top) / Math.max(rect.height, 1)) * rows)));
    var notches = Math.min(6, Math.max(1, Math.round(Math.abs(deltaY) / 28)));
    var btn = deltaY > 0 ? 65 : 64;
    var seq = "";
    for (var i = 0; i < notches; i++) {
      seq += "\x1b[<" + btn + ";" + col + ";" + row + "M";
    }
    sendToPty(term, seq);
  }

  function installMobileLayout(term, toolbar, container) {
    if (!container) {
      container =
        document.getElementById("terminal-container") ||
        (term.element && term.element.parentElement) ||
        document.body;
    }
    var frame = 0;
    var pendingReason = "init";
    var lastLayoutKey = "";

    function renderLayout(reason) {
      var toolbarH = toolbarHeightPx(toolbar);
      if (toolbar.previousElementSibling !== container && container.parentElement) {
        container.insertAdjacentElement("afterend", toolbar);
      }
      var containerRect = container.getBoundingClientRect();
      var toolbarRect = toolbar.getBoundingClientRect();
      var key = Math.round(containerRect.width) + ":" + Math.round(containerRect.height) + ":" + toolbarH;
      if (key === lastLayoutKey) return;
      lastLayoutKey = key;
      placeScreenshotAboveToolbar(toolbarH);
      tightFitTerminal(term, container);
      report("mobile-layout", {
        containerH: Math.round(containerRect.height),
        toolbarH: toolbarH,
        toolbarTop: Math.round(toolbarRect.top),
        toolbarBottom: Math.round(toolbarRect.bottom),
        viewportH: window.innerHeight,
        why: reason,
      });
    }

    function schedule(reason) {
      pendingReason = reason || "未知";
      if (frame) return;
      frame = window.requestAnimationFrame(function () {
        frame = 0;
        renderLayout(pendingReason);
      });
    }

    schedule("init");
    setTimeout(function () { schedule("settled"); }, 300);
    window.addEventListener("resize", function () { schedule("win-resize"); });
    if (window.visualViewport) {
      window.visualViewport.addEventListener("resize", function () {
        if (shouldSkipMobileKeyboardViewportFit()) return;
        schedule("vv-resize");
      });
    }
    if (typeof ResizeObserver !== "undefined") {
      try {
        var observer = new ResizeObserver(function () { schedule("element-resize"); });
        observer.observe(container);
        observer.observe(toolbar);
      } catch (_err) {
        // ignore
      }
    }
  }
})();
