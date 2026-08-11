package httpapi

import (
	"encoding/json"
	"maps"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/cuotos/kudzu/internal/gate"
	"github.com/cuotos/kudzu/internal/schedule"
	"github.com/cuotos/kudzu/internal/store/memory"
)

// specOnlyPaths are documented but not registered from the routes table.
// /metrics is served from a handler the caller supplies, so it cannot be.
var specOnlyPaths = map[string]bool{"/metrics": true}

// schemaTypes ties each documented schema to the Go type it describes, so
// TestOpenAPISchemasMatchGoTypes can compare field names. Schemas with no
// single backing type (envelopes, the error shape) are checked by hand below.
var schemaTypes = map[string]any{
	"Gate":            gate.Gate{},
	"DeployResult":    gate.DeployResult{},
	"ScheduleEntry":   gate.ScheduleEntry{},
	"Schedule":        schedule.Schedule{},
	"ScheduleRequest": scheduleReq{},
	"FreezeRequest":   freezeReq{},
	"UnfreezeRequest": unfreezeReq{},
	"Version":         versionInfo{},
}

func TestOpenAPIDocumentsEveryRoute(t *testing.T) {
	// Every registered route is documented...
	for _, rt := range routes {
		ops, ok := apiSpec.Paths[rt.docPath()]
		if !ok {
			t.Errorf("%s %s is registered but missing from openapi.json", rt.Method, rt.docPath())
			continue
		}
		if _, ok := ops[strings.ToLower(rt.Method)]; !ok {
			t.Errorf("openapi.json documents %s but not its %s operation", rt.docPath(), rt.Method)
		}
	}

	// ...and nothing is documented that is not served.
	served := map[string]bool{}
	for _, rt := range routes {
		served[strings.ToLower(rt.Method)+" "+rt.docPath()] = true
	}
	for path, ops := range apiSpec.Paths {
		if specOnlyPaths[path] {
			continue
		}
		for method := range ops {
			if !served[method+" "+path] {
				t.Errorf("openapi.json documents %s %s, which is not registered", strings.ToUpper(method), path)
			}
		}
	}
}

func TestOpenAPIMatchesRouteAuth(t *testing.T) {
	for _, rt := range routes {
		op, ok := apiSpec.Paths[rt.docPath()][strings.ToLower(rt.Method)]
		if !ok {
			continue // reported by TestOpenAPIDocumentsEveryRoute
		}
		documented := len(op.Security) > 0
		want := rt.Auth == authWrite
		if documented != want {
			t.Errorf("%s %s: spec says bearer=%v, routes table says %v",
				rt.Method, rt.docPath(), documented, want)
		}
	}
}

func TestOpenAPISchemasMatchGoTypes(t *testing.T) {
	for name, v := range schemaTypes {
		sc, ok := apiSpec.Components.Schemas[name]
		if !ok {
			t.Errorf("schema %s is missing from openapi.json", name)
			continue
		}

		want := jsonFields(reflect.TypeOf(v))
		got := slices.Sorted(maps.Keys(sc.Properties))
		if !slices.Equal(want, got) {
			t.Errorf("schema %s fields drifted from %T:\n  spec: %v\n  code: %v", name, v, got, want)
		}
	}
}

// TestOpenAPIEnvelopeSchemas covers the wrappers that have no single Go type:
// the handlers build them from a map literal.
func TestOpenAPIEnvelopeSchemas(t *testing.T) {
	for schemaName, field := range map[string]string{
		"GateList":     "gates",
		"ScheduleList": "schedules",
		"Error":        "error",
		"Status":       "status",
	} {
		sc, ok := apiSpec.Components.Schemas[schemaName]
		if !ok {
			t.Errorf("schema %s is missing", schemaName)
			continue
		}
		if _, ok := sc.Properties[field]; !ok {
			t.Errorf("schema %s should wrap %q", schemaName, field)
		}
	}
}

// jsonFields returns the sorted JSON field names a struct marshals to,
// flattening embedded structs the way encoding/json inlines them.
func jsonFields(t reflect.Type) []string {
	var out []string
	for i := range t.NumField() {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		tag, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if tag == "-" {
			continue
		}
		if f.Anonymous && tag == "" && f.Type.Kind() == reflect.Struct {
			out = append(out, jsonFields(f.Type)...)
			continue
		}
		if tag == "" {
			tag = f.Name
		}
		out = append(out, tag)
	}
	slices.Sort(out)
	return out
}

func TestOpenAPIEndpointServesTheSpec(t *testing.T) {
	h := newTestRouter()

	rec, body := getHTML(t, h, "/openapi.json", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("content-type=%q", ct)
	}
	// It must be valid JSON, since clients will feed it to a generator.
	var doc map[string]any
	if err := json.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatalf("served spec is not valid JSON: %v", err)
	}
	if doc["openapi"] != "3.1.0" {
		t.Errorf("openapi = %v", doc["openapi"])
	}
}

func TestDocsPageRendersEveryOperation(t *testing.T) {
	h := newTestRouter()

	rec, body := getHTML(t, h, "/docs", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("content-type=%q", ct)
	}

	for _, rt := range routes {
		if !strings.Contains(body, ">"+rt.docPath()+"<") {
			t.Errorf("docs page does not mention %s", rt.docPath())
		}
	}
	// Write endpoints must be visibly marked as needing a token.
	if !strings.Contains(body, "bearer token") {
		t.Error("docs page does not mark authenticated operations")
	}
	// Schemas are rendered with anchors the operations link to.
	if !strings.Contains(body, `id="schema-Gate"`) {
		t.Error("docs page is missing the Gate schema")
	}
}

func TestDocsFollowReadAuth(t *testing.T) {
	svc := gate.NewService(memory.New(), gate.NoopEvicter{}, gate.Config{FailureThreshold: 1}, nil)
	h := NewRouter(Options{Service: svc, WriteTokens: []string{testToken}, RequireReadAuth: true})

	for _, path := range []string{"/docs", "/openapi.json"} {
		if rec, _ := getHTML(t, h, path, ""); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s unauthenticated: code=%d, want 401", path, rec.Code)
		}
		if rec, _ := getHTML(t, h, path, testToken); rec.Code != http.StatusOK {
			t.Errorf("%s authenticated: code=%d, want 200", path, rec.Code)
		}
	}
}
