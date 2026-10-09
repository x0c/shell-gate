package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// 网络监控：把「浏览器 ↔ 门禁 ↔ 终端主机」这条链路拆成三段分别观测。
//
//   - 上行段（门禁 ↔ 终端主机）：只有门禁自己测得到，本文件负责——真实建连的握手耗时、
//     主动探测的 TCP/首字节耗时、代理转发的双向字节与速率、连接数与失败计数。
//   - 下行段（浏览器 ↔ 门禁）：只有浏览器测得到（服务端无从知道客户端到自己的往返），
//     由 web/workspace.html 打 /api/netmon/ping 计时。
//   - 端到端（按键 → 终端回显）：同样只能在浏览器侧测，由 web/enhance.js 在 ttyd 的
//     WebSocket 上做「发出一次输入 → 收到下一帧输出」的单时钟往返计时。这是终端「卡不卡」
//     的直接指标：它含远端应用处理时间，比纯网络 RTT 更贴近体感。
//
// 之所以坚持「单时钟往返」而不是「发送方打时间戳」：两端系统时钟不同步，单向时延算不准；
// WebSocket 又没有请求-响应语义，必须自己构造往返。浏览器 WebSocket API 也不暴露原生
// ping/pong，所以下行段用 HTTP 探针、端到端用回显，都是在同一个时钟上取两次时间差。
//
// 浏览器把自己那两段的观测推到 /api/netmon/sample 汇总，这样监控面板和离线日志
// （perf-log.ndjson 里的 netmon-sample）看到的是同一份数据。

const (
	defaultNetmonInterval = 15 * time.Second
	netmonProbeTimeout    = 8 * time.Second
	rttWindowSize         = 120 // 每类时延保留最近 120 个样本
	throughputWindowSec   = 60  // 吞吐环形缓冲保留 60 秒，够算 1s/10s/60s 速率
	maxNetmonClients      = 8   // 同时在线的浏览器页面上限，超出按最旧淘汰
	maxNetmonClientTabs   = 24
	netmonClientTTL       = 3 * time.Minute // 超过这个时间没再上报的页面视为已离开
)

// ---------- 时延样本窗口 ----------

// rttWindow 是固定容量的往返时延样本窗口（毫秒），给出分位数与抖动。
type rttWindow struct {
	mu      sync.Mutex
	buf     []float64
	next    int
	filled  bool
	last    float64
	prev    float64
	hasPrev bool
	jitter  float64
	total   int64
}

func newRTTWindow(size int) *rttWindow {
	if size <= 0 {
		size = 1
	}
	return &rttWindow{buf: make([]float64, size)}
}

