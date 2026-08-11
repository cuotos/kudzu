package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/cuotos/kudzu/internal/gate"
	"github.com/cuotos/kudzu/internal/store/memory"
)

const testToken = "s3cret"

func newTestRouter() http.Handler {
	svc := gate.NewService(memory.New(), gate.NoopEvicter{}, gate.Config{FailureThreshold: 1}, nil)
	return NewRouter(Options{
		Service:     svc,
		WriteTokens: []string{testToken},
	})
}

func do(t *testing.T, h http.Handler, method, path, token string, body any) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rdr = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, rdr)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var out map[string]any
	if rec.Body.Len() > 0 {
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
	}
	return rec, out
}

func TestGateLifecycleOverHTTP(t *testing.T) {
	h := newTestRouter()

	// Initially open.
	rec, body := do(t, h, http.MethodGet, "/v1/gate?service=orders&env=production", "", nil)
	if rec.Code != http.StatusOK || body["state"] != "open" || body["allowed"] != true {
		t.Fatalf("initial: code=%d body=%v", rec.Code, body)
	}

	// Freeze (auth required).
	rec, body = do(t, h, http.MethodPost, "/v1/gate/freeze", testToken,
		map[string]any{"service": "orders", "env": "production", "reason": "incident", "actor": "dan"})
	if rec.Code != http.StatusOK || body["state"] != "frozen" || body["allowed"] != false {
		t.Fatalf("freeze: code=%d body=%v", rec.Code, body)
	}

	// Gate now blocked.
	_, body = do(t, h, http.MethodGet, "/v1/gate?service=orders&env=production", "", nil)
	if body["allowed"] != false {
		t.Fatalf("expected blocked, got %v", body)
	}

	// Unfreeze.
	rec, body = do(t, h, http.MethodPost, "/v1/gate/unfreeze", testToken,
		map[string]any{"service": "orders", "env": "production", "actor": "dan"})
	if rec.Code != http.StatusOK || body["state"] != "open" {
		t.Fatalf("unfreeze: code=%d body=%v", rec.Code, body)
	}
}

func TestDeployFailureTripsOverHTTP(t *testing.T) {
	h := newTestRouter()
	rec, body := do(t, h, http.MethodPost, "/v1/deploy-result", testToken,
		map[string]any{"service": "orders", "env": "production", "status": "failed", "repo": "bw/orders"})
	if rec.Code != http.StatusOK || body["state"] != "tripped" {
		t.Fatalf("deploy-result: code=%d body=%v", rec.Code, body)
	}
}

func TestWriteRequiresAuth(t *testing.T) {
	h := newTestRouter()
	rec, _ := do(t, h, http.MethodPost, "/v1/gate/freeze", "",
		map[string]any{"service": "orders", "env": "production"})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("want 401 without token, got %d", rec.Code)
	}
	rec, _ = do(t, h, http.MethodPost, "/v1/gate/freeze", "wrong-token",
		map[string]any{"service": "orders", "env": "production"})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("want 401 with bad token, got %d", rec.Code)
	}
}

func TestMissingServiceIsBadRequest(t *testing.T) {
	h := newTestRouter()
	rec, _ := do(t, h, http.MethodGet, "/v1/gate?service=orders", "", nil) // no env
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400 for missing env, got %d", rec.Code)
	}
}

func TestScheduleCRUDOverHTTP(t *testing.T) {
	h := newTestRouter()

	rec, body := do(t, h, http.MethodPost, "/v1/schedules", testToken, map[string]any{
		"service": "orders", "env": "production",
		"cron": "0 14 * * 5", "duration_seconds": 14400, "reason": "friday freeze",
	})
	if rec.Code != http.StatusCreated || body["id"] == nil {
		t.Fatalf("add schedule: code=%d body=%v", rec.Code, body)
	}
	id, _ := body["id"].(string)

	rec, body = do(t, h, http.MethodGet, "/v1/schedules?service=orders&env=production", "", nil)
	scs, _ := body["schedules"].([]any)
	if rec.Code != http.StatusOK || len(scs) != 1 {
		t.Fatalf("list schedules: code=%d body=%v", rec.Code, body)
	}

	rec, _ = do(t, h, http.MethodDelete, "/v1/schedules/"+id+"?service=orders&env=production", testToken, nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete schedule: code=%d", rec.Code)
	}
}

