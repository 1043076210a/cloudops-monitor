package main

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

type AlertStateStore interface {
	IncrementCPUBreach(ctx context.Context, machineID string) (int, error)
	GetCPUBreachCount(ctx context.Context, machineID string) (int, error)
	ResetCPUBreach(ctx context.Context, machineID string) error
	TryAcquireCooldown(ctx context.Context, machineID string, ttl time.Duration) (bool, error)
}

type RedisAlertStateStore struct {
	client *redis.Client
}

func NewRedisAlertStateStore(addr, password string, db int) (*RedisAlertStateStore, error) {
	client := redis.NewClient(&redis.Options{
		Addr:     addr,
		Password: password,
		DB:       db,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		return nil, err
	}

	return &RedisAlertStateStore{client: client}, nil
}

func (s *RedisAlertStateStore) IncrementCPUBreach(ctx context.Context, machineID string) (int, error) {
	key := fmt.Sprintf("alert:cpu:count:%s", machineID)
	count, err := s.client.Incr(ctx, key).Result()
	if err != nil {
		return 0, err
	}
	_ = s.client.Expire(ctx, key, time.Hour).Err()
	return int(count), nil
}

func (s *RedisAlertStateStore) GetCPUBreachCount(ctx context.Context, machineID string) (int, error) {
	key := fmt.Sprintf("alert:cpu:count:%s", machineID)
	v, err := s.client.Get(ctx, key).Result()
	if err == redis.Nil {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	n, convErr := strconv.Atoi(v)
	if convErr != nil {
		return 0, convErr
	}
	return n, nil
}

func (s *RedisAlertStateStore) ResetCPUBreach(ctx context.Context, machineID string) error {
	key := fmt.Sprintf("alert:cpu:count:%s", machineID)
	return s.client.Del(ctx, key).Err()
}

func (s *RedisAlertStateStore) TryAcquireCooldown(ctx context.Context, machineID string, ttl time.Duration) (bool, error) {
	cooldownKey := fmt.Sprintf("alert:cpu:cooldown:%s", machineID)
	ok, err := s.client.SetNX(ctx, cooldownKey, "1", ttl).Result()
	if err != nil {
		return false, err
	}
	return ok, nil
}

type MemoryAlertStateStore struct {
	mu        sync.Mutex
	counts    map[string]int
	cooldowns map[string]time.Time
}

func NewMemoryAlertStateStore() *MemoryAlertStateStore {
	return &MemoryAlertStateStore{
		counts:    make(map[string]int),
		cooldowns: make(map[string]time.Time),
	}
}

func (s *MemoryAlertStateStore) IncrementCPUBreach(_ context.Context, machineID string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.counts[machineID]++
	return s.counts[machineID], nil
}

func (s *MemoryAlertStateStore) GetCPUBreachCount(_ context.Context, machineID string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.counts[machineID], nil
}

func (s *MemoryAlertStateStore) ResetCPUBreach(_ context.Context, machineID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.counts, machineID)
	return nil
}

func (s *MemoryAlertStateStore) TryAcquireCooldown(_ context.Context, machineID string, ttl time.Duration) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	expireAt, exists := s.cooldowns[machineID]
	if exists && now.Before(expireAt) {
		return false, nil
	}
	s.cooldowns[machineID] = now.Add(ttl)
	return true, nil
}
