package session

import (
	"context"
	"errors"
	"time"

	"go.uber.org/zap"
)

// Dispatcher는 온프렘(KubeVirt)과 오버플로우(EC2) 두 프로바이더 사이에서 세션을 라우팅한다.
// 자신도 Provider 를 구현하므로 핸들러는 디스패처를 일반 Provider 로 투명하게 사용한다.
//
// 라우팅 정책: 신규 세션은 온프렘 활성 세션 수가 onpremCap 미만이면 온프렘, 도달하면 오버플로우로
// 보낸다(온프렘 우선, 가득 차면 EC2 버스트). 조회/삭제/회수는 두 프로바이더에 걸쳐 동작한다.
type Dispatcher struct {
	onprem    Provider
	overflow  Provider // nil 이면 EC2 오버플로우 비활성 — 온프렘 전용으로 동작한다.
	onpremCap int      // 온프렘 활성 세션 상한. 이 수에 도달하면 오버플로우로 라우팅. 0이면 항상 오버플로우.
	log       *zap.Logger
}

var _ Provider = (*Dispatcher)(nil)

// NewDispatcher는 온프렘 프로바이더(필수)와 오버플로우 프로바이더(nil 허용)로 디스패처를 만든다.
func NewDispatcher(onprem, overflow Provider, onpremCap int, log *zap.Logger) *Dispatcher {
	return &Dispatcher{onprem: onprem, overflow: overflow, onpremCap: onpremCap, log: log}
}

// Create는 용량에 따라 온프렘 또는 오버플로우로 세션 생성을 라우팅한다.
func (d *Dispatcher) Create(ctx context.Context, sessionID, labID, userID string, init BootInit) (*Session, error) {
	if d.overflow == nil {
		return d.onprem.Create(ctx, sessionID, labID, userID, init)
	}
	n, err := d.onprem.CountActiveSessions(ctx)
	if err != nil {
		return nil, err
	}
	if n < d.onpremCap {
		return d.onprem.Create(ctx, sessionID, labID, userID, init)
	}
	if d.log != nil {
		d.log.Info("온프렘 세션 풀 만석 — EC2 오버플로우로 버스트",
			zap.Int("onprem_active", n), zap.Int("onprem_cap", d.onpremCap),
			zap.String("session_id", sessionID), zap.String("lab_id", labID))
	}
	return d.overflow.Create(ctx, sessionID, labID, userID, init)
}

// Get은 온프렘에서 먼저 찾고, 없으면 오버플로우에서 찾는다.
func (d *Dispatcher) Get(ctx context.Context, sessionID string) (*Session, error) {
	sess, err := d.onprem.Get(ctx, sessionID)
	if err == nil {
		return sess, nil
	}
	if d.overflow != nil && errors.Is(err, ErrNotFound) {
		return d.overflow.Get(ctx, sessionID)
	}
	return nil, err
}

// Delete는 온프렘에서 먼저 시도하고, 없으면 오버플로우에서 삭제한다.
func (d *Dispatcher) Delete(ctx context.Context, sessionID string) error {
	err := d.onprem.Delete(ctx, sessionID)
	if d.overflow != nil && errors.Is(err, ErrNotFound) {
		return d.overflow.Delete(ctx, sessionID)
	}
	return err
}

// FindActiveByUser는 두 프로바이더를 통틀어 유저의 활성 세션을 찾는다(단일 세션 제약은 합산 기준).
func (d *Dispatcher) FindActiveByUser(ctx context.Context, userID string) (string, error) {
	id, err := d.onprem.FindActiveByUser(ctx, userID)
	if err != nil {
		return "", err
	}
	if id != "" || d.overflow == nil {
		return id, nil
	}
	return d.overflow.FindActiveByUser(ctx, userID)
}

// Capacity는 배선된 프로바이더의 수용량 합을 반환한다 — 온프렘 cap 에 오버플로우가 있으면 그 cap 을
// 더한다. 핸들러 글로벌 쿼터가 이 값을 쓰면, 오버플로우 미연결 시 온프렘 cap 만 적용된다(과다 허용 방지).
func (d *Dispatcher) Capacity() int {
	if d.overflow == nil {
		return d.onpremCap
	}
	return d.onpremCap + d.overflow.Capacity()
}

// CountActiveSessions는 두 프로바이더의 활성 세션 합을 반환한다(글로벌 쿼터 판정용).
func (d *Dispatcher) CountActiveSessions(ctx context.Context) (int, error) {
	n, err := d.onprem.CountActiveSessions(ctx)
	if err != nil {
		return 0, err
	}
	if d.overflow == nil {
		return n, nil
	}
	m, err := d.overflow.CountActiveSessions(ctx)
	if err != nil {
		return 0, err
	}
	return n + m, nil
}

// ReapStuckSessions는 두 프로바이더의 stuck 세션을 모두 회수한다.
func (d *Dispatcher) ReapStuckSessions(ctx context.Context, timeout time.Duration) ([]string, error) {
	return d.reapBoth(func(p Provider) ([]string, error) { return p.ReapStuckSessions(ctx, timeout) })
}

// ReapExpiredSessions는 두 프로바이더의 만료 세션을 모두 회수한다(EC2 비용 누수 방지 포함).
func (d *Dispatcher) ReapExpiredSessions(ctx context.Context) ([]string, error) {
	return d.reapBoth(func(p Provider) ([]string, error) { return p.ReapExpiredSessions(ctx) })
}

// VMIAddress는 온프렘에서 먼저 찾고, 없으면 오버플로우(tailnet 호스트네임)에서 찾는다.
func (d *Dispatcher) VMIAddress(ctx context.Context, sessionID string) (string, error) {
	addr, err := d.onprem.VMIAddress(ctx, sessionID)
	if err == nil {
		return addr, nil
	}
	if d.overflow != nil && errors.Is(err, ErrNotFound) {
		return d.overflow.VMIAddress(ctx, sessionID)
	}
	return "", err
}

// reapBoth는 회수 동작을 두 프로바이더에 적용하고 회수된 세션 ID 를 합쳐 반환한다.
// 한쪽 오류가 다른 쪽 회수를 막지 않도록 best-effort 로 동작하되, 마지막 오류는 보존해 반환한다.
func (d *Dispatcher) reapBoth(fn func(Provider) ([]string, error)) ([]string, error) {
	reaped, err := fn(d.onprem)
	if d.overflow == nil {
		return reaped, err
	}
	r2, err2 := fn(d.overflow)
	reaped = append(reaped, r2...)
	if err2 != nil {
		return reaped, err2
	}
	return reaped, err
}