func TestListAllSchedulesOverHTTP(t *testing.T) {
	h := newTestRouter()
	now := time.Now()

	// An inactive window on one gate, an active one on another.
	rec, _ := do(t, h, http.MethodPost, "/v1/schedules", testToken, map[string]any{
		"service": "orders", "env": "production",
		"cron": "0 14 * * 5", "duration_seconds": 14400, "reason": "friday freeze",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("add recurring: code=%d", rec.Code)
	}
	rec, _ = do(t, h, http.MethodPost, "/v1/schedules", testToken, map[string]any{
		"service": "billing", "env": "staging", "reason": "migration",
		"start": now.Add(-time.Hour).Format(time.RFC3339),
		"end":   now.Add(time.Hour).Format(time.RFC3339),
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("add one-off: code=%d", rec.Code)
	}

	// No service/env: every window Kudzu knows about.
	rec, body := do(t, h, http.MethodGet, "/v1/schedules", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list all: code=%d body=%v", rec.Code, body)
	}
	entries, _ := body["schedules"].([]any)
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2: %v", len(entries), body)
	}

	byGate := map[string]map[string]any{}
	for _, e := range entries {
		m, _ := e.(map[string]any)
		byGate[fmt.Sprintf("%v/%v", m["service"], m["env"])] = m
	}

	active, ok := byGate["billing/staging"]
	if !ok {
		t.Fatalf("missing billing/staging in %v", byGate)
	}
	if active["active"] != true {
		t.Errorf("billing/staging active = %v, want true", active["active"])
	}
	// An active entry reports the bounds of the occurrence it is inside.
	if active["since"] == nil || active["until"] == nil {
		t.Errorf("active entry missing bounds: %v", active)
	}
	// The schedule's own fields stay inlined, so existing readers still work.
	if active["reason"] != "migration" || active["id"] == nil {
		t.Errorf("schedule fields not inlined: %v", active)
	}

	inactive, ok := byGate["orders/production"]
	if !ok {
		t.Fatalf("missing orders/production in %v", byGate)
	}
	if inactive["active"] != false {
		t.Errorf("orders/production active = %v, want false", inactive["active"])
	}
	if inactive["since"] != nil || inactive["until"] != nil {
		t.Errorf("inactive entry should have no bounds: %v", inactive)
	}
}

func TestListAllSchedulesEmptyIsAnArray(t *testing.T) {
	h := newTestRouter()
	rec, body := do(t, h, http.MethodGet, "/v1/schedules", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
	// An empty list must marshal as [] rather than null.
	if scs, ok := body["schedules"].([]any); !ok || len(scs) != 0 {
		t.Fatalf("schedules = %#v, want an empty array", body["schedules"])
	}
}

func TestListSchedulesRejectsHalfAKey(t *testing.T) {
	h := newTestRouter()
	for _, q := range []string{"?service=orders", "?env=production"} {
		if rec, _ := do(t, h, http.MethodGet, "/v1/schedules"+q, "", nil); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: code=%d, want 400", q, rec.Code)
		}
	}
}

func TestHealthAndReady(t *testing.T) {
	h := newTestRouter()
	if rec, _ := do(t, h, http.MethodGet, "/healthz", "", nil); rec.Code != http.StatusOK {
		t.Fatalf("healthz: %d", rec.Code)
	}
	if rec, _ := do(t, h, http.MethodGet, "/readyz", "", nil); rec.Code != http.StatusOK {
		t.Fatalf("readyz: %d", rec.Code)
	}
}
