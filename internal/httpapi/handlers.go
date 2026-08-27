package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/cuotos/kudzu/internal/gate"
	"github.com/cuotos/kudzu/internal/schedule"
)

// GateService is the behaviour the HTTP layer needs from gate.Service.
type GateService interface {
	Get(ctx context.Context, k gate.Key) (gate.Gate, error)
	List(ctx context.Context) ([]gate.Gate, error)
	Freeze(ctx context.Context, k gate.Key, reason, actor string, ttl time.Duration) (gate.Gate, error)
	Unfreeze(ctx context.Context, k gate.Key, actor string) (gate.Gate, error)
	RecordDeploy(ctx context.Context, r gate.DeployResult) (gate.Gate, error)
	AddSchedule(ctx context.Context, k gate.Key, s schedule.Schedule) error
	ListSchedules(ctx context.Context, k gate.Key) ([]gate.ScheduleEntry, error)
	ListAllSchedules(ctx context.Context) ([]gate.ScheduleEntry, error)
	DeleteSchedule(ctx context.Context, k gate.Key, id string) error
	Ping(ctx context.Context) error
}

// Server wires the gate service to HTTP handlers.
type Server struct {
	svc GateService
	log *slog.Logger
	// uiWrites reveals the gate board's write controls. It gates the rendered
	// page only; the write endpoints are authorised the same way regardless.
	uiWrites bool
}

func newServer(svc GateService, log *slog.Logger) *Server {
	if log == nil {
		log = slog.Default()
	}
	return &Server{svc: svc, log: log}
}

// --- read handlers ---

func (s *Server) handleGetGate(w http.ResponseWriter, r *http.Request) {
	k := gate.Key{Service: r.URL.Query().Get("service"), Env: r.URL.Query().Get("env")}
	g, err := s.svc.Get(r.Context(), k)
	if err != nil {
		s.writeServiceErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, g)
}

func (s *Server) handleListGates(w http.ResponseWriter, r *http.Request) {
	gates, err := s.svc.List(r.Context())
	if err != nil {
		s.writeServiceErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"gates": gates})
}

// --- write handlers ---

type freezeReq struct {
	Service    string `json:"service"`
	Env        string `json:"env"`
	Reason     string `json:"reason"`
	Actor      string `json:"actor"`
	TTLSeconds int    `json:"ttl_seconds"`
}

func (s *Server) handleFreeze(w http.ResponseWriter, r *http.Request) {
	var req freezeReq
	if !decode(w, r, &req) {
		return
	}
	g, err := s.svc.Freeze(r.Context(), gate.Key{Service: req.Service, Env: req.Env},
		req.Reason, req.Actor, time.Duration(req.TTLSeconds)*time.Second)
	if err != nil {
		s.writeServiceErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, g)
}

type unfreezeReq struct {
	Service string `json:"service"`
	Env     string `json:"env"`
	Actor   string `json:"actor"`
}

func (s *Server) handleUnfreeze(w http.ResponseWriter, r *http.Request) {
	var req unfreezeReq
	if !decode(w, r, &req) {
		return
	}
	g, err := s.svc.Unfreeze(r.Context(), gate.Key{Service: req.Service, Env: req.Env}, req.Actor)
	if err != nil {
		s.writeServiceErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, g)
}

func (s *Server) handleDeployResult(w http.ResponseWriter, r *http.Request) {
	var req gate.DeployResult
	if !decode(w, r, &req) {
		return
	}
	g, err := s.svc.RecordDeploy(r.Context(), req)
	if err != nil {
		s.writeServiceErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, g)
}

type scheduleReq struct {
	Service         string     `json:"service"`
	Env             string     `json:"env"`
	ID              string     `json:"id"`
	Reason          string     `json:"reason"`
	Cron            string     `json:"cron"`
	DurationSeconds int        `json:"duration_seconds"`
	Start           *time.Time `json:"start"`
	End             *time.Time `json:"end"`
}

func (s *Server) handleAddSchedule(w http.ResponseWriter, r *http.Request) {
	var req scheduleReq
	if !decode(w, r, &req) {
		return
	}
	sc := schedule.Schedule{
		ID:       req.ID,
		Reason:   req.Reason,
		Cron:     req.Cron,
		Duration: time.Duration(req.DurationSeconds) * time.Second,
		Start:    req.Start,
		End:      req.End,
	}
	if sc.ID == "" {
		sc.ID = randID()
	}
	if err := s.svc.AddSchedule(r.Context(), gate.Key{Service: req.Service, Env: req.Env}, sc); err != nil {
		s.writeServiceErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, sc)
}

// handleListSchedules lists freeze windows for one gate, or — with no service
// and no env — every window Kudzu knows about. Each entry says whether it is in
// force right now.
func (s *Server) handleListSchedules(w http.ResponseWriter, r *http.Request) {
	k := gate.Key{Service: r.URL.Query().Get("service"), Env: r.URL.Query().Get("env")}

	var (
		entries []gate.ScheduleEntry
		err     error
	)
	if k.Service == "" && k.Env == "" {
		entries, err = s.svc.ListAllSchedules(r.Context())
	} else {
		// A half-specified key is a mistake, not a request for everything, so
		// this path still rejects it via ErrInvalidKey.
		entries, err = s.svc.ListSchedules(r.Context(), k)
	}
	if err != nil {
		s.writeServiceErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"schedules": entries})
}

func (s *Server) handleDeleteSchedule(w http.ResponseWriter, r *http.Request) {
	k := gate.Key{Service: r.URL.Query().Get("service"), Env: r.URL.Query().Get("env")}
	id := r.PathValue("id")
	if err := s.svc.DeleteSchedule(r.Context(), k, id); err != nil {
		s.writeServiceErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- health ---

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleReadyz(w http.ResponseWriter, r *http.Request) {
	if err := s.svc.Ping(r.Context()); err != nil {
		writeError(w, http.StatusServiceUnavailable, "store unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

// --- helpers ---

func (s *Server) writeServiceErr(w http.ResponseWriter, err error) {
	if errors.Is(err, gate.ErrInvalidKey) {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.log.Error("request failed", "err", err)
	writeError(w, http.StatusInternalServerError, "internal error")
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func randID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
