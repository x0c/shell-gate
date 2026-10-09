package server

import (
	"context"
	"io"
	"log"
	"net/http"
	"strings"
)

type createTabBody struct {
	PresetID string `json:"presetId"`
	Title    string `json:"title"`
	Command  string `json:"command"`
	// Machine 是目标机器名；缺省 = 默认机器。预置绑定了机器时以预置为准。
	Machine string `json:"machine"`
}

func (s *Server) handleCreateTab(w http.ResponseWriter, r *http.Request) {
	id, ok := s.requireIdentity(w, r)
	if !ok {
		return
	}
	var body createTabBody
	if err := readJSON(r, &body); err != nil && err != io.EOF {
		writeJSON(w, http.StatusBadRequest, map[string]string{"message": "请求体无效"})
		return
	}
	title := strings.TrimSpace(body.Title)
	command := strings.TrimSpace(body.Command)
	// 机器优先级：预置绑定 > 请求指定 > 默认机器。
	machine := strings.TrimSpace(body.Machine)
	if machine == "" {
		machine = s.cfg.DefaultMachineName()
	}
	if body.PresetID != "" {
		p, err := s.store.Get(id.Email, body.PresetID)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"message": "读取预置失败"})
			return
		}
		if p == nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"message": "预置不存在"})
			return
		}
		command = p.Command
		if title == "" {
			title = p.Name
		}
		if p.Machine != "" {
			machine = p.Machine
		}
	}
	if !s.cfg.machineExists(machine) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"message": "机器不存在：" + machine})
		return
	}
	u := s.upstreamByName[machine]
	if _, err := u.ensureReady(r.Context()); err != nil {
		log.Printf("打开 Tab 前自动唤醒上游失败 machine=%s: %v", machine, err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"message": wakeFailureMessage(machine, err),
		})
		return
	}
	if title == "" {
		if command == "" {
			title = "shell"
		} else {
			title = command
		}
	}
	tab, err := s.tabs.Create(id.Email, machine, title, command)
	if err != nil {
		log.Printf("持久化 Tab 失败 email=%s machine=%s: %v", id.Email, machine, err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"message": "创建 Tab 失败"})
		return
	}
	if u.leaser != nil {
		if err := u.leaser.acquire(r.Context(), tab.ID); err != nil {
			if _, cleanupErr := s.tabs.Delete(id.Email, tab.ID); cleanupErr != nil {
				log.Printf("rollback tab: %v", cleanupErr)
			}
			log.Printf("创建 Mac Tab 的睡眠租约失败 machine=%s tab=%s: %v", machine, tab.ID, err)
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{
				"message": machine + " 未能保持终端会话；请稍后重试",
			})
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":      tab.ID,
		"title":   tab.Title,
		"command": tab.Command,
		"machine": tab.Machine,
		"path":    "/t/" + tab.ID + "/",
	})
}

func (s *Server) handleListTabs(w http.ResponseWriter, r *http.Request) {
	id, ok := s.requireIdentity(w, r)
	if !ok {
		return
	}
	tabs, err := s.tabs.ListFor(id.Email)
	if err != nil {
		writeJSON(w, 503, map[string]string{"message": "Could not restore tabs; try again"})
		return
	}
	items := make([]map[string]string, 0, len(tabs))
	for _, tab := range tabs {
		items = append(items, map[string]string{
			"id":      tab.ID,
			"title":   tab.Title,
			"command": tab.Command,
			"machine": tab.Machine,
			"path":    "/t/" + tab.ID + "/",
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"tabs": items})
}

func (s *Server) handleDeleteTab(w http.ResponseWriter, r *http.Request) {
	id, ok := s.requireIdentity(w, r)
	if !ok {
		return
	}
	tabID := r.PathValue("id")
	tab, exists := s.tabs.Get(tabID)
	if !exists || !strings.EqualFold(tab.Email, id.Email) {
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "Tab 不存在"})
		return
	}
	deleted, err := s.tabs.Delete(id.Email, tabID)
	if err != nil {
		writeJSON(w, 503, map[string]string{"message": "Could not close tab; try again"})
		return
	}
	if !deleted {
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "Tab 不存在"})
		return
	}
	if u := s.upstreamByName[tab.Machine]; u != nil && u.leaser != nil {
		if err := u.leaser.release(r.Context(), tab.ID); err != nil {
			// Tab 已明确关闭，不能因远端暂不可达把它留在工作台；远端租约有硬超时兜底。
			log.Printf("释放 Mac Tab 睡眠租约失败 machine=%s tab=%s: %v", tab.Machine, tab.ID, err)
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleTermProxy(w http.ResponseWriter, r *http.Request, email string) {
	rest := strings.TrimPrefix(r.URL.Path, "/t/")
	if rest == "" {
		http.NotFound(w, r)
		return
	}
	tabID, suffix, _ := strings.Cut(rest, "/")
	if tabID == "" {
		http.NotFound(w, r)
		return
	}
	tab, ok := s.tabs.Get(tabID)
	if !ok || !strings.EqualFold(tab.Email, email) {
		http.NotFound(w, r)
		return
	}
	// 无尾部斜杠时补上，保证 ttyd 前端用 pathname 拼出正确的 /t/{id}/ws
	if suffix == "" && !strings.HasSuffix(r.URL.Path, "/") && r.Method == http.MethodGet &&
		!strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		http.Redirect(w, r, "/t/"+tabID+"/", http.StatusFound)
		return
	}
	outPath := "/" + suffix
	if outPath != "/" && strings.HasSuffix(r.URL.Path, "/") && !strings.HasSuffix(outPath, "/") {
		outPath += "/"
	}
	u, ok := s.upstreamByName[tab.Machine]
	if !ok {
		// 机器已从配置里移除（需要重启才发生，此时内存 Tab 本就清空，理论上到不了这里）。
		http.Error(w, "终端上游不可用", http.StatusBadGateway)
		return
	}
	ctx := context.WithValue(r.Context(), proxyUserKey{}, email)
	ctx = context.WithValue(ctx, proxyCmdKey{}, tab.Command)
	rr := r.Clone(ctx)
	rr.URL.Path = outPath
	rr.URL.RawPath = ""
	u.proxy.ServeHTTP(w, rr)
}
