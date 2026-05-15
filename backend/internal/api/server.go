// Package api wires the HTTP layer: chi router, middlewares, handlers.
//
// Endpoints under /v1:
//   POST   /v1/vms            provision (returns 202 + id)
//   GET    /v1/vms            list all
//   GET    /v1/vms/{id}       one record
//   DELETE /v1/vms/{id}       tear down
//   GET    /v1/pool           pool occupancy summary
//
// Plus:
//   GET    /healthz           liveness + ESXi reachability
//   GET    /metrics           Prometheus
//   GET    /ui/*              embedded static fallback (frontend can also be served by nginx)
//
// All /v1 endpoints require Authorization: Bearer <token> matching cfg.AuthToken.
package api

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/cuneyt/vmaas-engine/internal/config"
	"github.com/cuneyt/vmaas-engine/internal/esxi"
	"github.com/cuneyt/vmaas-engine/internal/ipalloc"
	"github.com/cuneyt/vmaas-engine/internal/lifecycle"
	"github.com/cuneyt/vmaas-engine/internal/store"
)

// Server holds collaborators used by the handlers.
type Server struct {
	cfg   *config.Config
	orch  *lifecycle.Orchestrator
	alloc *ipalloc.Allocator
	st    *store.Store
	ex    *esxi.Client
}

// New constructs the API server.
func New(cfg *config.Config, orch *lifecycle.Orchestrator, alloc *ipalloc.Allocator, st *store.Store, ex *esxi.Client) *Server {
	return &Server{cfg: cfg, orch: orch, alloc: alloc, st: st, ex: ex}
}

// Router builds the chi router with all routes installed.
func (s *Server) Router() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)
	r.Use(loggingMiddleware)
	r.Use(corsMiddleware)
	r.Use(middleware.Timeout(60 * time.Second))

	r.Get("/healthz", s.handleHealth)
	r.Handle("/metrics", promhttp.Handler())

	r.Route("/v1", func(r chi.Router) {
		r.Use(s.authMiddleware)
		r.Get("/vms", s.handleListVMs)
		r.Post("/vms", s.handleCreateVM)
		r.Get("/vms/{id}", s.handleGetVM)
		r.Delete("/vms/{id}", s.handleDeleteVM)
		r.Get("/pool", s.handlePool)
	})

	r.Get("/ui/*", uiHandler())
	r.Get("/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/ui/", http.StatusFound)
	})

	return r
}
