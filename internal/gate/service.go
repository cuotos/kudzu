package gate

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/cuotos/kudzu/internal/schedule"
)

// ErrInvalidKey is returned when a service/env is missing.
var ErrInvalidKey = errors.New("service and env are required")

// Config tunes the gate service behaviour.
type Config struct {
	// FailureThreshold is the number of consecutive failed deploys that trips
	// the breaker. Defaults to 1.
	FailureThreshold int
	// CheckContext is the commit-status context posted when evicting in-flight
	// merge groups; it must match the required check name (e.g. "kudzu-gate").
	CheckContext string
}

// Service is the gate business logic. It is the only thing handlers talk to.
type Service struct {
	store    Store
	evicter  Evicter
	cfg      Config
	log      *slog.Logger
	now      func() time.Time
	evictCtx func() (context.Context, context.CancelFunc) // background ctx for eviction
}

// NewService builds a Service. evicter may be NoopEvicter{}.
func NewService(store Store, evicter Evicter, cfg Config, log *slog.Logger) *Service {
	if cfg.FailureThreshold < 1 {
		cfg.FailureThreshold = 1
	}
	if cfg.CheckContext == "" {
		cfg.CheckContext = "kudzu-gate"
	}
	if log == nil {
		log = slog.Default()
	}
	return &Service{
		store:   store,
		evicter: evicter,
		cfg:     cfg,
		log:     log,
		now:     time.Now,
		evictCtx: func() (context.Context, context.CancelFunc) {
			return context.WithTimeout(context.Background(), 30*time.Second)
		},
	}
}

// Get returns the effective gate for a key.
func (s *Service) Get(ctx context.Context, k Key) (Gate, error) {
	if !k.valid() {
		return Gate{}, ErrInvalidKey
	}
	f, err := s.store.GetFreeze(ctx, k)
	if err != nil {
		return Gate{}, fmt.Errorf("get freeze: %w", err)
	}
	b, err := s.store.GetBreaker(ctx, k)
	if err != nil {
		return Gate{}, fmt.Errorf("get breaker: %w", err)
	}
	sch, err := s.store.ListSchedules(ctx, k)
	if err != nil {
		return Gate{}, fmt.Errorf("list schedules: %w", err)
	}
	return Effective(k, f, b, sch, s.now()), nil
}

// List returns the effective gate for every known key.
func (s *Service) List(ctx context.Context) ([]Gate, error) {
	keys, err := s.store.ListKeys(ctx)
	if err != nil {
		return nil, fmt.Errorf("list keys: %w", err)
	}
	gates := make([]Gate, 0, len(keys))
	for _, k := range keys {
		g, err := s.Get(ctx, k)
		if err != nil {
			return nil, err
		}
		gates = append(gates, g)
	}
	return gates, nil
}

// Freeze applies a manual freeze. ttl <= 0 means no expiry.
func (s *Service) Freeze(ctx context.Context, k Key, reason, actor string, ttl time.Duration) (Gate, error) {
	if !k.valid() {
		return Gate{}, ErrInvalidKey
	}
	now := s.now()
	f := Freeze{Reason: reason, Actor: actor, Since: now}
	if ttl > 0 {
		exp := now.Add(ttl)
		f.ExpiresAt = &exp
	}
	if err := s.store.SetFreeze(ctx, k, f); err != nil {
		return Gate{}, fmt.Errorf("set freeze: %w", err)
	}
	s.audit(ctx, k, "freeze", actor, reason)
	return s.Get(ctx, k)
}

// Unfreeze clears a manual freeze and resets a tripped breaker.
func (s *Service) Unfreeze(ctx context.Context, k Key, actor string) (Gate, error) {
	if !k.valid() {
		return Gate{}, ErrInvalidKey
	}
	if err := s.store.ClearFreeze(ctx, k); err != nil {
		return Gate{}, fmt.Errorf("clear freeze: %w", err)
	}
	if err := s.store.SetBreaker(ctx, k, Breaker{}); err != nil {
		return Gate{}, fmt.Errorf("reset breaker: %w", err)
	}
	s.audit(ctx, k, "unfreeze", actor, "manual unfreeze + breaker reset")
	return s.Get(ctx, k)
}

