package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cuotos/kudzu/internal/gate"
	"github.com/cuotos/kudzu/internal/store/memory"
)

// getHTML fetches path and returns the response and its body as a string.
func getHTML(t *testing.T, h http.Handler, path, token string) (*httptest.ResponseRecorder, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec, rec.Body.String()
}

func TestDashboardServedAtRootAndUI(t *testing.T) {
	h := newTestRouter()

	for _, path := range []string{"/", "/ui"} {
		rec, body := getHTML(t, h, path, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: code=%d", path, rec.Code)
		}
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
			t.Fatalf("%s: content-type=%q", path, ct)
		}
		// No gates in a fresh store: the empty state, not a zero count.
		if !strings.Contains(body, "no gates yet") {
			t.Fatalf("%s: expected empty state, got:\n%s", path, body)
		}
	}
}

func TestDashboardShowsBlockedAndOpenGates(t *testing.T) {
	h := newTestRouter()

	// One manual freeze, one trip, one gate left open.
	do(t, h, http.MethodPost, "/v1/gate/freeze", testToken,
		map[string]any{"service": "billing", "env": "production", "reason": "incident 4021", "actor": "dan"})
	do(t, h, http.MethodPost, "/v1/deploy-result", testToken,
		map[string]any{"service": "orders", "env": "production", "status": "failed", "repo": "cuotos/orders"})
	do(t, h, http.MethodPost, "/v1/deploy-result", testToken,
		map[string]any{"service": "orders", "env": "staging", "status": "success", "repo": "cuotos/orders"})

	rec, body := getHTML(t, h, "/ui", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
	for _, want := range []string{
		`data-mood="tripped"`, // a trip outranks a freeze for the page accent
		"blocked",
		"incident 4021",
		"billing",
		"orders/staging", // the open gate, in the quiet list
	} {
		if !strings.Contains(body, want) {
			t.Errorf("dashboard missing %q", want)
		}
	}
}

func TestDashboardEscapesGateFields(t *testing.T) {
	h := newTestRouter()
	do(t, h, http.MethodPost, "/v1/gate/freeze", testToken, map[string]any{
		"service": "orders", "env": "production",
		"reason": `<script>alert(1)</script>`, "actor": "dan",
	})

	_, body := getHTML(t, h, "/ui", "")
	if strings.Contains(body, "<script>alert(1)</script>") {
		t.Fatal("reason was not escaped")
	}
	if !strings.Contains(body, "alert(1)") {
		t.Fatal("expected the escaped reason to still be rendered")
	}
}

func TestDashboardHonoursReadAuth(t *testing.T) {
	svc := gate.NewService(memory.New(), gate.NoopEvicter{}, gate.Config{FailureThreshold: 1}, nil)
	h := NewRouter(Options{Service: svc, WriteTokens: []string{testToken}, RequireReadAuth: true})

	if rec, _ := getHTML(t, h, "/ui", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated: code=%d, want 401", rec.Code)
	}
	if rec, _ := getHTML(t, h, "/ui", testToken); rec.Code != http.StatusOK {
		t.Fatalf("authenticated: code=%d, want 200", rec.Code)
	}
}

func TestDashboardDoesNotSwallowUnknownPaths(t *testing.T) {
	h := newTestRouter()
	if rec, _ := getHTML(t, h, "/nope", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("code=%d, want 404", rec.Code)
	}
}

func TestBuildDashboardOrdersAndSummarises(t *testing.T) {
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	gates := []gate.Gate{
		{Service: "web", Env: "production", State: gate.StateOpen, Allowed: true},
		{Service: "billing", Env: "staging", State: gate.StateFrozen, Source: gate.SourceManual,
			Since: now.Add(-90 * time.Minute)},
		{Service: "billing", Env: "production", State: gate.StateFrozen, Source: gate.SourceSchedule},
	}

	d := buildDashboard(gates, now)

	if d.Mood != "frozen" {
		t.Errorf("Mood = %q, want frozen", d.Mood)
	}
	if d.Count != "2" || d.Verdict != "blocked" {
		t.Errorf("Count/Verdict = %q/%q, want 2/blocked", d.Count, d.Verdict)
	}
	if d.Tracked != "3 gates" {
		t.Errorf("Tracked = %q", d.Tracked)
	}
	// Blocked gates sort by service then env, so the board does not reshuffle.
	if d.Blocked[0].Env != "production" || d.Blocked[1].Env != "staging" {
		t.Errorf("blocked order = %q, %q", d.Blocked[0].Env, d.Blocked[1].Env)
	}
	if len(d.Open) != 1 || d.Open[0].Service != "web" {
		t.Errorf("Open = %+v", d.Open)
	}
	// A zero Since renders no age at all rather than "56 years ago".
	if d.Blocked[0].Age != "" {
		t.Errorf("zero since produced Age = %q", d.Blocked[0].Age)
	}
	if d.Blocked[1].Age != "1h ago" {
		t.Errorf("Age = %q, want 1h ago", d.Blocked[1].Age)
	}
}

func TestDashboardShowsFreezeTTL(t *testing.T) {
	h := newTestRouter()

	// A freeze with a TTL, and one without.
	do(t, h, http.MethodPost, "/v1/gate/freeze", testToken, map[string]any{
		"service": "orders", "env": "production", "reason": "release train",
		"actor": "dan", "ttl_seconds": 5400,
	})
	do(t, h, http.MethodPost, "/v1/gate/freeze", testToken, map[string]any{
		"service": "billing", "env": "production", "reason": "incident", "actor": "dan",
	})

	_, body := getHTML(t, h, "/ui", "")
	if !strings.Contains(body, "in 1h") {
		t.Errorf("expected the TTL to be rendered, got:\n%s", body)
	}
	// One card has a TTL, the other must not grow an empty row for it.
	if got := strings.Count(body, "<dt>lifts</dt>"); got != 1 {
		t.Errorf("lifts rows = %d, want 1", got)
	}
}

func TestDashboardShowsScheduleWindowEnd(t *testing.T) {
	h := newTestRouter()
	now := time.Now()

	rec, _ := do(t, h, http.MethodPost, "/v1/schedules", testToken, map[string]any{
		"service": "orders", "env": "production", "reason": "release train",
		"start": now.Add(-10 * time.Minute).Format(time.RFC3339),
		// Half a minute of slack so the rendered "in 50m" does not depend on how
		// long the test takes to reach the render.
		"end": now.Add(50*time.Minute + 30*time.Second).Format(time.RFC3339),
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("add schedule: code=%d", rec.Code)
	}

	_, body := getHTML(t, h, "/ui", "")
	for _, want := range []string{"schedule", "release train", "<dt>lifts</dt>", "in 50m"} {
		if !strings.Contains(body, want) {
			t.Errorf("dashboard missing %q", want)
		}
	}
}

func TestBuildDashboardFormatsTTL(t *testing.T) {
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	exp := now.Add(42 * time.Minute)
	d := buildDashboard([]gate.Gate{
		{Service: "orders", Env: "production", State: gate.StateFrozen,
			Source: gate.SourceManual, Since: now, ExpiresAt: &exp},
		{Service: "web", Env: "production", State: gate.StateTripped, Source: gate.SourceBreaker, Since: now},
	}, now)

	if d.Blocked[0].Expires != "in 42m" {
		t.Errorf("Expires = %q, want in 42m", d.Blocked[0].Expires)
	}
	if d.Blocked[0].ExpiresStamp == "" {
		t.Error("expected an absolute expiry for the hover title")
	}
	// A trip has no TTL, so it must stay empty rather than render a zero time.
	if d.Blocked[1].Expires != "" {
		t.Errorf("tripped gate got Expires = %q", d.Blocked[1].Expires)
	}
}

func TestRemainingLabels(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{-time.Minute, "any moment"},
		{30 * time.Second, "in under a minute"},
		{45 * time.Minute, "in 45m"},
		{5 * time.Hour, "in 5h"},
		{96 * time.Hour, "in 4d"},
	}
	for _, c := range cases {
		if got := remaining(c.d); got != c.want {
			t.Errorf("remaining(%s) = %q, want %q", c.d, got, c.want)
		}
	}
}

func TestAgeLabels(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{-time.Second, "just now"},
		{30 * time.Second, "just now"},
		{5 * time.Minute, "5m ago"},
		{3 * time.Hour, "3h ago"},
		{72 * time.Hour, "3d ago"},
	}
	for _, c := range cases {
		if got := age(c.d); got != c.want {
			t.Errorf("age(%s) = %q, want %q", c.d, got, c.want)
		}
	}
}