func (w *rttWindow) add(ms float64) {
	if ms < 0 || math.IsNaN(ms) || math.IsInf(ms, 0) {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf[w.next] = ms
	w.next = (w.next + 1) % len(w.buf)
	if w.next == 0 {
		w.filled = true
	}
	if w.hasPrev {
		// RFC 3550 式抖动估计：对相邻样本差值取指数滑动平均。比标准差更贴近
		// 「忽快忽慢」的体感——总体偏慢但稳定的链路，打字仍然是跟手的。
		w.jitter += (math.Abs(ms-w.prev) - w.jitter) / 16
	}
	w.prev = ms
	w.hasPrev = true
	w.last = ms
	w.total++
}

func (w *rttWindow) addDuration(d time.Duration) {
	w.add(float64(d) / float64(time.Millisecond))
}

// rttStats 是对外暴露的时延统计快照，单位统一为毫秒。
type rttStats struct {
	Count    int64   `json:"count"`
	LastMs   float64 `json:"lastMs"`
	MinMs    float64 `json:"minMs"`
	MaxMs    float64 `json:"maxMs"`
	AvgMs    float64 `json:"avgMs"`
	P50Ms    float64 `json:"p50Ms"`
	P95Ms    float64 `json:"p95Ms"`
	JitterMs float64 `json:"jitterMs"`
}

func (w *rttWindow) stats() rttStats {
	w.mu.Lock()
	n := len(w.buf)
	if !w.filled {
		n = w.next
	}
	vals := make([]float64, n)
	copy(vals, w.buf[:n])
	out := rttStats{Count: w.total, LastMs: w.last, JitterMs: w.jitter}
	w.mu.Unlock()

	if n == 0 {
		return rttStats{}
	}
	sort.Float64s(vals)
	sum := 0.0
	for _, v := range vals {
		sum += v
	}
	out.MinMs = round1(vals[0])
	out.MaxMs = round1(vals[n-1])
	out.AvgMs = round1(sum / float64(n))
	out.P50Ms = round1(percentile(vals, 0.50))
	out.P95Ms = round1(percentile(vals, 0.95))
	out.LastMs = round1(out.LastMs)
	out.JitterMs = round1(out.JitterMs)
	return out
}

// percentile 取已排序切片的分位数（最近秩法）。
func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(math.Ceil(p*float64(len(sorted)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

func round1(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	return math.Round(v*10) / 10
}

// ---------- 吞吐速率环 ----------

type throughputPoint struct {
	at   time.Time
	up   int64
	down int64
}

// throughputRing 每秒记一次累计字节数，用两点差值算任意窗口的平均速率。
type throughputRing struct {
	mu     sync.Mutex
	points []throughputPoint
}

func (r *throughputRing) push(at time.Time, up, down int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.points = append(r.points, throughputPoint{at: at, up: up, down: down})
	if len(r.points) > throughputWindowSec+1 {
		r.points = r.points[len(r.points)-(throughputWindowSec+1):]
	}
}

// rate 返回窗口内的平均速率（字节/秒）。样本不足时用现有最早的点兜底。
func (r *throughputRing) rate(window time.Duration) (upBps, downBps float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.points) < 2 {
		return 0, 0
	}
	last := r.points[len(r.points)-1]
	base := r.points[0]
	for i := len(r.points) - 1; i >= 0; i-- {
		if last.at.Sub(r.points[i].at) >= window {
			base = r.points[i]
			break
		}
		base = r.points[i]
	}
	secs := last.at.Sub(base.at).Seconds()
	if secs <= 0 {
		return 0, 0
	}
	return round1(float64(last.up-base.up) / secs), round1(float64(last.down-base.down) / secs)
}

// ---------- 浏览器侧上报 ----------

// netmonClientTab 是浏览器侧单个终端 Tab 的观测。
type netmonClientTab struct {
	TabID       string  `json:"tabId"`
	Title       string  `json:"title"`
	Machine     string  `json:"machine"` // 该 Tab 开在哪台上游机器上
	Connected   bool    `json:"connected"`
	DownBps     float64 `json:"downBps"`
	UpBps       float64 `json:"upBps"`
	DownBytes   int64   `json:"downBytes"`
	UpBytes     int64   `json:"upBytes"`
	EchoMs      float64 `json:"echoMs"`
	Cols        int     `json:"cols"`
	Rows        int     `json:"rows"`
	RenderPerS  float64 `json:"renderPerSec"`
	LongTaskMs  float64 `json:"longTaskMs"`
	Reconnects  int     `json:"reconnects"`
	CloseCode   int     `json:"closeCode"`
	IdleSeconds float64 `json:"idleSeconds"`
}

// netmonClientSample 是浏览器推上来的下行段与端到端观测。
type netmonClientSample struct {
	ClientID   string            `json:"clientId"`
	Device     string            `json:"device"` // mobile / desktop，粗粒度即可
	Online     bool              `json:"online"`
	NetType    string            `json:"netType"` // navigator.connection.effectiveType，拿不到就空
	GateRTT    rttStats          `json:"gateRtt"` // 浏览器 ↔ 门禁
	EchoRTT    rttStats          `json:"echoRtt"` // 按键 → 终端回显（含远端处理）
	Tabs       []netmonClientTab `json:"tabs"`
	Reconnects int               `json:"reconnects"`
}

// netmonClientView 是快照里返回的浏览器观测，附带「多久之前上报的」。
type netmonClientView struct {
	netmonClientSample
	AgeMs int64 `json:"ageMs"`
}

// netmonClientRecord 是服务端保存的一份浏览器上报，收下时刻单独记，不跟对外的 AgeMs 混用。
type netmonClientRecord struct {
	owner  string
	sample netmonClientSample
	at     time.Time
}

// ---------- 监控器本体 ----------

type netMonitor struct {
	target   *url.URL
	interval time.Duration
	start    time.Time
	// probeUser 是探测请求带的身份头取值。上游 ttyd 用 -H X-Webauth-User 做鉴权，
	// 不带就只回 407——那样量到的是「被拒绝」的耗时，走不到真实处理路径。带上白名单里的
	// 邮箱，探测才和真实开终端走同一条上游代码路径（探首页不会 fork shell，只有 /ws 才会）。
	probeUser string

	// 代理转发字节：up = 门禁 → 终端主机（用户输入方向），down = 终端主机 → 门禁（终端输出方向）
	upBytes    atomic.Int64
	downBytes  atomic.Int64
	connOpened atomic.Int64
	connClosed atomic.Int64
	connActive atomic.Int64
	dialFails  atomic.Int64
	proxyFails atomic.Int64

	dialRTT   *rttWindow // 每次真实建连的握手耗时（免费样本，不额外发包）
	probeTCP  *rttWindow // 主动探测：TCP 建连
	probeTTFB *rttWindow // 主动探测：HTTP 首字节（含上游应用处理）

	rates       throughputRing
	probeClient *http.Client

	mu           sync.Mutex
	probeOK      int64
	probeFail    int64
	probeStreak  int // 连续失败次数
	probeLastAt  time.Time
	probeLastErr string
	probeStatus  int
	clients      map[string]*netmonClientRecord
	connections  map[*countingConn]struct{}
	shuttingDown bool

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

func newNetMonitor(target *url.URL, interval time.Duration, probeUser string) *netMonitor {
	m := &netMonitor{
		target:      target,
		interval:    interval,
		probeUser:   probeUser,
		start:       time.Now(),
		dialRTT:     newRTTWindow(rttWindowSize),
		probeTCP:    newRTTWindow(rttWindowSize),
		probeTTFB:   newRTTWindow(rttWindowSize),
		clients:     make(map[string]*netmonClientRecord),
		connections: make(map[*countingConn]struct{}),
	}
	// 探针必须每次重新建连才能量到握手耗时，所以关掉连接复用；它也不能复用反代的
	// 计数拨号器，否则探测流量会混进业务吞吐统计里。
	m.probeClient = &http.Client{
		Timeout: netmonProbeTimeout,
		Transport: &http.Transport{
			DisableKeepAlives: true,
			DialContext:       (&net.Dialer{Timeout: netmonProbeTimeout}).DialContext,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return m
}

// dialContext 是反代 Transport 的拨号入口：顺手把每次真实建连的握手耗时记成时延样本，
// 并把连接包成计数器，统计经过门禁转发的双向字节。
func (m *netMonitor) dialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	started := time.Now()
	c, err := (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext(ctx, network, addr)
	if err != nil {
		m.dialFails.Add(1)
		return nil, err
	}
	m.dialRTT.addDuration(time.Since(started))
	m.connOpened.Add(1)
	m.connActive.Add(1)
	wrapped := &countingConn{Conn: c, mon: m}
	m.mu.Lock()
	if m.shuttingDown {
		m.mu.Unlock()
		_ = wrapped.Close()
		return nil, net.ErrClosed
	}
	m.connections[wrapped] = struct{}{}
	m.mu.Unlock()
	return wrapped, nil
}

// countingConn 统计一条上游连接的双向字节。
//
// 必须在 net.Conn 这一层统计：ttyd 是 WebSocket，反代在 101 之后就退化成裸字节对拷，
// ModifyResponse 之类的响应钩子完全看不到终端流量。
//
// 刻意不实现 ReadFrom/WriteTo：那两个快路径会绕开这里的 Read/Write 直接搬字节，统计就漏了。
// CloseWrite 则必须转发——反代在单向拷贝结束后靠它做 TCP 半关闭，吞掉会让上游收不到 EOF。
type countingConn struct {
	net.Conn
	mon       *netMonitor
	closeOnce sync.Once
}

func (c *countingConn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	if n > 0 {
		c.mon.downBytes.Add(int64(n))
	}
	return n, err
}

func (c *countingConn) Write(b []byte) (int, error) {
	n, err := c.Conn.Write(b)
	if n > 0 {
		c.mon.upBytes.Add(int64(n))
	}
	return n, err
}

func (c *countingConn) Close() error {
	c.closeOnce.Do(func() {
		c.mon.connActive.Add(-1)
		c.mon.connClosed.Add(1)
		c.mon.mu.Lock()
		delete(c.mon.connections, c)
		c.mon.mu.Unlock()
	})
	return c.Conn.Close()
}

func (c *countingConn) CloseWrite() error {
	if cw, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return cw.CloseWrite()
	}
	return nil
}

func (m *netMonitor) noteProxyError() { m.proxyFails.Add(1) }

// startLoop 起后台采样：每秒记一次吞吐，每 interval 主动探一次上游。
// interval <= 0 表示只做被动统计，不主动发探测包。
func (m *netMonitor) startLoop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	m.wg.Add(1)
	go m.loop(ctx)
}

func (m *netMonitor) stopLoop() {
	m.mu.Lock()
	cancel := m.cancel
	m.cancel = nil
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	m.wg.Wait()
}

func (m *netMonitor) loop(ctx context.Context) {
	defer m.wg.Done()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	var probing atomic.Bool
	lastProbe := time.Time{}
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-tick.C:
			m.rates.push(now, m.upBytes.Load(), m.downBytes.Load())
			if m.interval <= 0 || now.Sub(lastProbe) < m.interval {
				continue
			}
			// 探测走独立 goroutine：上游不可达时单次探测会挂满超时，不能拖住每秒的吞吐采样。
			if probing.CompareAndSwap(false, true) {
				lastProbe = now
				go func() {
					defer probing.Store(false)
					m.probeOnce(ctx)
				}()
			}
		}
	}
}

// probeOnce 主动探一次上游 ttyd：用 httptrace 拆出 TCP 建连与首字节两段耗时。
// 前者是纯网络往返，后者含上游应用处理——两者分开才知道慢在网络还是慢在对端。
func (m *netMonitor) probeOnce(ctx context.Context) {
	if m.target == nil {
		return
	}
	reqCtx, cancel := context.WithTimeout(ctx, netmonProbeTimeout)
	defer cancel()

	var connectStart, connectDone, firstByte time.Time
	trace := &httptrace.ClientTrace{
		ConnectStart:         func(string, string) { connectStart = time.Now() },
		ConnectDone:          func(string, string, error) { connectDone = time.Now() },
		GotFirstResponseByte: func() { firstByte = time.Now() },
	}
	req, err := http.NewRequestWithContext(httptrace.WithClientTrace(reqCtx, trace), http.MethodGet, m.target.String(), nil)
	if err != nil {
		m.recordProbe(0, err)
		return
	}
	if m.probeUser != "" {
		req.Header.Set(authHeaderName, m.probeUser)
	}
	started := time.Now()
	resp, err := m.probeClient.Do(req)
	if err != nil {
		m.recordProbe(0, err)
		return
	}
	// 上游首页很小，读完丢掉即可；不读干净会让连接无法正常收尾。
	_, _ = readAndDiscard(resp)

	if !connectStart.IsZero() && connectDone.After(connectStart) {
		m.probeTCP.addDuration(connectDone.Sub(connectStart))
	}
	if !firstByte.IsZero() {
		m.probeTTFB.addDuration(firstByte.Sub(started))
	}
	m.recordProbe(resp.StatusCode, nil)
}

func readAndDiscard(resp *http.Response) (n int64, err error) {
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("关闭探测响应正文: %w", closeErr))
		}
	}()
	return io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
}

