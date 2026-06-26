package vmfiles

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

type runnerFunc func(context.Context, string) ([]byte, error)

func (f runnerFunc) Run(ctx context.Context, sessionID string) ([]byte, error) {
	return f(ctx, sessionID)
}

var emptySnapshot = []byte(`{"root":"/home/lab","items":[],"truncated":false}`)
var snapshotWithFile = []byte(`{"root":"/home/lab","items":[{"path":"work","name":"work","type":"directory","depth":1},{"path":"work/app.log","name":"app.log","type":"file","depth":2}],"truncated":false}`)

type runnerWithRead struct {
	list      []byte
	readCalls atomic.Int32
}

func (r *runnerWithRead) Run(context.Context, string) ([]byte, error) {
	return r.list, nil
}

func (r *runnerWithRead) Read(_ context.Context, sessionID, relativePath string) ([]byte, error) {
	r.readCalls.Add(1)
	if sessionID != "abc123" {
		return nil, errors.New("unexpected session")
	}
	if relativePath != "work/app.log" {
		return nil, errors.New("unexpected path")
	}
	return []byte(`{"path":"work/app.log","content":"hello\n","truncated":false}` + "\n"), nil
}

func TestServiceCoalescesConcurrentRequestsForSameSession(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	runner := runnerFunc(func(ctx context.Context, sessionID string) ([]byte, error) {
		if calls.Add(1) == 1 {
			close(started)
		}
		select {
		case <-release:
			return emptySnapshot, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	})
	service := NewService(runner, time.Second, 2)

	first := make(chan error, 1)
	second := make(chan error, 1)
	go func() {
		_, err := service.List(context.Background(), "abc123")
		first <- err
	}()
	<-started
	go func() {
		_, err := service.List(context.Background(), "abc123")
		second <- err
	}()
	time.Sleep(10 * time.Millisecond)
	close(release)

	if err := <-first; err != nil {
		t.Fatalf("first List() error = %v", err)
	}
	if err := <-second; err != nil {
		t.Fatalf("second List() error = %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("runner calls = %d, want 1", got)
	}
}

func TestServiceRejectsDifferentSessionWhenConcurrencyIsFull(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	runner := runnerFunc(func(ctx context.Context, sessionID string) ([]byte, error) {
		close(started)
		select {
		case <-release:
			return emptySnapshot, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	})
	service := NewService(runner, time.Second, 1)

	first := make(chan error, 1)
	go func() {
		_, err := service.List(context.Background(), "first")
		first <- err
	}()
	<-started

	if _, err := service.List(context.Background(), "second"); !errors.Is(err, ErrBusy) {
		t.Fatalf("second List() error = %v, want ErrBusy", err)
	}
	close(release)
	if err := <-first; err != nil {
		t.Fatalf("first List() error = %v", err)
	}
}

func TestServiceAppliesRunnerTimeout(t *testing.T) {
	runner := runnerFunc(func(ctx context.Context, sessionID string) ([]byte, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	service := NewService(runner, 20*time.Millisecond, 1)

	if _, err := service.List(context.Background(), "abc123"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("List() error = %v, want context deadline exceeded", err)
	}
}

func TestServiceDoesNotStartRunnerForAlreadyCancelledContext(t *testing.T) {
	called := make(chan struct{}, 1)
	runner := runnerFunc(func(ctx context.Context, sessionID string) ([]byte, error) {
		called <- struct{}{}
		return emptySnapshot, nil
	})
	service := NewService(runner, time.Second, 1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := service.List(ctx, "abc123"); !errors.Is(err, context.Canceled) {
		t.Fatalf("List() error = %v, want context canceled", err)
	}
	select {
	case <-called:
		t.Fatal("runner was called for an already cancelled context")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestServiceReadAllowsOnlyListedFiles(t *testing.T) {
	runner := &runnerWithRead{list: snapshotWithFile}
	service := NewService(runner, time.Second, 1)

	got, err := service.Read(context.Background(), "abc123", "work/app.log")
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if string(got) != `{"path":"work/app.log","content":"hello\n","truncated":false}`+"\n" {
		t.Fatalf("Read() output = %q", got)
	}
	if got := runner.readCalls.Load(); got != 1 {
		t.Fatalf("read calls = %d, want 1", got)
	}
}

func TestServiceReadRejectsUnlistedPathBeforeRunnerRead(t *testing.T) {
	runner := &runnerWithRead{list: snapshotWithFile}
	service := NewService(runner, time.Second, 1)

	if _, err := service.Read(context.Background(), "abc123", "work/missing.log"); err == nil {
		t.Fatal("Read() error = nil, want unlisted path error")
	}
	if got := runner.readCalls.Load(); got != 0 {
		t.Fatalf("read calls = %d, want 0", got)
	}
}

func TestServiceReadRejectsListedDirectoryBeforeRunnerRead(t *testing.T) {
	runner := &runnerWithRead{list: snapshotWithFile}
	service := NewService(runner, time.Second, 1)

	if _, err := service.Read(context.Background(), "abc123", "work"); err == nil {
		t.Fatal("Read() error = nil, want directory rejection")
	}
	if got := runner.readCalls.Load(); got != 0 {
		t.Fatalf("read calls = %d, want 0", got)
	}
}
