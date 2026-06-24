// Package httpapi exposes the Kudzu gate service over HTTP.
package httpapi

import (
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// Options configures the HTTP router.
type Options struct {
	Service         GateService
	Metrics         observer
	MetricsHandler  http.Handler
	WriteTokens     []string
	RequireReadAuth bool
	Log             *slog.Logger
}

// NewRouter builds the fully wired HTTP handler: routes, auth on writes,
// and per-route logging/metrics/recovery middleware.
func NewRouter(opts Options) http.Handler {
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}
	srv := newServer(opts.Service, log)
	auth := newTokenAuth(opts.WriteTokens)

	mux := http.NewServeMux()

	// read returns a read handler, gated by auth only if RequireReadAuth.
	read := func(h http.HandlerFunc) http.HandlerFunc {
		if opts.RequireReadAuth {
			return auth.require(h)
		}
		return h
	}

	register := func(pattern string, h http.HandlerFunc) {
		// The route label for metrics is the pattern minus the method.
		route := pattern
		if _, after, ok := strings.Cut(pattern, " "); ok {
			route = after
		}
		mux.HandleFunc(pattern, instrument(route, log, opts.Metrics, h))
	}

	// Reads.
	register("GET /v1/gate", read(srv.handleGetGate))
	register("GET /v1/gates", read(srv.handleListGates))
	register("GET /v1/schedules", read(srv.handleListSchedules))

	// Writes (always authenticated).
	register("POST /v1/gate/freeze", auth.require(srv.handleFreeze))
	register("POST /v1/gate/unfreeze", auth.require(srv.handleUnfreeze))
	register("POST /v1/deploy-result", auth.require(srv.handleDeployResult))
	register("POST /v1/schedules", auth.require(srv.handleAddSchedule))
	register("DELETE /v1/schedules/{id}", auth.require(srv.handleDeleteSchedule))

	// Operational endpoints (unauthenticated).
	register("GET /healthz", srv.handleHealthz)
	register("GET /readyz", srv.handleReadyz)
	if opts.MetricsHandler != nil {
		mux.Handle("GET /metrics", opts.MetricsHandler)
	}

	return mux
}

// DefaultReadTimeout is a sane default for server timeouts.
const DefaultReadTimeout = 10 * time.Second