func (m *netMonitor) recordProbe(status int, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.probeLastAt = time.Now()
	m.probeStatus = status
	if err != nil {
		m.probeFail++
		m.probeStreak++
		m.probeLastErr = err.Error()
		return
	}
	m.probeOK++
	m.probeStreak = 0
	m.probeLastErr = ""
}

// putClientSample 收下浏览器上报，按页面去重保存；超出上限先清过期再淘汰最旧的。
func (m *netMonitor) putClientSample(sample netmonClientSample) { m.putClientSampleFor(sample, "") }

func (m *netMonitor) putClientSampleFor(sample netmonClientSample, owner string) {
	if sample.ClientID == "" {
		return
	}
	if len(sample.Tabs) > maxNetmonClientTabs {
		sample.Tabs = sample.Tabs[:maxNetmonClientTabs]
	}
	now := time.Now()
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, c := range m.clients {
		if now.Sub(c.at) > netmonClientTTL {
			delete(m.clients, id)
		}
	}
	m.clients[owner+":"+sample.ClientID] = &netmonClientRecord{sample: sample, at: now, owner: owner}
	for len(m.clients) > maxNetmonClients {
		oldestID := ""
		var oldest time.Time
		for id, c := range m.clients {
			if oldestID == "" || c.at.Before(oldest) {
				oldestID, oldest = id, c.at
			}
		}
		delete(m.clients, oldestID)
	}
}

