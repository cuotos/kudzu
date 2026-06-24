package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

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

func TestHealthAndReady(t *testing.T) {
	h := newTestRouter()
	if rec, _ := do(t, h, http.MethodGet, "/healthz", "", nil); rec.Code != http.StatusOK {
		t.Fatalf("healthz: %d", rec.Code)
	}
	if rec, _ := do(t, h, http.MethodGet, "/readyz", "", nil); rec.Code != http.StatusOK {
		t.Fatalf("readyz: %d", rec.Code)
	}
}
