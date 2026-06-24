package schedule

import (
	"testing"
	"time"
)

func tp(t time.Time) *time.Time { return &t }

func TestOneOffWindow(t *testing.T) {
	start := time.Date(2026, 6, 23, 9, 0, 0, 0, time.UTC)
	end := time.Date(2026, 6, 23, 17, 0, 0, 0, time.UTC)
	s := Schedule{ID: "1", Start: tp(start), End: tp(end)}

	if !s.Valid() {
		t.Fatal("expected valid one-off window")
	}
	cases := []struct {
		name string
		now  time.Time
		want bool
	}{
		{"before", start.Add(-time.Minute), false},
		{"at start", start, true},
		{"inside", start.Add(time.Hour), true},
		{"at end (exclusive)", end, false},
		{"after", end.Add(time.Minute), false},
	}
	for _, c := range cases {
		if got := s.IsActiveAt(c.now); got != c.want {
			t.Errorf("%s: IsActiveAt=%v want %v", c.name, got, c.want)
		}
	}
}

func TestRecurringWindow(t *testing.T) {
	// Freeze every Friday from 14:00 for 4 hours.
	s := Schedule{ID: "fri", Cron: "0 14 * * 5", Duration: 4 * time.Hour, Reason: "no Friday deploys"}
	if !s.Valid() {
		t.Fatal("expected valid recurring window")
	}

	friday := func(h, m int) time.Time {
		// 2026-06-26 is a Friday.
		return time.Date(2026, 6, 26, h, m, 0, 0, time.UTC)
	}
	cases := []struct {
		name string
		now  time.Time
		want bool
	}{
		{"friday before window", friday(13, 59), false},
		{"friday at window start", friday(14, 0), true},
		{"friday mid window", friday(16, 30), true},
		{"friday at window end (exclusive)", friday(18, 0), false},
		{"friday after window", friday(18, 1), false},
		{"thursday", time.Date(2026, 6, 25, 15, 0, 0, 0, time.UTC), false},
	}
	for _, c := range cases {
		if got := s.IsActiveAt(c.now); got != c.want {
			t.Errorf("%s: IsActiveAt=%v want %v", c.name, got, c.want)
		}
	}
}

func TestInvalidSchedule(t *testing.T) {
	cases := []Schedule{
		{ID: "empty"},
		{ID: "bad-cron", Cron: "not a cron", Duration: time.Hour},
		{ID: "no-duration", Cron: "0 14 * * 5"},
		{ID: "reversed", Start: tp(time.Now()), End: tp(time.Now().Add(-time.Hour))},
	}
	for _, s := range cases {
		if s.Valid() {
			t.Errorf("%s: expected invalid", s.ID)
		}
	}
}

func TestActiveWindow(t *testing.T) {
	now := time.Date(2026, 6, 26, 15, 0, 0, 0, time.UTC)
	schedules := []Schedule{
		{ID: "daily-standup", Cron: "0 9 * * *", Duration: 30 * time.Minute},
		{ID: "fri-freeze", Cron: "0 14 * * 5", Duration: 4 * time.Hour, Reason: "weekend safety"},
	}
	win, ok := ActiveWindow(schedules, now)
	if !ok || win.ID != "fri-freeze" {
		t.Fatalf("expected fri-freeze active, got %+v ok=%v", win, ok)
	}
}
