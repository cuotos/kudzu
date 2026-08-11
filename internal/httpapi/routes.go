package httpapi

import "net/http"

// authKind is how a route is protected.
type authKind int

const (
	// authNone is never authenticated — the operational endpoints.
	authNone authKind = iota
	// authRead is authenticated only when RequireReadAuth is set.
	authRead
	// authWrite always requires a bearer token from WriteTokens.
	authWrite
)

// routeDef is one route Kudzu serves.
//
// This table is the single source of truth: NewRouter registers from it, and
// TestOpenAPIDocumentsEveryRoute checks it against the embedded spec, so a new
// endpoint cannot ship undocumented.
type routeDef struct {
	Method  string
	Pattern string   // path pattern as http.ServeMux understands it
	Label   string   // metrics/log label; defaults to Pattern
	Auth    authKind //
	Handler func(*Server) http.HandlerFunc
}

// label is the route label used for metrics and access logs. It is the static
// pattern rather than the concrete path, to keep metric cardinality bounded.
func (r routeDef) label() string {
	if r.Label != "" {
		return r.Label
	}
	return r.Pattern
}

// docPath is the path as written in the OpenAPI spec. ServeMux spells an
// exact-match root as "/{$}"; OpenAPI spells it "/".
func (r routeDef) docPath() string {
	if r.Pattern == "/{$}" {
		return "/"
	}
	return r.Pattern
}

var routes = []routeDef{
	// Human-facing pages and the machine-readable spec, gated like the reads.
	{Method: "GET", Pattern: "/{$}", Label: "/ui", Auth: authRead,
		Handler: func(s *Server) http.HandlerFunc { return s.handleUI }},
	{Method: "GET", Pattern: "/ui", Auth: authRead,
		Handler: func(s *Server) http.HandlerFunc { return s.handleUI }},
	{Method: "GET", Pattern: "/docs", Auth: authRead,
		Handler: func(s *Server) http.HandlerFunc { return s.handleDocs }},
	{Method: "GET", Pattern: "/openapi.json", Auth: authRead,
		Handler: func(s *Server) http.HandlerFunc { return s.handleOpenAPI }},

	// Reads.
	{Method: "GET", Pattern: "/v1/gate", Auth: authRead,
		Handler: func(s *Server) http.HandlerFunc { return s.handleGetGate }},
	{Method: "GET", Pattern: "/v1/gates", Auth: authRead,
		Handler: func(s *Server) http.HandlerFunc { return s.handleListGates }},
	{Method: "GET", Pattern: "/v1/schedules", Auth: authRead,
		Handler: func(s *Server) http.HandlerFunc { return s.handleListSchedules }},

	// Writes.
	{Method: "POST", Pattern: "/v1/gate/freeze", Auth: authWrite,
		Handler: func(s *Server) http.HandlerFunc { return s.handleFreeze }},
	{Method: "POST", Pattern: "/v1/gate/unfreeze", Auth: authWrite,
		Handler: func(s *Server) http.HandlerFunc { return s.handleUnfreeze }},
	{Method: "POST", Pattern: "/v1/deploy-result", Auth: authWrite,
		Handler: func(s *Server) http.HandlerFunc { return s.handleDeployResult }},
	{Method: "POST", Pattern: "/v1/schedules", Auth: authWrite,
		Handler: func(s *Server) http.HandlerFunc { return s.handleAddSchedule }},
	{Method: "DELETE", Pattern: "/v1/schedules/{id}", Auth: authWrite,
		Handler: func(s *Server) http.HandlerFunc { return s.handleDeleteSchedule }},

	// Operational endpoints, never authenticated so probes and scrapers work.
	{Method: "GET", Pattern: "/healthz", Auth: authNone,
		Handler: func(s *Server) http.HandlerFunc { return s.handleHealthz }},
	{Method: "GET", Pattern: "/readyz", Auth: authNone,
		Handler: func(s *Server) http.HandlerFunc { return s.handleReadyz }},
	{Method: "GET", Pattern: "/versionz", Auth: authNone,
		Handler: func(s *Server) http.HandlerFunc { return s.handleVersionz }},
}
