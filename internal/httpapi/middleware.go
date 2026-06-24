package httpapi

import (
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"
)

// statusRecorder captures the response status code for logging/metrics.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// observer records a completed request (implemented by observability.Metrics).
type observer interface {
	Observe(method, route string, status int, d time.Duration)
}

// instrument wraps a handler with panic recovery, structured access logging,
// and metrics. route is the static pattern (not the concrete path) to keep
// metric cardinality bounded.
func instrument(route string, log *slog.Logger, m observer, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

		defer func() {
			if v := recover(); v != nil {
				log.Error("panic recovered", "route", route, "panic", v, "stack", string(debug.Stack()))
				if rec.status == http.StatusOK {
					rec.WriteHeader(http.StatusInternalServerError)
				}
			}
			d := time.Since(start)
			if m != nil {
				m.Observe(r.Method, route, rec.status, d)
			}
			// Health probes are noisy; log them at debug level.
			level := slog.LevelInfo
			if route == "/healthz" || route == "/readyz" {
				level = slog.LevelDebug
			}
			log.Log(r.Context(), level, "request",
				"method", r.Method, "route", route, "status", rec.status, "dur_ms", d.Milliseconds())
		}()

		next(rec, r)
	}
}
