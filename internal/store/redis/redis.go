// Package redis implements gate.Store on top of Redis. State is small and
// JSON-encoded per (service, environment); a set tracks known keys so the
// service can enumerate every gate.
package redis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	goredis "github.com/redis/go-redis/v9"

	"github.com/cuotos/kudzu/internal/gate"
	"github.com/cuotos/kudzu/internal/schedule"
)

const (
	prefix   = "kudzu"
	keySep   = "\x1f" // unit separator, safe inside the keys set member
	auditCap = 100
	keysSet  = prefix + ":keys"
)

// Store is a Redis-backed gate.Store.
type Store struct {
	rdb *goredis.Client
}

// New wraps an existing go-redis client.
func New(rdb *goredis.Client) *Store { return &Store{rdb: rdb} }

var _ gate.Store = (*Store)(nil)

func freezeKey(k gate.Key) string { return fmt.Sprintf("%s:freeze:%s:%s", prefix, k.Service, k.Env) }
func breakerKey(k gate.Key) string {
	return fmt.Sprintf("%s:breaker:%s:%s", prefix, k.Service, k.Env)
}
func schedKey(k gate.Key) string { return fmt.Sprintf("%s:sched:%s:%s", prefix, k.Service, k.Env) }
func auditKey(k gate.Key) string { return fmt.Sprintf("%s:audit:%s:%s", prefix, k.Service, k.Env) }

func (s *Store) registerKey(ctx context.Context, k gate.Key) error {
	return s.rdb.SAdd(ctx, keysSet, k.Service+keySep+k.Env).Err()
}

func (s *Store) GetFreeze(ctx context.Context, k gate.Key) (*gate.Freeze, error) {
	raw, err := s.rdb.Get(ctx, freezeKey(k)).Bytes()
	if errors.Is(err, goredis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var f gate.Freeze
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, err
	}
	return &f, nil
}

func (s *Store) SetFreeze(ctx context.Context, k gate.Key, f gate.Freeze) error {
	raw, err := json.Marshal(f)
	if err != nil {
		return err
	}
	if err := s.rdb.Set(ctx, freezeKey(k), raw, 0).Err(); err != nil {
		return err
	}
	return s.registerKey(ctx, k)
}

func (s *Store) ClearFreeze(ctx context.Context, k gate.Key) error {
	return s.rdb.Del(ctx, freezeKey(k)).Err()
}

func (s *Store) GetBreaker(ctx context.Context, k gate.Key) (*gate.Breaker, error) {
	raw, err := s.rdb.Get(ctx, breakerKey(k)).Bytes()
	if errors.Is(err, goredis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var b gate.Breaker
	if err := json.Unmarshal(raw, &b); err != nil {
		return nil, err
	}
	return &b, nil
}

func (s *Store) SetBreaker(ctx context.Context, k gate.Key, b gate.Breaker) error {
	raw, err := json.Marshal(b)
	if err != nil {
		return err
	}
	if err := s.rdb.Set(ctx, breakerKey(k), raw, 0).Err(); err != nil {
		return err
	}
	return s.registerKey(ctx, k)
}

func (s *Store) ListSchedules(ctx context.Context, k gate.Key) ([]schedule.Schedule, error) {
	vals, err := s.rdb.HVals(ctx, schedKey(k)).Result()
	if err != nil {
		return nil, err
	}
	out := make([]schedule.Schedule, 0, len(vals))
	for _, v := range vals {
		var sc schedule.Schedule
		if err := json.Unmarshal([]byte(v), &sc); err != nil {
			return nil, err
		}
		out = append(out, sc)
	}
	return out, nil
}

func (s *Store) AddSchedule(ctx context.Context, k gate.Key, sc schedule.Schedule) error {
	raw, err := json.Marshal(sc)
	if err != nil {
		return err
	}
	if err := s.rdb.HSet(ctx, schedKey(k), sc.ID, raw).Err(); err != nil {
		return err
	}
	return s.registerKey(ctx, k)
}

func (s *Store) DeleteSchedule(ctx context.Context, k gate.Key, id string) error {
	return s.rdb.HDel(ctx, schedKey(k), id).Err()
}

func (s *Store) ListKeys(ctx context.Context) ([]gate.Key, error) {
	members, err := s.rdb.SMembers(ctx, keysSet).Result()
	if err != nil {
		return nil, err
	}
	keys := make([]gate.Key, 0, len(members))
	for _, m := range members {
		svc, env, ok := strings.Cut(m, keySep)
		if !ok {
			continue
		}
		keys = append(keys, gate.Key{Service: svc, Env: env})
	}
	return keys, nil
}

func (s *Store) AppendAudit(ctx context.Context, k gate.Key, e gate.AuditEntry) error {
	raw, err := json.Marshal(e)
	if err != nil {
		return err
	}
	pipe := s.rdb.TxPipeline()
	pipe.LPush(ctx, auditKey(k), raw)
	pipe.LTrim(ctx, auditKey(k), 0, auditCap-1)
	_, err = pipe.Exec(ctx)
	return err
}

func (s *Store) Ping(ctx context.Context) error {
	return s.rdb.Ping(ctx).Err()
}
