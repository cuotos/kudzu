package httpapi

import (
	"bytes"
	"cmp"
	"embed"
	"fmt"
	"html/template"
	"net/http"
	"slices"
	"time"

	"github.com/cuotos/kudzu/internal/gate"
	"github.com/cuotos/kudzu/internal/schedule"
)

//go:embed templates/dashboard.html templates/docs.html
var uiFS embed.FS

// dashboardTmpl is parsed at init so a broken template fails the binary, not a
// request.
var dashboardTmpl = template.Must(template.New("dashboard.html").
	Funcs(template.FuncMap{"dash": dash}).
	ParseFS(uiFS, "templates/dashboard.html"))

// dash renders an empty cell as an em dash, so a table row keeps its shape when
// a gate has no reason, actor, or expiry.
func dash(v any) string {
	if s := fmt.Sprint(v); s != "" {
		return s
	}
	return "—"
}

// uiGate is one gate as the dashboard shows it: the gate plus pre-formatted
// timestamps, so the template stays logic-free.
type uiGate struct {
	gate.Gate
	Age          string // "12m ago"; empty when the gate has no since
	Stamp        string // absolute timestamp, shown on hover
	Expires      string // "in 42m"; empty when the state has no TTL
	ExpiresStamp string // absolute expiry, shown on hover
}

// uiSchedule is one freeze window as the dashboard shows it: the gate it
// belongs to plus a pre-formatted window label, so the template stays
// logic-free.
type uiSchedule struct {
	Service string
	Env     string
	ID      string
	Reason  string
	Window  string // "0 18 * * 5 for 12h", or an absolute interval for a one-off
	Active  bool   // the window contains now
	Lifts   string // "in 2h"; empty unless active
	Stamp   string // absolute end of the current occurrence, shown on hover
}

// uiData is the dashboard view model.
type uiData struct {
	Mood     string // open | frozen | tripped | empty — the worst state present
	Count    string // the big number; empty when there is nothing to count
	Verdict  string
	Subtitle string
	Tracked  string
	Updated  string
	Blocked  []uiGate
	Open     []uiGate

	Schedules []uiSchedule
}

// handleUI renders the gate board.
func (s *Server) handleUI(w http.ResponseWriter, r *http.Request) {
	gates, err := s.svc.List(r.Context())
	if err != nil {
		s.log.Error("dashboard failed", "err", err)
		http.Error(w, "kudzu: cannot read gate state", http.StatusInternalServerError)
		return
	}
	schedules, err := s.svc.ListAllSchedules(r.Context())
	if err != nil {
		s.log.Error("dashboard failed", "err", err)
		http.Error(w, "kudzu: cannot read freeze windows", http.StatusInternalServerError)
		return
	}

	// Render to a buffer so a template error cannot emit half a page.
	var buf bytes.Buffer
	if err := dashboardTmpl.Execute(&buf, buildDashboard(gates, schedules, time.Now())); err != nil {
		s.log.Error("dashboard render failed", "err", err)
		http.Error(w, "kudzu: cannot render dashboard", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(buf.Bytes())
}

// buildDashboard splits gates into blocked and open, sorts gates and freeze
// windows for a stable board across refreshes, and picks the verdict copy.
func buildDashboard(gates []gate.Gate, schedules []gate.ScheduleEntry, now time.Time) uiData {
	slices.SortFunc(gates, func(a, b gate.Gate) int {
		if c := cmp.Compare(a.Service, b.Service); c != 0 {
			return c
		}
		return cmp.Compare(a.Env, b.Env)
	})

	d := uiData{
		Mood:    "open",
		Updated: now.Format("15:04:05 MST"),
	}
	for _, g := range gates {
		row := uiGate{Gate: g}
		if !g.Since.IsZero() {
			row.Age = age(now.Sub(g.Since))
			row.Stamp = g.Since.Format(time.RFC1123)
		}
		if g.ExpiresAt != nil {
			row.Expires = remaining(g.ExpiresAt.Sub(now))
			row.ExpiresStamp = g.ExpiresAt.Format(time.RFC1123)
		}
		if g.Allowed {
			d.Open = append(d.Open, row)
			continue
		}
		d.Blocked = append(d.Blocked, row)
		// A trip is worse than a freeze, so it wins the page accent.
		if g.State == gate.StateTripped {
			d.Mood = "tripped"
		} else if d.Mood != "tripped" {
			d.Mood = "frozen"
		}
	}

	slices.SortFunc(schedules, func(a, b gate.ScheduleEntry) int {
		if c := cmp.Compare(a.Service, b.Service); c != 0 {
			return c
		}
		if c := cmp.Compare(a.Env, b.Env); c != 0 {
			return c
		}
		return cmp.Compare(a.ID, b.ID)
	})
	for _, e := range schedules {
		row := uiSchedule{
			Service: e.Service,
			Env:     e.Env,
			ID:      e.ID,
			Reason:  e.Reason,
			Window:  windowLabel(e.Schedule),
			Active:  e.Active,
		}
		if e.Active && e.Until != nil {
			row.Lifts = remaining(e.Until.Sub(now))
			row.Stamp = e.Until.Format(time.RFC1123)
		}
		d.Schedules = append(d.Schedules, row)
	}

	d.Tracked = fmt.Sprintf("%d gates", len(gates))
	if len(gates) == 1 {
		d.Tracked = "1 gate"
	}

	switch {
	case len(d.Blocked) > 0:
		d.Count = fmt.Sprint(len(d.Blocked))
		d.Verdict = "blocked"
		d.Subtitle = "Queued pull requests for these gates are ejected from the merge queue."
	case len(gates) > 0:
		d.Count = fmt.Sprint(len(gates))
		d.Verdict = "open"
		d.Subtitle = "Nothing frozen, nothing tripped. Every service is free to merge."
	default:
		d.Mood = "empty"
		d.Verdict = "no gates yet"
		d.Subtitle = "A gate appears here the first time it is frozen, scheduled, or a deploy result lands."
	}
	return d
}

// age renders an elapsed duration as a short relative label.
func age(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

// remaining renders time left as a short forward-looking label. A TTL that has
// passed but whose gate is still listed means the board caught it mid-lapse.
func remaining(d time.Duration) string {
	switch {
	case d <= 0:
		return "any moment"
	case d < time.Minute:
		return "in under a minute"
	case d < time.Hour:
		return fmt.Sprintf("in %dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("in %dh", int(d.Hours()))
	default:
		return fmt.Sprintf("in %dd", int(d.Hours()/24))
	}
}

// windowLabel renders a freeze window as a single line: the cron expression and
// how long it holds for a recurring window, or the absolute bounds of a one-off.
func windowLabel(sc schedule.Schedule) string {
	if sc.Start != nil && sc.End != nil {
		const stamp = "02 Jan 2006 15:04 MST"
		return sc.Start.Format(stamp) + " \u2192 " + sc.End.Format(stamp)
	}
	if sc.Cron != "" {
		return sc.Cron + " for " + shortDur(sc.Duration)
	}
	return ""
}

// shortDur renders a window length compactly: "12h", "90m", "45s".
func shortDur(d time.Duration) string {
	switch {
	case d >= time.Hour && d%time.Hour == 0:
		return fmt.Sprintf("%dh", int(d.Hours()))
	case d >= time.Minute:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	default:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
}
