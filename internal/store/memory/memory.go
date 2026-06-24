// Package memory is an in-memory gate.Store for tests and local single-replica
// use. State is lost on restart and not shared across processes.
package memory

import (
	"context"
	"sync"

	"github.com/cuotos/kudzu/internal/gate"
	"github.com/cuotos/kudzu/internal/schedule"
)

const auditCap = 100

// Store is a concurrency-safe in-memory implementation of gate.Store.
type Store struct {
	mu        sync.RWMutex
	freezes   map[gate.Key]gate.Freeze
	breakers  map[gate.Key]gate.Breaker
	schedules map[gate.Key][]schedule.Schedule
	audit     map[gate.Key][]gate.AuditEntry
}

// New returns an empty in-memory store.
func New() *Store {
	return &Store{
		freezes:   map[gate.Key]gate.Freeze{},
		breakers:  map[gate.Key]gate.Breaker{},
		schedules: map[gate.Key][]schedule.Schedule{},
		audit:     map[gate.Key][]gate.AuditEntry{},
	}
}

var _ gate.Store = (*Store)(nil)

func (s *Store) GetFreeze(_ context.Context, k gate.Key) (*gate.Freeze, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if f, ok := s.freezes[k]; ok {
		cp := f
		return &cp, nil
	}
	return nil, nil
}

func (s *Store) SetFreeze(_ context.Context, k gate.Key, f gate.Freeze) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.freezes[k] = f
	return nil
}

func (s *Store) ClearFreeze(_ context.Context, k gate.Key) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.freezes, k)
	return nil
}

func (s *Store) GetBreaker(_ context.Context, k gate.Key) (*gate.Breaker, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if b, ok := s.breakers[k]; ok {
		cp := b
		return &cp, nil
	}
	return nil, nil
}

func (s *Store) SetBreaker(_ context.Context, k gate.Key, b gate.Breaker) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.breakers[k] = b
	return nil
}

func (s *Store) ListSchedules(_ context.Context, k gate.Key) ([]schedule.Schedule, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]schedule.Schedule(nil), s.schedules[k]...), nil
}

func (s *Store) AddSchedule(_ context.Context, k gate.Key, sc schedule.Schedule) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.schedules[k] = append(s.schedules[k], sc)
	return nil
}

func (s *Store) DeleteSchedule(_ context.Context, k gate.Key, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.schedules[k][:0]
	for _, sc := range s.schedules[k] {
		if sc.ID != id {
			out = append(out, sc)
		}
	}
	s.schedules[k] = out
	return nil
}

func (s *Store) ListKeys(_ context.Context) ([]gate.Key, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	seen := map[gate.Key]struct{}{}
	for k := range s.freezes {
		seen[k] = struct{}{}
	}
	for k := range s.breakers {
		seen[k] = struct{}{}
	}
	for k := range s.schedules {
		if len(s.schedules[k]) > 0 {
			seen[k] = struct{}{}
		}
	}
	keys := make([]gate.Key, 0, len(seen))
	for k := range seen {
		keys = append(keys, k)
	}
	return keys, nil
}

func (s *Store) AppendAudit(_ context.Context, k gate.Key, e gate.AuditEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries := append(s.audit[k], e)
	if len(entries) > auditCap {
		entries = entries[len(entries)-auditCap:]
	}
	s.audit[k] = entries
	return nil
}

func (s *Store) Ping(context.Context) error { return nil }
