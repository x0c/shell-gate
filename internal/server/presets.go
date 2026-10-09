package server

import (
	"errors"
	"github.com/x0c/shell-gate/internal/server/store"
	"net/http"
)

func (s *Server) handleListPresets(w http.ResponseWriter, r *http.Request) {
	id, ok := s.requireIdentity(w, r)
	if !ok {
		return
	}
	list, err := s.store.List(id.Email)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"message": "Could not load presets"})
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
		writeJSON(w, http.StatusBadRequest, map[string]string{"message": "Invalid request"})
		return
	}
	if body.Machine != "" && !s.cfg.machineExists(body.Machine) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"message": "Machine not found: " + body.Machine})
		return
	}
	p, err := s.store.Create(id.Email, body.Name, body.Command, body.Machine, body.SortOrder)
	if err != nil {
		if errors.Is(err, store.ErrInvalidPreset) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"message": "Name and command are required"})
		} else {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"message": "Could not save preset. Please try again."})
		}
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
		writeJSON(w, http.StatusBadRequest, map[string]string{"message": "Invalid request"})
		return
	}
	if body.Machine != "" && !s.cfg.machineExists(body.Machine) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"message": "Machine not found: " + body.Machine})
		return
	}
	p, err := s.store.Update(id.Email, presetID, body.Name, body.Command, body.Machine, body.SortOrder)
	if err != nil {
		if errors.Is(err, store.ErrInvalidPreset) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"message": "Name and command are required"})
		} else {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"message": "Could not save preset. Please try again."})
		}
		return
	}
	if p == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "Preset not found"})
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
		writeJSON(w, http.StatusInternalServerError, map[string]string{"message": "Could not delete preset"})
		return
	}
	if !okDel {
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "Preset not found"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
