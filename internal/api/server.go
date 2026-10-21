// Package api serves the status of every instance as JSON, a small UI on
// top, and Prometheus metrics.
package api

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.uber.org/zap"

	"github.com/ut0mt8/mig-scaleset/internal/scaler"
)

//go:embed ui
var uiFiles embed.FS

type Server struct {
	version   string
	startedAt time.Time
	log       *zap.Logger
	http      *http.Server

	// Fixed at startup, in config order.
	instances []*Instance
	byName    map[string]*Instance
}

func New(addr, version string, log *zap.Logger) *Server {
	s := &Server{
		version:   version,
		startedAt: time.Now(),
		log:       log,
		instances: []*Instance{},
		byName:    map[string]*Instance{},
	}

	ui, err := fs.Sub(uiFiles, "ui")
	if err != nil {
		panic(err) // the embed path is fixed at build time
	}

	registry := prometheus.NewRegistry()
	registry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		&collector{server: s},
	)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("GET /api/v1/instances", s.listInstances)
	mux.HandleFunc("GET /api/v1/instances/{name}/status", s.status)
	mux.HandleFunc("GET /api/v1/instances/{name}/runners", s.runners)
	mux.HandleFunc("GET /api/v1/instances/{name}/events", s.events)
	mux.Handle("GET /metrics", promhttp.HandlerFor(registry, promhttp.HandlerOpts{}))
	mux.Handle("GET /", http.FileServerFS(ui))

	s.http = &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	return s
}

// Add registers an instance. Call it for every instance before Run.
func (s *Server) Add(info InstanceInfo) *Instance {
	inst := &Instance{info: info, state: stateStarting, since: time.Now()}
	s.instances = append(s.instances, inst)
	s.byName[info.Name] = inst
	return inst
}

// Run serves until ctx is done.
func (s *Server) Run(ctx context.Context) error {
	errc := make(chan error, 1)
	go func() {
		s.log.Info("api listening", zap.String("addr", s.http.Addr))
		errc <- s.http.ListenAndServe()
	}()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()

	err := s.http.Shutdown(shutdownCtx)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

type instancesResponse struct {
	Version   string            `json:"version"`
	StartedAt time.Time         `json:"started_at"`
	Instances []instanceSummary `json:"instances"`
}

func (s *Server) listInstances(w http.ResponseWriter, _ *http.Request) {
	summaries := make([]instanceSummary, 0, len(s.instances))
	for _, inst := range s.instances {
		summaries = append(summaries, inst.summary())
	}

	writeJSON(w, http.StatusOK, instancesResponse{
		Version:   s.version,
		StartedAt: s.startedAt,
		Instances: summaries,
	})
}

type statusResponse struct {
	Instance instanceSummary `json:"instance"`
	Now      time.Time       `json:"now"`
	scaler.Status
}

func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	inst, sc, ok := s.lookup(w, r)
	if !ok {
		return
	}

	writeJSON(w, http.StatusOK, statusResponse{
		Instance: inst.summary(),
		Now:      time.Now(),
		Status:   sc.Status(),
	})
}

func (s *Server) runners(w http.ResponseWriter, r *http.Request) {
	_, sc, ok := s.lookup(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, sc.Runners())
}

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	_, sc, ok := s.lookup(w, r)
	if !ok {
		return
	}

	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 {
			writeError(w, http.StatusBadRequest, "limit must be a positive integer")
			return
		}
		limit = parsed
	}

	writeJSON(w, http.StatusOK, sc.Events(limit))
}

// lookup finds the instance in the path and its scaler. It answers 404 for
// an unknown instance and 503 while the scaler has never started.
func (s *Server) lookup(w http.ResponseWriter, r *http.Request) (*Instance, *scaler.Scaler, bool) {
	name := r.PathValue("name")

	inst, ok := s.byName[name]
	if !ok {
		writeError(w, http.StatusNotFound, "unknown instance "+strconv.Quote(name))
		return nil, nil, false
	}

	sc := inst.scaler.Load()
	if sc == nil {
		state := inst.summary().State
		writeError(w, http.StatusServiceUnavailable, "instance "+strconv.Quote(name)+" is "+state)
		return nil, nil, false
	}
	return inst, sc, true
}

func writeError(w http.ResponseWriter, code int, message string) {
	writeJSON(w, code, map[string]string{"error": message})
}

func writeJSON(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(body) //nolint:errcheck // client went away
}
