package observability

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/cuotos/kudzu/internal/gate"
)

// fakeLister is a stand-in for gate.Service in the gate-state collector tests.
type fakeLister struct {
	gates []gate.Gate
	err   error
}

func (f fakeLister) List(context.Context) ([]gate.Gate, error) { return f.gates, f.err }

func TestStatusClass(t *testing.T) {
	cases := []struct {
		code int
		want string
	}{
		{200, "2xx"}, {204, "2xx"}, {301, "3xx"}, {404, "4xx"}, {422, "4xx"}, {500, "5xx"}, {503, "5xx"},
	}
	for _, c := range cases {
		if got := statusClass(c.code); got != c.want {
			t.Errorf("statusClass(%d) = %q, want %q", c.code, got, c.want)
		}
	}
}

func TestObserveCountsRequests(t *testing.T) {
	m := New(fakeLister{}, nil)
	m.Observe("GET", "/v1/gate", 200, 5*time.Millisecond)
	m.Observe("GET", "/v1/gate", 503, 7*time.Millisecond)

	if got := testutil.ToFloat64(m.reqs.WithLabelValues("GET", "/v1/gate", "2xx")); got != 1 {
		t.Errorf("2xx count = %v, want 1", got)
	}
	if got := testutil.ToFloat64(m.reqs.WithLabelValues("GET", "/v1/gate", "5xx")); got != 1 {
		t.Errorf("5xx count = %v, want 1", got)
	}
	// One observation per labelled route in the duration histogram.
	if got := testutil.CollectAndCount(m.dur); got == 0 {
		t.Error("duration histogram recorded no series")
	}
}

func TestGateCollector(t *testing.T) {
	lister := fakeLister{gates: []gate.Gate{
		{Service: "orders", Env: "production", State: gate.StateOpen, Allowed: true},
		{Service: "web", Env: "staging", State: gate.StateFrozen, Allowed: false},
	}}
	c := newGateCollector(lister, nil)

	want := `
# HELP kudzu_gate_allowed 1 if the gate is open (deploys allowed), 0 otherwise.
# TYPE kudzu_gate_allowed gauge
kudzu_gate_allowed{env="production",service="orders"} 1
kudzu_gate_allowed{env="staging",service="web"} 0
# HELP kudzu_gate_state Current gate state, 1 for the active state.
# TYPE kudzu_gate_state gauge
kudzu_gate_state{env="production",service="orders",state="open"} 1
kudzu_gate_state{env="staging",service="web",state="frozen"} 1
`
	if err := testutil.CollectAndCompare(c, strings.NewReader(want)); err != nil {
		t.Errorf("collected metrics differ:\n%v", err)
	}
}

func TestGateCollectorListError(t *testing.T) {
	c := newGateCollector(fakeLister{err: errors.New("redis down")}, nil)
	if got := testutil.CollectAndCount(c); got != 0 {
		t.Errorf("emitted %d metrics on list error, want 0", got)
	}
}