// ---------- 快照 ----------

type netmonGate struct {
	UptimeSec  int64   `json:"uptimeSec"`
	Goroutines int     `json:"goroutines"`
	HeapMB     float64 `json:"heapMB"`
	ActiveTabs int     `json:"activeTabs"`
	Version    string  `json:"version"`
}

type netmonThroughput struct {
	UpBps1s    float64 `json:"upBps1s"`
	DownBps1s  float64 `json:"downBps1s"`
	UpBps10s   float64 `json:"upBps10s"`
	DownBps10s float64 `json:"downBps10s"`
	UpBps60s   float64 `json:"upBps60s"`
	DownBps60s float64 `json:"downBps60s"`
	UpBytes    int64   `json:"upBytesTotal"`
	DownBytes  int64   `json:"downBytesTotal"`
}

type netmonProbe struct {
	IntervalSec int      `json:"intervalSec"`
	TCP         rttStats `json:"tcp"`
	TTFB        rttStats `json:"ttfb"`
	OKCount     int64    `json:"okCount"`
	FailCount   int64    `json:"failCount"`
	FailStreak  int      `json:"failStreak"`
	LastStatus  int      `json:"lastStatus"`
	LastError   string   `json:"lastError"`
	LastAgeMs   int64    `json:"lastAgeMs"`
}

