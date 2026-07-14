package memory

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/cuotos/kudzu/internal/gate"
	"github.com/cuotos/kudzu/internal/schedule"
)

var (
	ctx = context.Background()
	k   = gate.Key{Service: "orders", Env: "production"}
)

func TestFreezeLifecycle(t *testing.T) {
	s := New()

	if f, err := s.GetFreeze(ctx, k); err != nil || f != nil {
		t.Fatalf("initial GetFreeze = %+v, %v; want nil,nil", f, err)
	}

	want := gate.Freeze{Reason: "incident", Actor: "dan", Since: time.Unix(1, 0)}
	if err := s.SetFreeze(ctx, k, want); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetFreeze(ctx, k)
	if err != nil || got == nil || got.Reason != "incident" {
		t.Fatalf("GetFreeze = %+v, %v", got, err)
	}

	// The store must hand back a copy, not a pointer into its own map.
	got.Reason = "mutated"
	again, _ := s.GetFreeze(ctx, k)
	if again.Reason != "incident" {
		t.Errorf("mutating a returned Freeze leaked into the store: %q", again.Reason)
	}

	if err := s.ClearFreeze(ctx, k); err != nil {
		t.Fatal(err)
	}
	if f, _ := s.GetFreeze(ctx, k); f != nil {
		t.Errorf("GetFreeze after clear = %+v, want nil", f)
	}
}

func TestBreakerLifecycle(t *testing.T) {
	s := New()
	if b, err := s.GetBreaker(ctx, k); err != nil || b != nil {
		t.Fatalf("initial GetBreaker = %+v, %v; want nil,nil", b, err)
	}
	if err := s.SetBreaker(ctx, k, gate.Breaker{Tripped: true, Fails: 2}); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetBreaker(ctx, k)
	if err != nil || got == nil || !got.Tripped || got.Fails != 2 {
		t.Fatalf("GetBreaker = %+v, %v", got, err)
	}
	got.Fails = 99 // mutate the copy
	again, _ := s.GetBreaker(ctx, k)
	if again.Fails != 2 {
		t.Errorf("mutating a returned Breaker leaked into the store: %d", again.Fails)
	}
}

func TestScheduleLifecycle(t *testing.T) {
	s := New()
	a := schedule.Schedule{ID: "a"}
	b := schedule.Schedule{ID: "b"}
	for _, sc := range []schedule.Schedule{a, b} {
		if err := s.AddSchedule(ctx, k, sc); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.ListSchedules(ctx, k)
	if err != nil || len(got) != 2 {
		t.Fatalf("ListSchedules = %v, %v; want 2 entries", got, err)
	}
	// The returned slice must be a copy: appending to it must not grow the store.
	_ = append(got, schedule.Schedule{ID: "leak"})
	if reGot, _ := s.ListSchedules(ctx, k); len(reGot) != 2 {
		t.Errorf("appending to the returned slice mutated the store: len=%d", len(reGot))
	}

	if err := s.DeleteSchedule(ctx, k, "a"); err != nil {
		t.Fatal(err)
	}
	left, _ := s.ListSchedules(ctx, k)
	if len(left) != 1 || left[0].ID != "b" {
		t.Errorf("after delete = %v, want only b", left)
	}
}

func TestListKeys(t *testing.T) {
	s := New()
	freezeOnly := gate.Key{Service: "a", Env: "prod"}
	breakerOnly := gate.Key{Service: "b", Env: "prod"}
	schedOnly := gate.Key{Service: "c", Env: "prod"}

	_ = s.SetFreeze(ctx, freezeOnly, gate.Freeze{})
	_ = s.SetBreaker(ctx, breakerOnly, gate.Breaker{})
	_ = s.AddSchedule(ctx, schedOnly, schedule.Schedule{ID: "x"})
	// A key whose schedule list was emptied should not be reported.
	emptied := gate.Key{Service: "d", Env: "prod"}
	_ = s.AddSchedule(ctx, emptied, schedule.Schedule{ID: "y"})
	_ = s.DeleteSchedule(ctx, emptied, "y")

	keys, err := s.ListKeys(ctx)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[gate.Key]bool{}
	for _, k := range keys {
		seen[k] = true
	}
	for _, want := range []gate.Key{freezeOnly, breakerOnly, schedOnly} {
		if !seen[want] {
			t.Errorf("ListKeys missing %+v", want)
		}
	}
	if seen[emptied] {
		t.Errorf("ListKeys reported %+v whose schedules are empty", emptied)
	}
}

func TestAuditCap(t *testing.T) {
	s := New()
	for i := range auditCap + 50 {
		if err := s.AppendAudit(ctx, k, gate.AuditEntry{Event: "e", Detail: strconv.Itoa(i)}); err != nil {
			t.Fatal(err)
		}
	}
	s.mu.RLock()
	entries := s.audit[k]
	s.mu.RUnlock()
	if len(entries) != auditCap {
		t.Fatalf("audit len = %d, want capped at %d", len(entries), auditCap)
	}
	// The cap keeps the most recent entries (oldest dropped).
	if entries[len(entries)-1].Detail != strconv.Itoa(auditCap+49) {
		t.Errorf("newest entry = %q, want %q", entries[len(entries)-1].Detail, strconv.Itoa(auditCap+49))
	}
}

func TestPing(t *testing.T) {
	if err := New().Ping(ctx); err != nil {
		t.Errorf("Ping = %v, want nil", err)
	}
}
