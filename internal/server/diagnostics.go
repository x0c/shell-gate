package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	debugLogFileName = "debug-log.ndjson"
	perfLogFileName  = "perf-log.ndjson"
	logRotateMaxSize = 5 << 20 // 5 MiB，超出后 rename 为 *.1 再新建
)

// handleDebugLog 接收终端增强脚本上报的诊断埋点（fire-and-forget），登录态保护。
// 功能类事件写 debug-log.ndjson；perf-sample 写 perf-log.ndjson，避免卡顿采样淹没
// 剪贴板/标点类取证。前端约定不上报剪贴板实际内容，只报事件元信息。
func (s *Server) handleDebugLog(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.DebugLogs {
		writeJSON(w, http.StatusOK, map[string]string{"status": "disabled"})
		return
	}
	id, ok := s.requireIdentity(w, r)
	if !ok {
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 8<<10)) // 单条最多 8KB，防止滥用写爆磁盘
	_ = r.Body.Close()
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"message": "Could not read request"})
		return
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"message": "Invalid JSON request"})
		return
	}
	payload["ts"] = time.Now().UTC().Format(time.RFC3339Nano)
	payload["email"] = id.Email
	line, err := json.Marshal(payload)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"message": "Could not encode diagnostics"})
		return
	}
	name := debugLogFileName
	// 采样类事件（卡顿、网络）单独落一份，避免淹没剪贴板 / 标点类取证。
	switch event, _ := payload["event"].(string); event {
	case "perf-sample", "netmon-sample":
		name = perfLogFileName
	}
	if err := appendNdjsonLogLine(s.cfg.DataDir, name, line); err != nil {
		log.Printf("写调试日志失败: %v", err)
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func appendNdjsonLogLine(dataDir, name string, line []byte) (err error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return err
	}
	path := filepath.Join(dataDir, name)
	if err := rotateNdjsonLogIfNeeded(path, logRotateMaxSize); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := f.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("关闭日志文件: %w", closeErr))
		}
	}()
	_, err = f.Write(append(line, '\n'))
	return err
}

// rotateNdjsonLogIfNeeded 在文件已达 maxSize 时把它挪成 path+".1"（覆盖旧备份），
// 下次 OpenFile 会新建空文件继续写。
func rotateNdjsonLogIfNeeded(path string, maxSize int64) error {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if info.Size() < maxSize {
		return nil
	}
	backup := path + ".1"
	_ = os.Remove(backup)
	return os.Rename(path, backup)
}

// netmonUpstreamView 是多机器快照里的一台上游机器（带上机器名，顺序即配置顺序）。
type netmonUpstreamView struct {
	Name string `json:"name"`
	netmonUpstream
}

// netmonMultiSnapshot 是多机器版本的链路快照：上行段按机器拆分，门禁自身指标与
// 浏览器观测（下行段 / 端到端）与机器无关，仍是全局一份。单机部署下 upstreams 只有
// 一项，前端按同一结构渲染。
type netmonMultiSnapshot struct {
	Gate      netmonGate           `json:"gate"`
	Upstreams []netmonUpstreamView `json:"upstreams"`
	Clients   []netmonClientView   `json:"clients"`
}

// handleNetmon 返回门禁侧的链路快照：上行段（门禁 ↔ 各终端主机）的时延 / 吞吐 / 连接健康，
// 外加浏览器最近推上来的下行段与端到端观测。面板把两边拼起来展示。
func (s *Server) handleNetmon(w http.ResponseWriter, r *http.Request) {
	id, ok := s.requireIdentity(w, r)
	if !ok {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	primary := s.primaryUpstream()
	if primary == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"message": "No terminal machine is configured"})
		return
	}
	// 门禁自身指标与浏览器上报挂在默认机器的监控器上（与具体机器无关，只是存取容器）。
	count, err := s.tabs.CountFor(id.Email)
	if err != nil {
		writeJSON(w, 503, map[string]string{"message": "Workspace storage unavailable"})
		return
	}
	base := primary.netmon.snapshotFor(count, id.Email)
	multi := netmonMultiSnapshot{Gate: base.Gate, Clients: base.Clients}
	for _, u := range s.upstreams {
		multi.Upstreams = append(multi.Upstreams, netmonUpstreamView{Name: u.name, netmonUpstream: u.netmon.upstreamSnapshot()})
	}
	writeJSON(w, http.StatusOK, multi)
}

// handleNetmonPing 是下行段（浏览器 ↔ 门禁）的计时靶子：浏览器自己前后取两次时间差算往返。
// 服务端刻意不参与计时——两端时钟不同步，只有同一个时钟上的往返差值才准。
func (s *Server) handleNetmonPing(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireIdentity(w, r); !ok {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]int64{"serverTimeMs": time.Now().UnixMilli()})
}

// handleNetmonSample 收下浏览器测到的下行段与端到端数据，汇总进快照，便于事后排查。
func (s *Server) handleNetmonSample(w http.ResponseWriter, r *http.Request) {
	id, ok := s.requireIdentity(w, r)
	if !ok {
		return
	}
	var sample netmonClientSample
	if err := readJSON(r, &sample); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"message": "Invalid request"})
		return
	}
	if strings.TrimSpace(sample.ClientID) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"message": "Missing clientId"})
		return
	}
	if primary := s.primaryUpstream(); primary != nil {
		primary.netmon.putClientSampleFor(sample, id.Email)
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