// RecordDeploy feeds a deploy result into the circuit breaker. A failure
// increments the consecutive-failure counter and, once the threshold is
// reached, trips the gate and proactively evicts in-flight merge groups. A
// success resets the counter.
func (s *Service) RecordDeploy(ctx context.Context, r DeployResult) (Gate, error) {
	k := Key{Service: r.Service, Env: r.Env}
	if !k.valid() {
		return Gate{}, ErrInvalidKey
	}
	b, err := s.store.GetBreaker(ctx, k)
	if err != nil {
		return Gate{}, fmt.Errorf("get breaker: %w", err)
	}
	cur := Breaker{}
	if b != nil {
		cur = *b
	}

	switch r.Status {
	case "success":
		cur.Fails = 0
		s.audit(ctx, k, "deploy_success", r.Actor, r.SHA)
	case "failed":
		cur.Fails++
		cur.LastSHA, cur.LastRun = r.SHA, r.RunURL
		s.audit(ctx, k, "deploy_failed", r.Actor, fmt.Sprintf("fails=%d sha=%s", cur.Fails, r.SHA))
		if !cur.Tripped && cur.Fails >= s.cfg.FailureThreshold {
			cur.Tripped = true
			cur.Since = s.now()
			cur.Actor = r.Actor
			cur.Reason = fmt.Sprintf("circuit breaker tripped after %d failed deploy(s)", cur.Fails)
			s.audit(ctx, k, "trip", r.Actor, cur.Reason)
			s.evict(k, r)
		}
	default:
		return Gate{}, fmt.Errorf("invalid status %q (want success or failed)", r.Status)
	}

	if err := s.store.SetBreaker(ctx, k, cur); err != nil {
		return Gate{}, fmt.Errorf("set breaker: %w", err)
	}
	return s.Get(ctx, k)
}

// evict runs the GitHub eviction off the request path; failures are logged, not
// fatal — the next merge_group gate check is the backstop.
func (s *Service) evict(k Key, r DeployResult) {
	if r.Repo == "" {
		s.log.Warn("breaker tripped without repo; skipping proactive eviction",
			"service", k.Service, "env", k.Env)
		return
	}
	base := r.Base
	if base == "" {
		base = "main"
	}
	go func() {
		ctx, cancel := s.evictCtx()
		defer cancel()
		desc := fmt.Sprintf("Deploy gate tripped for %s/%s", k.Service, k.Env)
		shas, err := s.evicter.Evict(ctx, r.Repo, base, s.cfg.CheckContext, desc)
		if err != nil {
			s.log.Error("merge-group eviction failed", "repo", r.Repo, "base", base, "err", err)
			return
		}
		for _, sha := range shas {
			s.audit(context.Background(), k, "evict", r.Actor, fmt.Sprintf("%s@%s", r.Repo, sha))
		}
		s.log.Info("evicted in-flight merge groups", "repo", r.Repo, "base", base, "count", len(shas))
	}()
}

// AddSchedule validates and stores a freeze window.
func (s *Service) AddSchedule(ctx context.Context, k Key, sc schedule.Schedule) error {
	if !k.valid() {
		return ErrInvalidKey
	}
	if !sc.Valid() {
		return errors.New("invalid schedule: need a valid cron+duration or start+end window")
	}
	if err := s.store.AddSchedule(ctx, k, sc); err != nil {
		return fmt.Errorf("add schedule: %w", err)
	}
	s.audit(ctx, k, "schedule_add", "", sc.ID)
	return nil
}

// ListSchedules returns the freeze windows for a key.
func (s *Service) ListSchedules(ctx context.Context, k Key) ([]schedule.Schedule, error) {
	if !k.valid() {
		return nil, ErrInvalidKey
	}
	return s.store.ListSchedules(ctx, k)
}

// DeleteSchedule removes a freeze window by id.
func (s *Service) DeleteSchedule(ctx context.Context, k Key, id string) error {
	if !k.valid() {
		return ErrInvalidKey
	}
	if err := s.store.DeleteSchedule(ctx, k, id); err != nil {
		return fmt.Errorf("delete schedule: %w", err)
	}
	s.audit(ctx, k, "schedule_delete", "", id)
	return nil
}

// Ping checks the backing store (used by readiness probes).
func (s *Service) Ping(ctx context.Context) error { return s.store.Ping(ctx) }

func (s *Service) audit(ctx context.Context, k Key, event, actor, detail string) {
	if err := s.store.AppendAudit(ctx, k, AuditEntry{
		Time: s.now(), Event: event, Actor: actor, Detail: detail,
	}); err != nil {
		s.log.Warn("append audit failed", "event", event, "err", err)
	}
}
