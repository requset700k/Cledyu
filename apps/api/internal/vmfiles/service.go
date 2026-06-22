package vmfiles

import (
	"context"
	"errors"
	"fmt"
	"time"

	"golang.org/x/sync/singleflight"
)

var ErrBusy = errors.New("VM file listing is busy")

// Lister는 HTTP 핸들러가 의존하는 최소 파일 목록 인터페이스다.
type Lister interface {
	List(context.Context, string) (Snapshot, error)
}

// Runner는 한 세션에서 고정된 VM 파일 목록 명령을 실행한다.
// 범용 실행 경로가 되지 않도록 command나 path 인자를 의도적으로 제공하지 않는다.
type Runner interface {
	Run(context.Context, string) ([]byte, error)
}

// Service는 Runner에 중복 요청 병합, 전역 동시성 제한, timeout 경계를 적용한다.
type Service struct {
	runner  Runner
	timeout time.Duration
	limit   chan struct{}
	group   singleflight.Group
}

func NewService(runner Runner, timeout time.Duration, maxConcurrent int) *Service {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	if maxConcurrent <= 0 {
		maxConcurrent = 1
	}
	return &Service{
		runner:  runner,
		timeout: timeout,
		limit:   make(chan struct{}, maxConcurrent),
	}
}

// List는 sessionID의 검증된 파일 목록을 반환한다. 같은 세션의 동시 요청은 하나의
// VM 작업을 공유하고, 서로 다른 세션의 요청은 전역 한도 안에서만 실행한다.
func (s *Service) List(ctx context.Context, sessionID string) (Snapshot, error) {
	if s == nil || s.runner == nil {
		return Snapshot{}, errors.New("VM file listing is unavailable")
	}
	if sessionID == "" {
		return Snapshot{}, errors.New("session ID is required")
	}

	result := s.group.DoChan(sessionID, func() (any, error) {
		select {
		case s.limit <- struct{}{}:
			defer func() { <-s.limit }()
		default:
			return Snapshot{}, ErrBusy
		}

		// 첫 요청자가 연결을 끊더라도 공유 작업까지 취소하지 않는다. 작업 자체의 짧은
		// timeout은 유지하므로 남아 있는 다른 요청자에게 같은 결과를 제공할 수 있다.
		runCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.timeout)
		defer cancel()
		raw, err := s.runner.Run(runCtx, sessionID)
		if err != nil {
			return Snapshot{}, fmt.Errorf("list VM files: %w", err)
		}
		return ParseSnapshot(raw)
	})

	select {
	case <-ctx.Done():
		return Snapshot{}, ctx.Err()
	case res := <-result:
		if res.Err != nil {
			return Snapshot{}, res.Err
		}
		snapshot, ok := res.Val.(Snapshot)
		if !ok {
			return Snapshot{}, errors.New("unexpected VM file listing result")
		}
		return snapshot, nil
	}
}
