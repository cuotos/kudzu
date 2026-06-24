package gate

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/cuotos/kudzu/internal/schedule"
)

// fakeStore is a minimal in-package gate.Store so these tests can set the
// service clock (svc.now) without an import cycle through internal/store.
type fakeStore struct {
	mu        sync.Mutex
	freeze    map[Key]Freeze
	breaker   map[Key]Breaker
	schedules map[Key][]schedule.Schedule
	audit     []AuditEntry
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		freeze:    map[Key]Freeze{},
		breaker:   map[Key]Breaker{},
		schedules: map[Key][]schedule.Schedule{},
	}
}

func (s *fakeStore) GetFreeze(_ context.Context, k Key) (*Freeze, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if f, ok := s.freeze[k]; ok {
		return &f, nil
	}
	return nil, nil
}
func (s *fakeStore) SetFreeze(_ context.Context, k Key, f Freeze) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.freeze[k] = f
	return nil
}
func (s *fakeStore) ClearFreeze(_ context.Context, k Key) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.freeze, k)
	return nil
}
func (s *fakeStore) GetBreaker(_ context.Context, k Key) (*Breaker, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if b, ok := s.breaker[k]; ok {
		return &b, nil
	}
	return nil, nil
}
func (s *fakeStore) SetBreaker(_ context.Context, k Key, b Breaker) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.breaker[k] = b
	return nil
}
func (s *fakeStore) ListSchedules(_ context.Context, k Key) ([]schedule.Schedule, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.schedules[k], nil
}
func (s *fakeStore) AddSchedule(_ context.Context, k Key, sc schedule.Schedule) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.schedules[k] = append(s.schedules[k], sc)
	return nil
}
func (s *fakeStore) DeleteSchedule(_ context.Context, k Key, id string) error { return nil }
func (s *fakeStore) ListKeys(_ context.Context) ([]Key, error) {
	return []Key{{Service: "orders", Env: "production"}}, nil
}
func (s *fakeStore) AppendAudit(_ context.Context, _ Key, e AuditEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.audit = append(s.audit, e)
	return nil
}
func (s *fakeStore) Ping(context.Context) error { return nil }

// fakeEvicter records the calls made to it.
type fakeEvicter struct {
	mu    sync.Mutex
	calls []string
}

func (e *fakeEvicter) Evict(_ context.Context, repo, base, _, _ string) ([]string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls = append(e.calls, repo+"@"+base)
	return []string{"deadbeef"}, nil
}
func (e *fakeEvicter) count() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.calls)
}

var clock = time.Date(2026, 6, 23, 12, 0, 0, 0, time.UTC)

func newTestService(t *testing.T, ev Evicter, cfg Config) (*Service, *fakeStore) {
	t.Helper()
	st := newFakeStore()
	if ev == nil {
		ev = NoopEvicter{}
	}
	svc := NewService(st, ev, cfg, nil)
	svc.now = func() time.Time { return clock }
	// Run eviction synchronously-ish by giving it a real background context.
	return svc, st
}

var k = Key{Service: "orders", Env: "production"}

func TestEffectivePrecedence(t *testing.T) {
	tripped := &Breaker{Tripped: true, Reason: "boom"}
	manual := &Freeze{Reason: "incident", Since: clock}
	win := []schedule.Schedule{{ID: "w", Start: tp(clock.Add(-time.Hour)), End: tp(clock.Add(time.Hour)), Reason: "window"}}

	cases := []struct {
		name      string
		f         *Freeze
		b         *Breaker
		sch       []schedule.Schedule
		wantState State
		wantSrc   Source
	}{
		{"open", nil, nil, nil, StateOpen, ""},
		{"schedule only", nil, nil, win, StateFrozen, SourceSchedule},
		{"manual beats schedule", manual, nil, win, StateFrozen, SourceManual},
		{"trip beats manual", manual, tripped, win, StateTripped, SourceBreaker},
	}
	for _, c := range cases {
		g := Effective(k, c.f, c.b, c.sch, clock)
		if g.State != c.wantState || g.Source != c.wantSrc {
			t.Errorf("%s: state=%s src=%s want state=%s src=%s", c.name, g.State, g.Source, c.wantState, c.wantSrc)
		}
		if (g.State == StateOpen) != g.Allowed {
			t.Errorf("%s: Allowed=%v but state=%s", c.name, g.Allowed, g.State)
		}
	}
}

