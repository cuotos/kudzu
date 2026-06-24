// Package gate holds the deployment-gate domain: the gate state model, the
// rules that compute a gate's effective state, and the Store/Evicter ports the
// service depends on.
package gate

import (
	"context"
	"time"

	"github.com/bloomandwild/kudzu/internal/schedule"
)

// State is the effective state of a gate.
type State string

const (
	// StateOpen means deploys (and therefore merges) are allowed.
	StateOpen State = "open"
	// StateFrozen means deploys are temporarily blocked (manual or scheduled).
	StateFrozen State = "frozen"
	// StateTripped means the circuit breaker fired after a failed deploy.
	StateTripped State = "tripped"
)

// Source identifies what caused a non-open state.
type Source string

const (
	SourceManual   Source = "manual"
	SourceSchedule Source = "schedule"
	SourceBreaker  Source = "breaker"
)

// Key identifies a gate by service and environment.
type Key struct {
	Service string `json:"service"`
	Env     string `json:"env"`
}

// Gate is the effective, computed view of a gate returned to callers.
type Gate struct {
	Service string    `json:"service"`
	Env     string    `json:"env"`
	State   State     `json:"state"`
	Allowed bool      `json:"allowed"`
	Reason  string    `json:"reason,omitempty"`
	Source  Source    `json:"source,omitempty"`
	Since   time.Time `json:"since,omitzero"`
	Actor   string    `json:"actor,omitempty"`
}

// Freeze is a manual freeze record. A nil *Freeze means no manual freeze.
type Freeze struct {
	Reason    string     `json:"reason,omitempty"`
	Actor     string     `json:"actor,omitempty"`
	Since     time.Time  `json:"since"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"` // nil = no TTL
}

// ActiveAt reports whether the manual freeze applies at now. Safe on a nil receiver.
func (f *Freeze) ActiveAt(now time.Time) bool {
	if f == nil {
		return false
	}
	if f.ExpiresAt != nil && !now.Before(*f.ExpiresAt) {
		return false
	}
	return true
}

// Breaker is the circuit-breaker record for a gate.
type Breaker struct {
	Tripped bool      `json:"tripped"`
	Fails   int       `json:"fails"`
	LastSHA string    `json:"last_sha,omitempty"`
	LastRun string    `json:"last_run,omitempty"`
	Reason  string    `json:"reason,omitempty"`
	Actor   string    `json:"actor,omitempty"`
	Since   time.Time `json:"since,omitzero"`
}

// AuditEntry is a single recorded change to a gate.
type AuditEntry struct {
	Time   time.Time `json:"time"`
	Event  string    `json:"event"`
	Actor  string    `json:"actor,omitempty"`
	Detail string    `json:"detail,omitempty"`
}

// DeployResult is the circuit-breaker input reported by a deploy pipeline.
type DeployResult struct {
	Service string `json:"service"`
	Env     string `json:"env"`
	Status  string `json:"status"` // "success" | "failed"
	Repo    string `json:"repo"`   // "owner/name", used for eviction
	Base    string `json:"base"`   // queue base branch, defaults to "main"
	SHA     string `json:"sha,omitempty"`
	RunURL  string `json:"run_url,omitempty"`
	Actor   string `json:"actor,omitempty"`
}

func (k Key) valid() bool { return k.Service != "" && k.Env != "" }

// Store is the persistence port for gate state. Implementations live under
// internal/store. It is defined here (where it is consumed) to keep the
// dependency arrow pointing inward.
type Store interface {
	GetFreeze(ctx context.Context, k Key) (*Freeze, error)
	SetFreeze(ctx context.Context, k Key, f Freeze) error
	ClearFreeze(ctx context.Context, k Key) error

	GetBreaker(ctx context.Context, k Key) (*Breaker, error)
	SetBreaker(ctx context.Context, k Key, b Breaker) error

	ListSchedules(ctx context.Context, k Key) ([]schedule.Schedule, error)
	AddSchedule(ctx context.Context, k Key, s schedule.Schedule) error
	DeleteSchedule(ctx context.Context, k Key, id string) error

	ListKeys(ctx context.Context) ([]Key, error)
	AppendAudit(ctx context.Context, k Key, e AuditEntry) error
	Ping(ctx context.Context) error
}

// Evicter posts a failing status to the in-flight merge-group branches of a
// repo so GitHub evicts them from the queue. Implemented by internal/github;
// NoopEvicter is used when no GitHub App is configured.
type Evicter interface {
	// Evict posts state=failure / the given check context to the head commit
	// of every gh-readonly-queue/<base>/* branch in repo ("owner/name").
	// It returns the SHAs it acted on.
	Evict(ctx context.Context, repo, base, checkContext, description string) ([]string, error)
}

// NoopEvicter does nothing; used when GitHub App credentials are absent.
type NoopEvicter struct{}

func (NoopEvicter) Evict(context.Context, string, string, string, string) ([]string, error) {
	return nil, nil
}

// Effective computes the gate state from its parts using the precedence
// tripped > manual freeze > schedule window > open.
func Effective(k Key, f *Freeze, b *Breaker, schedules []schedule.Schedule, now time.Time) Gate {
	g := Gate{Service: k.Service, Env: k.Env, State: StateOpen, Allowed: true}
	switch {
	case b != nil && b.Tripped:
		g.State, g.Allowed, g.Source = StateTripped, false, SourceBreaker
		g.Reason, g.Since, g.Actor = b.Reason, b.Since, b.Actor
	case f.ActiveAt(now):
		g.State, g.Allowed, g.Source = StateFrozen, false, SourceManual
		g.Reason, g.Since, g.Actor = f.Reason, f.Since, f.Actor
	default:
		if win, ok := schedule.ActiveWindow(schedules, now); ok {
			g.State, g.Allowed, g.Source = StateFrozen, false, SourceSchedule
			g.Reason = win.Reason
			if win.Start != nil {
				g.Since = *win.Start
			}
		}
	}
	return g
}