type netmonConns struct {
	Active     int64 `json:"active"`
	Opened     int64 `json:"opened"`
	Closed     int64 `json:"closed"`
	DialFails  int64 `json:"dialFails"`
	ProxyFails int64 `json:"proxyFails"`
}

type netmonUpstream struct {
	Target     string           `json:"target"`
	Handshake  rttStats         `json:"handshake"`
	Probe      netmonProbe      `json:"probe"`
	Conns      netmonConns      `json:"conns"`
	Throughput netmonThroughput `json:"throughput"`
}

type netmonSnapshot struct {
	Gate     netmonGate         `json:"gate"`
	Upstream netmonUpstream     `json:"upstream"`
	Clients  []netmonClientView `json:"clients"`
}

// upstreamSnapshot 返回单台上游机器的监控快照（不含门禁自身与浏览器观测，
// 多机器时由服务端把多份拼起来，见 server 的 handleNetmon）。
func (m *netMonitor) upstreamSnapshot() netmonUpstream {
	up1, down1 := m.rates.rate(time.Second)
	up10, down10 := m.rates.rate(10 * time.Second)
	up60, down60 := m.rates.rate(60 * time.Second)

	target := ""
	if m.target != nil {
		target = m.target.Host
	}

	m.mu.Lock()
	probe := netmonProbe{
		IntervalSec: int(m.interval / time.Second),
		OKCount:     m.probeOK,
		FailCount:   m.probeFail,
		FailStreak:  m.probeStreak,
		LastStatus:  m.probeStatus,
		LastError:   m.probeLastErr,
	}
	m.mu.Unlock()
	probe.TCP = m.probeTCP.stats()
	probe.TTFB = m.probeTTFB.stats()

	return netmonUpstream{
		Target:    target,
		Handshake: m.dialRTT.stats(),
		Probe:     probe,
		Conns: netmonConns{
			Active:     m.connActive.Load(),
			Opened:     m.connOpened.Load(),
			Closed:     m.connClosed.Load(),
			DialFails:  m.dialFails.Load(),
			ProxyFails: m.proxyFails.Load(),
		},
		Throughput: netmonThroughput{
			UpBps1s: up1, DownBps1s: down1,
			UpBps10s: up10, DownBps10s: down10,
			UpBps60s: up60, DownBps60s: down60,
			UpBytes:   m.upBytes.Load(),
			DownBytes: m.downBytes.Load(),
		},
	}
}

