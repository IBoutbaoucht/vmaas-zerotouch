package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/cuneyt/vmaas-engine/internal/lifecycle"
	"github.com/cuneyt/vmaas-engine/internal/store"
)

// createReq is the JSON body for POST /v1/vms.
type createReq struct {
	Name string `json:"name,omitempty"`
}

// vmDTO is the shape returned to clients (avoid leaking internal fields).
type vmDTO struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Status    string    `json:"status"`
	IP        string    `json:"ip,omitempty"`
	GoldVM    string    `json:"gold_vm,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Error     string    `json:"error,omitempty"`
}

func toDTO(v store.VM) vmDTO {
	return vmDTO{
		ID: v.ID, Name: v.Name, Status: string(v.Status), IP: v.IP,
		GoldVM: v.GoldVM, CreatedAt: v.CreatedAt, UpdatedAt: v.UpdatedAt, Error: v.Error,
	}
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	// A trivial ESXi RPC: read AboutInfo (already cached, but if the API client
	// is broken this will surface).
	about := s.ex.About(r.Context())
	if about.Name == "" {
		http.Error(w, "esxi unreachable", http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"esxi":    about.Name,
		"version": about.Version,
		"build":   about.Build,
	})
}

func (s *Server) handleListVMs(w http.ResponseWriter, r *http.Request) {
	vms, err := s.orch.List()
	if err != nil {
		httpErr(w, err, http.StatusInternalServerError)
		return
	}
	out := make([]vmDTO, 0, len(vms))
	for _, v := range vms {
		out = append(out, toDTO(v))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleCreateVM(w http.ResponseWriter, r *http.Request) {
	var req createReq
	if r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpErr(w, err, http.StatusBadRequest)
			return
		}
	}
	id, err := s.orch.Provision(lifecycle.CreateRequest{Name: req.Name})
	if err != nil {
		httpErr(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"id":     id,
		"status": "pending",
	})
}

func (s *Server) handleGetVM(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	v, err := s.orch.Get(id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		httpErr(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, toDTO(v))
}

func (s *Server) handleDeleteVM(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := s.orch.Delete(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		httpErr(w, err, http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handlePool(w http.ResponseWriter, r *http.Request) {
	used, err := s.alloc.Used()
	if err != nil {
		httpErr(w, err, http.StatusInternalServerError)
		return
	}
	free, err := s.alloc.Free()
	if err != nil {
		httpErr(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"total": s.alloc.Total(),
		"used":  used, // map ip -> vmID
		"free":  free,
	})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func httpErr(w http.ResponseWriter, err error, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}
