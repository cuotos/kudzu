package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cuotos/kudzu/internal/gate"
	"github.com/cuotos/kudzu/internal/schedule"
	"github.com/cuotos/kudzu/internal/store/memory"
)

// blockedRow returns the blocked-table row for a service. Each row is rendered
// on its own source line, so a line match is enough to isolate one gate.
func blockedRow(t *testing.T, body, service string) string {
	t.Helper()
	for line := range strings.SplitSeq(body, "\n") {
		if strings.Contains(line, "<td>"+service+"</td>") && strings.Contains(line, `class="state"`) {
			return line
		}
	}
	t.Fatalf("no blocked row for %q in:\n%s", service, body)
	return ""
}

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
		`class="gate-table"`,
		`<tr class="is-tripped">`, // the tripped row carries its own accent
		`class="open-table"`,
		"<td>orders</td><td>staging</td>", // the open gate, as a table row
	} {
		if !strings.Contains(body, want) {
			t.Errorf("dashboard missing %q", want)
		}
	}
}

func TestDashboardOpenTableHasARowPerOpenGate(t *testing.T) {
	h := newTestRouter()

	for _, env := range []string{"staging", "production", "sandbox"} {
		do(t, h, http.MethodPost, "/v1/deploy-result", testToken,
			map[string]any{"service": "orders", "env": env, "status": "success", "repo": "cuotos/orders"})
	}
	// A frozen gate belongs in the blocked cards, not the open table.
	do(t, h, http.MethodPost, "/v1/gate/freeze", testToken,
		map[string]any{"service": "orders", "env": "production", "reason": "incident", "actor": "dan"})

	_, body := getHTML(t, h, "/ui", "")

	_, table, found := strings.Cut(body, `class="open-table"`)
	if !found {
		t.Fatalf("no open table rendered:\n%s", body)
	}
	table, _, _ = strings.Cut(table, "</table>")

	if got := strings.Count(table, "<tr><td>"); got != 2 {
		t.Errorf("open table has %d body rows, want 2", got)
	}
	if !strings.Contains(table, "<td>orders</td><td>sandbox</td>") ||
		!strings.Contains(table, "<td>orders</td><td>staging</td>") {
		t.Errorf("missing an open row:\n%s", table)
	}
	if strings.Contains(table, "<td>production</td>") {
		t.Errorf("the frozen gate leaked into the open table:\n%s", table)
	}
	// Rows keep the service/env sort, so the table does not reshuffle on refresh.
	if strings.Index(table, "sandbox") > strings.Index(table, "staging") {
		t.Errorf("rows out of order:\n%s", table)
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

	d := buildDashboard(gates, nil, now)

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
	if row := blockedRow(t, body, "orders"); !strings.Contains(row, "in 1h") {
		t.Errorf("expected the TTL in the orders row, got:\n%s", row)
	}
	// The gate with no TTL keeps the column, filled with an em dash.
	if row := blockedRow(t, body, "billing"); !strings.HasSuffix(row, "<td class=\"soft\" title=\"\">—</td></tr>") {
		t.Errorf("expected an empty lifts cell for billing, got:\n%s", row)
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
	row := blockedRow(t, body, "orders")
	for _, want := range []string{"schedule", "release train", "in 50m"} {
		if !strings.Contains(row, want) {
			t.Errorf("blocked row missing %q:\n%s", want, row)
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
	}, nil, now)

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

// newUIWritesRouter is the router with the board's write controls enabled.
func newUIWritesRouter() http.Handler {
	svc := gate.NewService(memory.New(), gate.NoopEvicter{}, gate.Config{FailureThreshold: 1}, nil)
	return NewRouter(Options{
		Service:     svc,
		WriteTokens: []string{testToken},
		UIWrites:    true,
	})
}

// scheduleRow returns the schedules-table row for a service. Each row is
// rendered on its own source line, as the gate rows are. It matches on the
// window cell rather than on a control, so it works with writes on or off.
func scheduleRow(t *testing.T, body, service string) string {
	t.Helper()
	for line := range strings.SplitSeq(body, "\n") {
		if strings.Contains(line, "<td>"+service+"</td>") && strings.Contains(line, `class="window"`) {
			return line
		}
	}
	t.Fatalf("no schedule row for %q in:\n%s", service, body)
	return ""
}

func TestDashboardListsScheduleWindows(t *testing.T) {
	h := newTestRouter()

	// A recurring window, and a one-off window that is in force right now.
	do(t, h, http.MethodPost, "/v1/schedules", testToken, map[string]any{
		"service": "orders", "env": "production", "id": "friday",
		"reason": "no friday deploys", "cron": "0 18 * * 5", "duration_seconds": 12 * 3600,
	})
	now := time.Now()
	do(t, h, http.MethodPost, "/v1/schedules", testToken, map[string]any{
		"service": "billing", "env": "staging", "id": "xmas",
		"reason": "change freeze", "start": now.Add(-time.Hour), "end": now.Add(3 * time.Hour),
	})

	_, body := getHTML(t, h, "/ui", "")

	friday := scheduleRow(t, body, "orders")
	for _, want := range []string{"orders", "production", "0 18 * * 5", "12h", "no friday deploys"} {
		if !strings.Contains(friday, want) {
			t.Errorf("recurring row missing %q: %s", want, friday)
		}
	}

	xmas := scheduleRow(t, body, "billing")
	if !strings.Contains(xmas, "is-active") {
		t.Errorf("in-force window not marked active: %s", xmas)
	}
	if !strings.Contains(xmas, "change freeze") {
		t.Errorf("one-off row missing reason: %s", xmas)
	}
}

func TestDashboardScheduleRowsCarryDeleteControls(t *testing.T) {
	h := newUIWritesRouter()
	do(t, h, http.MethodPost, "/v1/schedules", testToken, map[string]any{
		"service": "orders", "env": "production", "id": "friday",
		"cron": "0 18 * * 5", "duration_seconds": 3600,
	})

	_, body := getHTML(t, h, "/ui", "")
	row := scheduleRow(t, body, "orders")
	for _, want := range []string{`data-act="delete-schedule"`, `data-service="orders"`, `data-env="production"`, `data-id="friday"`} {
		if !strings.Contains(row, want) {
			t.Errorf("schedule row missing %q: %s", want, row)
		}
	}
}

func TestDashboardGateRowsCarryFreezeAndUnfreezeControls(t *testing.T) {
	h := newUIWritesRouter()
	do(t, h, http.MethodPost, "/v1/gate/freeze", testToken,
		map[string]any{"service": "orders", "env": "production", "reason": "incident", "actor": "dan"})
	do(t, h, http.MethodPost, "/v1/deploy-result", testToken,
		map[string]any{"service": "billing", "env": "staging", "status": "success", "repo": "acme/billing"})

	_, body := getHTML(t, h, "/ui", "")

	blocked := blockedRow(t, body, "orders")
	if !strings.Contains(blocked, `data-act="unfreeze"`) {
		t.Errorf("blocked row has no unfreeze control: %s", blocked)
	}

	// The open gate's row lives in the quiet table; find it by service cell.
	var open string
	for line := range strings.SplitSeq(body, "\n") {
		if strings.Contains(line, "<td>billing</td>") && !strings.Contains(line, `class="state"`) {
			open = line
		}
	}
	if open == "" {
		t.Fatalf("no open row for billing in:\n%s", body)
	}
	if !strings.Contains(open, `data-act="freeze"`) {
		t.Errorf("open row has no freeze control: %s", open)
	}
	if !strings.Contains(open, `data-env="staging"`) {
		t.Errorf("open row missing env data attribute: %s", open)
	}
}

func TestDashboardEscapesScheduleFields(t *testing.T) {
	h := newTestRouter()
	do(t, h, http.MethodPost, "/v1/schedules", testToken, map[string]any{
		"service": "orders", "env": "production", "id": "evil",
		"reason": `<script>alert(1)</script>`, "cron": "0 18 * * 5", "duration_seconds": 3600,
	})

	_, body := getHTML(t, h, "/ui", "")
	if strings.Contains(body, "<script>alert(1)</script>") {
		t.Fatalf("schedule reason rendered unescaped:\n%s", body)
	}
}

func TestBuildDashboardFormatsScheduleWindows(t *testing.T) {
	now := time.Date(2026, 3, 2, 12, 0, 0, 0, time.UTC)
	start, end := now.Add(-time.Hour), now.Add(2*time.Hour)

	d := buildDashboard(nil, []gate.ScheduleEntry{
		{
			Service: "orders", Env: "production",
			Schedule: schedule.Schedule{ID: "friday", Cron: "0 18 * * 5", Duration: 12 * time.Hour},
		},
		{
			Service: "billing", Env: "staging",
			Schedule: schedule.Schedule{ID: "xmas", Start: &start, End: &end},
			Active:   true, Since: &start, Until: &end,
		},
	}, now)

	if len(d.Schedules) != 2 {
		t.Fatalf("want 2 schedules, got %d", len(d.Schedules))
	}
	byID := map[string]uiSchedule{}
	for _, row := range d.Schedules {
		byID[row.ID] = row
	}

	if got := byID["friday"].Window; got != "0 18 * * 5 for 12h" {
		t.Errorf("recurring window label = %q", got)
	}
	if byID["friday"].Active {
		t.Error("recurring window wrongly marked active")
	}
	if got := byID["xmas"].Window; !strings.Contains(got, "2026") {
		t.Errorf("one-off window label = %q, want an absolute interval", got)
	}
	if got := byID["xmas"].Lifts; got != "in 2h" {
		t.Errorf("active window lifts = %q", got)
	}
}

func TestDashboardOmitsUnfreezeOnScheduledFreeze(t *testing.T) {
	h := newUIWritesRouter()
	now := time.Now()

	// A window in force right now, so the gate is frozen by the schedule.
	do(t, h, http.MethodPost, "/v1/schedules", testToken, map[string]any{
		"service": "orders", "env": "production", "id": "freeze-now",
		"reason": "change freeze",
		"start":  now.Add(-time.Hour).Format(time.RFC3339),
		"end":    now.Add(time.Hour).Format(time.RFC3339),
	})
	// And a manual freeze on a different gate, which unfreeze *can* lift.
	do(t, h, http.MethodPost, "/v1/gate/freeze", testToken,
		map[string]any{"service": "billing", "env": "staging", "reason": "incident", "actor": "dan"})

	_, body := getHTML(t, h, "/ui", "")

	// Unfreeze clears a manual freeze and resets a trip, but a scheduled gate
	// recomputes from its window and freezes straight back, so offering the
	// button there would be a lie.
	scheduled := blockedRow(t, body, "orders")
	if strings.Contains(scheduled, `data-act="unfreeze"`) {
		t.Errorf("scheduled freeze should carry no unfreeze control: %s", scheduled)
	}
	// The cell stays, so the row keeps its shape, but it is empty: the Source
	// column already says "schedule", and the windows table is directly below.
	if !strings.Contains(scheduled, `<td class="actions"></td>`) {
		t.Errorf("scheduled freeze should have an empty action cell: %s", scheduled)
	}

	manual := blockedRow(t, body, "billing")
	if !strings.Contains(manual, `data-act="unfreeze"`) {
		t.Errorf("manual freeze should keep its unfreeze control: %s", manual)
	}
}

func TestDashboardOmitsUnfreezeOnTrippedGateOnlyWhenScheduled(t *testing.T) {
	h := newUIWritesRouter()
	do(t, h, http.MethodPost, "/v1/deploy-result", testToken,
		map[string]any{"service": "web", "env": "production", "status": "failed", "repo": "cuotos/web"})

	_, body := getHTML(t, h, "/ui", "")
	// Unfreeze resets a trip, so a tripped gate keeps the control.
	if row := blockedRow(t, body, "web"); !strings.Contains(row, `data-act="unfreeze"`) {
		t.Errorf("tripped gate should keep its unfreeze control: %s", row)
	}
}

func TestDashboardHasNoControlsUnlessUIWritesIsOn(t *testing.T) {
	h := newTestRouter() // KUDZU_UI_WRITES defaults off
	do(t, h, http.MethodPost, "/v1/gate/freeze", testToken,
		map[string]any{"service": "orders", "env": "production", "reason": "incident", "actor": "dan"})
	do(t, h, http.MethodPost, "/v1/schedules", testToken, map[string]any{
		"service": "orders", "env": "production", "cron": "0 18 * * 5", "duration_seconds": 3600,
	})

	_, body := getHTML(t, h, "/ui", "")
	// Nothing to press, rather than something merely hidden: no controls, no
	// dialogs, no session script.
	for _, unwanted := range []string{"data-act=", "<dialog", `id="session"`, "sessionStorage", "kudzu.token"} {
		if strings.Contains(body, unwanted) {
			t.Errorf("read-only board should not contain %q", unwanted)
		}
	}
	if !strings.Contains(body, "KUDZU_UI_WRITES") {
		t.Error("read-only board should say how to turn the controls on")
	}
	// The board still refreshes itself, and still lists windows.
	if !strings.Contains(body, "setInterval(refresh, 15000)") {
		t.Error("read-only board lost its auto-refresh")
	}
	if !strings.Contains(body, "0 18 * * 5") {
		t.Error("read-only board lost its freeze windows")
	}
}

func TestDashboardHasControlsWhenUIWritesIsOn(t *testing.T) {
	h := newUIWritesRouter()
	_, body := getHTML(t, h, "/ui", "")
	for _, want := range []string{`id="session"`, "<dialog", "sessionStorage", `data-act="new-schedule"`, `data-act="new-freeze"`} {
		if !strings.Contains(body, want) {
			t.Errorf("interactive board missing %q", want)
		}
	}
	if !strings.Contains(body, "setInterval(refresh, 15000)") {
		t.Error("interactive board lost its auto-refresh")
	}
}

func TestDashboardReasonIsOptional(t *testing.T) {
	h := newUIWritesRouter()
	_, body := getHTML(t, h, "/ui", "")

	// The API has never required a reason, so neither should the forms.
	for line := range strings.SplitSeq(body, "\n") {
		if strings.Contains(line, `name="reason"`) && strings.Contains(line, "required") {
			t.Errorf("reason input should not be required: %s", strings.TrimSpace(line))
		}
	}

	// A gate frozen without one keeps its row shape.
	do(t, h, http.MethodPost, "/v1/gate/freeze", testToken,
		map[string]any{"service": "orders", "env": "production", "actor": "dan"})
	_, body = getHTML(t, h, "/ui", "")
	if row := blockedRow(t, body, "orders"); !strings.Contains(row, `<td class="reason">—</td>`) {
		t.Errorf("reasonless freeze should render an em dash: %s", row)
	}
}
