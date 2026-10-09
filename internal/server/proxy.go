package server

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type proxyUserKey struct{}

type proxyCmdKey struct{}

func newTTYDProxy(target *url.URL, mon *netMonitor) *httputil.ReverseProxy {
	proxy := &httputil.ReverseProxy{
		Rewrite: func(req *httputil.ProxyRequest) {
			req.SetURL(target)
			req.Out.Host = target.Host
			// 先剥掉客户端可能伪造的身份头，再写入门禁已校验的身份。
			req.Out.Header.Del(authHeaderName)
			req.Out.Header.Del("X-WEBAUTH-USER")
			req.Out.Header.Del("Cookie")
			req.Out.Header.Del("Authorization")
			req.Out.Header.Set("Accept-Encoding", "gzip")
			if email, ok := req.In.Context().Value(proxyUserKey{}).(string); ok && email != "" {
				req.Out.Header.Set(authHeaderName, email)
			}
			// 服务端注入启动命令；剥掉客户端自带的 arg，避免任意命令注入。
			// 空命令也照样注入：包装里带的「网页终端」标记对纯打字的 Tab 同样要生效。
			q := req.Out.URL.Query()
			q.Del("arg")
			cmd, _ := req.In.Context().Value(proxyCmdKey{}).(string)
			q.Set("arg", interactiveLoginCommand(strings.TrimSpace(cmd)))
			req.Out.URL.RawQuery = q.Encode()
		},
	}
	// 自带 Transport：拨号口既是上游握手时延的免费采样点，也是统计转发字节的唯一位置
	// （ttyd 走 WebSocket，101 之后反代只是裸字节对拷，响应钩子看不到终端流量）。
	// 关掉 HTTP/2 尝试：WebSocket 升级必须走 HTTP/1.1。
	// Proxy 刻意不用 ProxyFromEnvironment：上游是 ZeroTier 内网 ttyd，走环境代理会把
	// X-Webauth-User 与终端流量送到外部代理，属于放行策略错误。
	proxy.Transport = &http.Transport{
		Proxy:                 nil,
		DialContext:           mon.dialContext,
		ForceAttemptHTTP2:     false,
		MaxIdleConns:          32,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
	}
	proxy.ModifyResponse = injectEnhanceScript
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		if errors.Is(err, context.Canceled) {
			return
		}
		mon.noteProxyError()
		log.Printf("反代 ttyd 失败: %v", err)
		http.Error(w, "终端上游不可用", http.StatusBadGateway)
	}
	return proxy
}

// interactiveLoginCommand 把预置/自定义启动命令包成「登录 + 交互」shell 再执行，
// 保证快捷命令的效果与用户自己在终端里敲一遍完全一致。
//
// 背景：上游 ttyd 的启动脚本收到 arg 后走的是 `bash -lc "$*"`，只读 /etc/profile 与
// ~/.profile，**不读 ~/.bashrc**；而用户手敲命令用的是交互式 shell，~/.bashrc 里的
// PATH 追加、别名、函数、各类工具初始化全都生效。两者 PATH 顺序不同，同名命令会解析
// 到不同副本——2026-08-02 实测：预置的 subswap 命中 ~/.cargo/bin 下的 0.3.25 旧版，
// 而终端里敲到的是 ~/.local/bin 下的 1.4.11 新版。加 -i 让 ~/.bashrc 也走一遍即对齐。
//
// 命令跑完必须落回交互 shell，不能让 shell 直接退出：终端页面的唯一进程就是这个 shell，
// 它一退出 ttyd 就关掉 WebSocket，而 enhance.js 的断线恢复层会重载页面、反代又重新注入
// 同一条启动命令——2026-08-02 实测 subswap（会正常结束的一次性命令）因此陷入
// 「跑完→断开→自动重连→再跑一遍」的死循环。常驻型程序（Corral）不退出所以从未暴露。
// 末尾用换行而不是 `;` 拼接，避免命令以注释或后台符号结尾时把收尾语句吞掉。
//
// **换行只能是 bash 自己展开出来的，绝不能是 URL 里的真实换行字符**：整条命令是作为
// ttyd 的 `arg` 查询参数传过去的，2026-08-03 实测只要参数里出现 `%0A`，ttyd 依赖的
// libwebsockets 会直接判握手失败（日志 `hs0405 has failed the connection`，门禁侧看到
// 反代 EOF → 浏览器 502），带命令的 Tab 全部连不上，只有空 shell 还能开。所以这里用
// bash 的 ANSI-C 引用 `$'...'`，URL 里只是反斜杠加 n 两个普通字符，换行由 bash 展开。
//
// 预置命令与收尾交互壳都跟账号的 $SHELL（Mac 上是 zsh+starship，终端节点是 bash+ble.sh）。
// 必须用 `"$SHELL" -lic`，不能写死 bash：否则 Mac 点预置会整段跑在 bash 里，看不到
// zsh 插件；终端节点 $SHELL 本就是 bash，行为与旧写法一致。ANSI-C 引用 `$'...'` 在
// bash/zsh 都可用。空命令（「＋ shell」）也要注入 arg：上游无参脚本会开 bash -l，
// Mac 进不了默认 zsh。2026-09-05 起不再导出 SHELLGATE_WEB_TERM——网页端默认还原
// 本机插件，接受回显变慢。
func interactiveLoginCommand(cmd string) string {
	live := `exec "$SHELL" -li`
	if cmd == "" {
		return live
	}
	return `exec "$SHELL" -lic ` + ansiCQuote(cmd+"\n"+live)
}

