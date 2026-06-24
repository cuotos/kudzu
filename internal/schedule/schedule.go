// Package schedule evaluates pre-declared freeze windows. A window can be a
// one-off interval ([Start, End)) or a recurring window described by a standard
// 5-field cron expression marking the window start plus a Duration.
package schedule

import (
	"time"

	"github.com/robfig/cron/v3"
)

// Schedule is a single freeze window. Exactly one of (Start+End) or
// (Cron+Duration) should be set.
type Schedule struct {
	ID     string `json:"id"`
	Reason string `json:"reason,omitempty"`

	// Recurring window: cron expression for the window start, held open for Duration.
	Cron     string        `json:"cron,omitempty"`
	Duration time.Duration `json:"duration,omitempty"`

	// One-off window: active while Start <= now < End.
	Start *time.Time `json:"start,omitempty"`
	End   *time.Time `json:"end,omitempty"`
}

var cronParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)

// Valid reports whether the schedule is well-formed (and, for recurring
// windows, that the cron expression parses).
func (s Schedule) Valid() bool {
	switch {
	case s.Start != nil && s.End != nil:
		return s.End.After(*s.Start)
	case s.Cron != "" && s.Duration > 0:
		_, err := cronParser.Parse(s.Cron)
		return err == nil
	default:
		return false
	}
}

// IsActiveAt reports whether the window contains now.
func (s Schedule) IsActiveAt(now time.Time) bool {
	if s.Start != nil && s.End != nil {
		return !now.Before(*s.Start) && now.Before(*s.End)
	}
	if s.Cron != "" && s.Duration > 0 {
		spec, err := cronParser.Parse(s.Cron)
		if err != nil {
			return false
		}
		// Find the latest activation at or before now and check whether the
		// window it opened still contains now. Next() returns the first fire
		// strictly after its argument, so seed it just before (now-Duration).
		t := spec.Next(now.Add(-s.Duration - time.Nanosecond))
		for !t.After(now) { // t <= now
			if now.Before(t.Add(s.Duration)) {
				return true
			}
			t = spec.Next(t)
		}
	}
	return false
}

// ActiveWindow returns the first schedule active at now, if any.
func ActiveWindow(schedules []Schedule, now time.Time) (Schedule, bool) {
	for _, s := range schedules {
		if s.IsActiveAt(now) {
			return s, true
		}
	}
	return Schedule{}, false
}
