package vmfiles

import (
	"context"
	"errors"
	"fmt"
	"time"

	"golang.org/x/sync/singleflight"
)

var ErrBusy = errors.New("VM file listing is busy")
var ErrFileNotListed = errors.New("VM file is not listed")

// Lister는 HTTP 핸들러가 의존하는 최소 파일 목록 인터페이스다.
type Lister interface {
	List(context.Context, string) (Snapshot, error)
}

// Runner는 한 세션에서 고정된 VM 파일 목록 명령을 실행한다.
// 범용 실행 경로가 되지 않도록 command나 path 인자를 의도적으로 제공하지 않는다.
type Runner interface {
	Run(context.Context, string) ([]byte, error)
}

// Reader는 목록에 포함된 단일 파일만 읽는 VM forced command 실행 경로다.
type Reader interface {
	Read(context.Context, string, string) ([]byte, error)
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

// Read는 먼저 현재 VM snapshot을 조회해 relativePath가 목록에 포함된 일반 파일인지 확인한 뒤
// preview forced command를 실행한다. 사용자가 추측한 숨김/깊이초과/미목록 경로는 VM으로 보내지 않는다.
func (s *Service) Read(ctx context.Context, sessionID, relativePath string) ([]byte, error) {
	if s == nil || s.runner == nil {
		return nil, errors.New("VM file reading is unavailable")
	}
	reader, ok := s.runner.(Reader)
	if !ok {
		return nil, errors.New("VM file reading is unavailable")
	}
	snapshot, err := s.List(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if !snapshotContainsFile(snapshot, relativePath) {
		return nil, ErrFileNotListed
	}

	// List()의 슬롯은 snapshot 확인이 끝나면 해제된다. preview는 그 뒤 별도 SSH read를
	// 열기 때문에, 파일을 빠르게 여러 개 클릭해도 port-forward/SSH 연결 수가 폭증하지
	// 않도록 read 실행에도 같은 전역 한도를 적용한다.
	release, err := s.acquireLimit()
	if err != nil {
		return nil, err
	}
	defer release()

	runCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	output, err := reader.Read(runCtx, sessionID, relativePath)
	if err != nil {
		return nil, fmt.Errorf("read VM file: %w", err)
	}
	return output, nil
}

func snapshotContainsFile(snapshot Snapshot, relativePath string) bool {
	for _, item := range snapshot.Items {
		if item.Path == relativePath && item.Type == "file" {
			return true
		}
	}
	return false
}

// List는 sessionID의 검증된 파일 목록을 반환한다. 같은 세션의 동시 요청은 하나의
// VM 작업을 공유하고, 서로 다른 세션의 요청은 전역 한도 안에서만 실행한다.
func (s *Service) List(ctx context.Context, sessionID string) (Snapshot, error) {
	// 이미 종료된 HTTP 요청은 singleflight 작업이나 VM 조회 슬롯을 만들지 않는다.
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	if s == nil || s.runner == nil {
		return Snapshot{}, errors.New("VM file listing is unavailable")
	}
	if sessionID == "" {
		return Snapshot{}, errors.New("session ID is required")
	}

	result := s.group.DoChan(sessionID, func() (any, error) {
		release, err := s.acquireLimit()
		if err != nil {
			return Snapshot{}, err
		}
		defer release()

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

// acquireLimit은 VM으로 내려가는 실제 작업 수를 전역으로 제한한다.
// 사용자가 새로고침/파일 클릭을 반복해 한도가 꽉 차면 대기열을 만들지 않고 ErrBusy로 돌려
// HTTP 계층이 429/backoff 가능한 응답을 낼 수 있게 한다.
func (s *Service) acquireLimit() (func(), error) {
	select {
	case s.limit <- struct{}{}:
		return func() { <-s.limit }, nil
	default:
		return nil, ErrBusy
	}
}