func tp(t time.Time) *time.Time { return &t }

func TestFreezeUnfreeze(t *testing.T) {
	svc, _ := newTestService(t, nil, Config{})
	ctx := context.Background()

	if g, _ := svc.Get(ctx, k); g.State != StateOpen {
		t.Fatalf("want open, got %s", g.State)
	}
	g, err := svc.Freeze(ctx, k, "release window", "dan", 0)
	if err != nil || g.State != StateFrozen || g.Allowed {
		t.Fatalf("freeze: g=%+v err=%v", g, err)
	}
	g, err = svc.Unfreeze(ctx, k, "dan")
	if err != nil || g.State != StateOpen || !g.Allowed {
		t.Fatalf("unfreeze: g=%+v err=%v", g, err)
	}
}

func TestFreezeTTLExpiry(t *testing.T) {
	svc, _ := newTestService(t, nil, Config{})
	ctx := context.Background()

	if _, err := svc.Freeze(ctx, k, "short", "dan", time.Hour); err != nil {
		t.Fatal(err)
	}
	if g, _ := svc.Get(ctx, k); g.State != StateFrozen {
		t.Fatalf("want frozen within ttl, got %s", g.State)
	}
	// Advance the clock past the TTL.
	svc.now = func() time.Time { return clock.Add(2 * time.Hour) }
	if g, _ := svc.Get(ctx, k); g.State != StateOpen {
		t.Fatalf("want open after ttl, got %s", g.State)
	}
}

func TestBreakerThresholdAndEviction(t *testing.T) {
	ev := &fakeEvicter{}
	svc, _ := newTestService(t, ev, Config{FailureThreshold: 2})
	ctx := context.Background()

	dr := DeployResult{Service: "orders", Env: "production", Status: "failed", Repo: "bw/orders", Base: "main"}

	// First failure: counts but does not trip.
	if g, err := svc.RecordDeploy(ctx, dr); err != nil || g.State != StateOpen {
		t.Fatalf("first failure: g=%+v err=%v", g, err)
	}
	// Second failure: trips and evicts.
	g, err := svc.RecordDeploy(ctx, dr)
	if err != nil || g.State != StateTripped {
		t.Fatalf("second failure: g=%+v err=%v", g, err)
	}

	// Eviction runs in a goroutine; wait briefly for it.
	deadline := time.Now().Add(time.Second)
	for ev.count() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if ev.count() != 1 {
		t.Fatalf("want 1 eviction call, got %d", ev.count())
	}

	// A success resets the counter but the trip stays sticky.
	if g, _ := svc.RecordDeploy(ctx, DeployResult{Service: "orders", Env: "production", Status: "success"}); g.State != StateTripped {
		t.Fatalf("trip should be sticky after success, got %s", g.State)
	}
	// Manual unfreeze clears the trip.
	if g, _ := svc.Unfreeze(ctx, k, "dan"); g.State != StateOpen {
		t.Fatalf("unfreeze should clear trip, got %s", g.State)
	}
}

func TestRecordDeployInvalidStatus(t *testing.T) {
	svc, _ := newTestService(t, nil, Config{})
	_, err := svc.RecordDeploy(context.Background(), DeployResult{Service: "a", Env: "b", Status: "weird"})
	if err == nil {
		t.Fatal("expected error on invalid status")
	}
}

func TestScheduleMakesGateFrozen(t *testing.T) {
	svc, _ := newTestService(t, nil, Config{})
	ctx := context.Background()
	sc := schedule.Schedule{ID: "now", Start: tp(clock.Add(-time.Minute)), End: tp(clock.Add(time.Minute)), Reason: "maint"}
	if err := svc.AddSchedule(ctx, k, sc); err != nil {
		t.Fatal(err)
	}
	g, _ := svc.Get(ctx, k)
	if g.State != StateFrozen || g.Source != SourceSchedule {
		t.Fatalf("want scheduled freeze, got state=%s src=%s", g.State, g.Source)
	}
}