// healthy 判断这台上游机器是否健康：从未探测（未开主动探测）时不知道好坏，
// 按健康处理；开探测后连续失败 3 次才算不可用，避免单次抖动误报。
func (m *netMonitor) healthy() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.probeLastAt.IsZero() {
		return true
	}
	return m.probeStreak < 3
}

// failStreak 返回连续探测失败次数（机器列表接口用）。
func (m *netMonitor) failStreak() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.probeStreak
}

func (m *netMonitor) snapshot(activeTabs int) netmonSnapshot { return m.snapshotFor(activeTabs, "") }

func (m *netMonitor) snapshotFor(activeTabs int, owner string) netmonSnapshot {
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)

	m.mu.Lock()
	probeLastAt := m.probeLastAt
	now := time.Now()
	clients := make([]netmonClientView, 0, len(m.clients))
	for id, c := range m.clients {
		age := now.Sub(c.at)
		if age > netmonClientTTL {
			delete(m.clients, id)
			continue
		}
		if c.owner != owner {
			continue
		}
		clients = append(clients, netmonClientView{netmonClientSample: c.sample, AgeMs: age.Milliseconds()})
	}
	m.mu.Unlock()

	sort.Slice(clients, func(i, j int) bool { return clients[i].AgeMs < clients[j].AgeMs })

	up := m.upstreamSnapshot()
	if !probeLastAt.IsZero() {
		up.Probe.LastAgeMs = time.Since(probeLastAt).Milliseconds()
	} else {
		up.Probe.LastAgeMs = -1
	}

	return netmonSnapshot{
		Gate: netmonGate{
			UptimeSec:  int64(time.Since(m.start).Seconds()),
			Goroutines: runtime.NumGoroutine(),
			HeapMB:     round1(float64(mem.HeapAlloc) / (1024 * 1024)),
			ActiveTabs: activeTabs,
		},
		Upstream: up,
		Clients:  clients,
	}
}

// closeConnections also closes upgraded WebSockets, which http.Server.Shutdown
// deliberately does not own. Close outside the monitor lock.
func (m *netMonitor) closeConnections() {
	m.mu.Lock()
	m.shuttingDown = true
	connections := make([]*countingConn, 0, len(m.connections))
	for c := range m.connections {
		connections = append(connections, c)
	}
	m.mu.Unlock()
	for _, c := range connections {
		_ = c.Close()
	}
}
