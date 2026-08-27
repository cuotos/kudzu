// Package httpapi exposes the Kudzu gate service over HTTP.
package httpapi

import (
	"log/slog"
	"net/http"
	"time"
)

// Options configures the HTTP router.
type Options struct {
	Service         GateService
	Metrics         observer
	MetricsHandler  http.Handler
	WriteTokens     []string
	RequireReadAuth bool
	UIWrites        bool
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
	srv.uiWrites = opts.UIWrites
	auth := newTokenAuth(opts.WriteTokens)

	mux := http.NewServeMux()

	// read returns a read handler, gated by auth only if RequireReadAuth.
	read := func(h http.HandlerFunc) http.HandlerFunc {
		if opts.RequireReadAuth {
			return auth.require(h)
		}
		return h
	}

	for _, rt := range routes {
		h := rt.Handler(srv)
		switch rt.Auth {
		case authWrite:
			h = auth.require(h)
		case authRead:
			h = read(h)
		}
		mux.HandleFunc(rt.Method+" "+rt.Pattern, instrument(rt.label(), log, opts.Metrics, h))
	}

	// Metrics is the one route whose handler comes from the caller rather than
	// the server, so it sits outside the table.
	if opts.MetricsHandler != nil {
		mux.Handle("GET /metrics", opts.MetricsHandler)
	}

	return mux
}

// DefaultReadTimeout is a sane default for server timeouts.
const DefaultReadTimeout = 10 * time.Second