// ansiCQuote 用 ANSI-C 引用（`$'...'`）把字符串包成字面量：反斜杠、单引号转义，
// 换行写成 `\n` 两个字符交给 bash/zsh 展开。选它而不是普通单引号，是因为普通单引号
// 里没法表达换行（见 interactiveLoginCommand 的说明）。
func ansiCQuote(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `'`, `\'`, "\n", `\n`, "\r", `\r`)
	return "$'" + r.Replace(s) + "'"
}

// injectEnhanceScript 给 ttyd 返回的终端页面（text/html）注入增强脚本标签，
// 处理响应可能带的 gzip 压缩并同步更新 Content-Length。非 HTML 响应原样放行。
func injectEnhanceScript(resp *http.Response) error {
	if !strings.Contains(resp.Header.Get("Content-Type"), "text/html") {
		return nil
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		return err
	}

	gzipped := strings.EqualFold(resp.Header.Get("Content-Encoding"), "gzip")
	raw := body
	if gzipped {
		gr, gerr := gzip.NewReader(bytes.NewReader(body))
		if gerr != nil {
			// 解压失败就原样放行，避免打断终端页面加载。
			resp.Body = io.NopCloser(bytes.NewReader(body))
			resp.ContentLength = int64(len(body))
			return nil
		}
		decoded, derr := io.ReadAll(gr)
		_ = gr.Close()
		if derr != nil {
			resp.Body = io.NopCloser(bytes.NewReader(body))
			resp.ContentLength = int64(len(body))
			return nil
		}
		raw = decoded
	}

	out := insertBeforeBodyClose(raw, []byte(enhanceScriptTag))

	if gzipped {
		var buf bytes.Buffer
		gw := gzip.NewWriter(&buf)
		if _, werr := gw.Write(out); werr != nil {
			return werr
		}
		if cerr := gw.Close(); cerr != nil {
			return cerr
		}
		out = buf.Bytes()
	}

	resp.Body = io.NopCloser(bytes.NewReader(out))
	resp.ContentLength = int64(len(out))
	resp.Header.Set("Content-Length", strconv.Itoa(len(out)))
	return nil
}

// insertBeforeBodyClose 把 tag 插到 </body> 之前（大小写不敏感查找）；
// 找不到闭合标签就直接追加到末尾，保证脚本总能被加载。
func insertBeforeBodyClose(html []byte, tag []byte) []byte {
	idx := bytes.LastIndex(bytes.ToLower(html), []byte("</body>"))
	if idx < 0 {
		out := make([]byte, 0, len(html)+len(tag))
		out = append(out, html...)
		out = append(out, tag...)
		return out
	}
	out := make([]byte, 0, len(html)+len(tag))
	out = append(out, html[:idx]...)
	out = append(out, tag...)
	out = append(out, html[idx:]...)
	return out
}
