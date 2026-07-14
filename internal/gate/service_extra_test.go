package gate

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/cuotos/kudzu/internal/schedule"
)

func TestInvalidKeyErrors(t *testing.T) {
	svc, _ := newTestService(t, nil, Config{})
	ctx := context.Background()
	bad := Key{} // missing service + env

	ops := map[string]func() error{
		"Get":            func() error { _, err := svc.Get(ctx, bad); return err },
		"Freeze":         func() error { _, err := svc.Freeze(ctx, bad, "r", "a", 0); return err },
		"Unfreeze":       func() error { _, err := svc.Unfreeze(ctx, bad, "a"); return err },
		"RecordDeploy":   func() error { _, err := svc.RecordDeploy(ctx, DeployResult{Status: "success"}); return err },
		"AddSchedule":    func() error { return svc.AddSchedule(ctx, bad, schedule.Schedule{ID: "x"}) },
		"ListSchedules":  func() error { _, err := svc.ListSchedules(ctx, bad); return err },
		"DeleteSchedule": func() error { return svc.DeleteSchedule(ctx, bad, "x") },
	}
	for name, op := range ops {
		if err := op(); !errors.Is(err, ErrInvalidKey) {
			t.Errorf("%s: err = %v, want ErrInvalidKey", name, err)
		}
	}
}

func TestAddScheduleInvalid(t *testing.T) {
	svc, _ := newTestService(t, nil, Config{})
	// Neither a valid one-off window nor a valid cron+duration.
	err := svc.AddSchedule(context.Background(), k, schedule.Schedule{ID: "bad", Cron: "not a cron"})
	if err == nil {
		t.Fatal("expected error for an invalid schedule")
	}
	if errors.Is(err, ErrInvalidKey) {
		t.Fatalf("got ErrInvalidKey, want a schedule-validation error: %v", err)
	}
}

func TestListReturnsEffectiveGates(t *testing.T) {
	svc, _ := newTestService(t, nil, Config{})
	ctx := context.Background()
	// fakeStore.ListKeys always reports orders/production; freeze it.
	if _, err := svc.Freeze(ctx, k, "incident", "dan", 0); err != nil {
		t.Fatal(err)
	}
	gates, err := svc.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(gates) != 1 || gates[0].State != StateFrozen {
		t.Fatalf("List = %+v, want one frozen gate", gates)
	}
}

func TestTripWithoutRepoSkipsEviction(t *testing.T) {
	ev := &fakeEvicter{}
	svc, _ := newTestService(t, ev, Config{FailureThreshold: 1})
	// Repo omitted: the breaker still trips, but eviction is skipped (logged).
	g, err := svc.RecordDeploy(context.Background(),
		DeployResult{Service: "orders", Env: "production", Status: "failed"})
	if err != nil || g.State != StateTripped {
		t.Fatalf("RecordDeploy = %+v, %v; want tripped", g, err)
	}
	// Give any (erroneous) eviction goroutine a chance to run.
	time.Sleep(20 * time.Millisecond)
	if ev.count() != 0 {
		t.Errorf("eviction called %d times without a repo, want 0", ev.count())
	}
}

func TestPing(t *testing.T) {
	svc, _ := newTestService(t, nil, Config{})
	if err := svc.Ping(context.Background()); err != nil {
		t.Errorf("Ping = %v, want nil", err)
	}
}
