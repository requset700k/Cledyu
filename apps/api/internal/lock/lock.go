// Package lock은 유저별 임계영역 직렬화를 위한 락을 제공한다.
// 단일 인스턴스는 in-memory mutex(MemLocker), 다중 레플리카는 Redis 분산 락(RedisLocker)을 쓴다.
package lock

import (
	"context"
	"sync"
)

// Locker는 키 단위 락이다. Acquire 는 release 함수와 획득 성공 여부(ok)를 반환한다.
//   - MemLocker: 같은 키 요청을 블로킹 직렬화한다(ok 는 항상 true).
//   - RedisLocker: 짧게 재시도 후에도 못 잡으면 ok=false(동시 작업 진행 중 — 호출부가 409).
//
// 두 경우 모두 ok=true 면 반드시 release() 를 호출해야 한다.
type Locker interface {
	Acquire(ctx context.Context, key string) (release func(), ok bool)
}

// MemLocker는 키별 sync.Mutex 맵으로 단일 인스턴스 내 직렬화를 제공한다.
// (다중 레플리카에서는 best-effort — 종전 userLocks 와 동일 동작.)
type MemLocker struct {
	mu sync.Mutex
	m  map[string]*sync.Mutex
}

func NewMemLocker() *MemLocker { return &MemLocker{m: make(map[string]*sync.Mutex)} }

func (l *MemLocker) get(key string) *sync.Mutex {
	l.mu.Lock()
	defer l.mu.Unlock()
	mu, ok := l.m[key]
	if !ok {
		mu = &sync.Mutex{}
		l.m[key] = mu
	}
	return mu
}

func (l *MemLocker) Acquire(_ context.Context, key string) (func(), bool) {
	mu := l.get(key)
	mu.Lock()
	return mu.Unlock, true
}
