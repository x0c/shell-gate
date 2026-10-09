package server

import "net/http"

func (s *Server) handleListPresets(w http.ResponseWriter, r *http.Request) {
	id, ok := s.requireIdentity(w, r)
	if !ok {
		return
	}
	list, err := s.store.List(id.Email)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"message": "读取预置失败"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"presets": list})
}

type presetBody struct {
	Name    string `json:"name"`
	Command string `json:"command"`
	// Machine 是预置绑定的机器名；空 = 跟随顶栏当前选择的机器。
	Machine   string `json:"machine"`
	SortOrder int    `json:"sortOrder"`
}

func (s *Server) handleCreatePreset(w http.ResponseWriter, r *http.Request) {
	id, ok := s.requireIdentity(w, r)
	if !ok {
		return
	}
	var body presetBody
	if err := readJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"message": "请求体无效"})
		return
	}
	if body.Machine != "" && !s.cfg.machineExists(body.Machine) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"message": "机器不存在：" + body.Machine})
		return
	}
	p, err := s.store.Create(id.Email, body.Name, body.Command, body.Machine, body.SortOrder)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"message": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) handleUpdatePreset(w http.ResponseWriter, r *http.Request) {
	id, ok := s.requireIdentity(w, r)
	if !ok {
		return
	}
	presetID := r.PathValue("id")
	var body presetBody
	if err := readJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"message": "请求体无效"})
		return
	}
	if body.Machine != "" && !s.cfg.machineExists(body.Machine) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"message": "机器不存在：" + body.Machine})
		return
	}
	p, err := s.store.Update(id.Email, presetID, body.Name, body.Command, body.Machine, body.SortOrder)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"message": err.Error()})
		return
	}
	if p == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "预置不存在"})
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) handleDeletePreset(w http.ResponseWriter, r *http.Request) {
	id, ok := s.requireIdentity(w, r)
	if !ok {
		return
	}
	okDel, err := s.store.Delete(id.Email, r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"message": "删除失败"})
		return
	}
	if !okDel {
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "预置不存在"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
