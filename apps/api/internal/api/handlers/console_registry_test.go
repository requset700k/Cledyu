package handlers

import (
	"context"
	"sync"
	"testing"
	"time"
)

// consoleRegistry는 세션당 활성 라이브 터미널 연결을 1개로 제한한다(기존 창 우선).
// 두 번째 acquire 는 첫 연결이 release 하기 전까지 false 여야, 호출부가 그 WS 를 정상 종료로 닫는다.
func TestConsoleRegistry_SingleActivePerSession(t *testing.T) {
	r := newConsoleRegistry()

	if !r.acquire("s1") {
		t.Fatal("첫 acquire 는 true 여야 한다")
	}
	if r.acquire("s1") {
		t.Fatal("이미 활성인 세션의 두 번째 acquire 는 false 여야 한다(기존 창 우선)")
	}

	// 다른 세션은 서로 간섭하지 않는다.
	if !r.acquire("s2") {
		t.Fatal("다른 세션의 acquire 는 독립적으로 true 여야 한다")
	}

	// release 후에는 다시 획득 가능해야 한다(창을 닫으면 다음 창이 이어받을 수 있음).
	r.release("s1")
	if !r.acquire("s1") {
		t.Fatal("release 후 acquire 는 다시 true 여야 한다")
	}
}

// 미초기화(테스트가 &Handler{} 리터럴로 만든 경우) nil 레지스트리는 가드를 비활성화해
// panic 없이 통과시켜야 한다 — h.met 등 다른 nil 허용 의존성과 동일한 관용.
func TestConsoleRegistry_NilReceiverIsSafe(t *testing.T) {
	var r *consoleRegistry
	if !r.acquire("s1") {
		t.Fatal("nil 레지스트리 acquire 는 가드 비활성으로 true 여야 한다")
	}
	r.release("s1") // panic 하면 실패
}

// 동시 acquire 는 정확히 하나만 성공해야 한다(더블클릭/두 창 동시 접속의 경합).
func TestConsoleRegistry_ConcurrentAcquireOnlyOneWins(t *testing.T) {
	r := newConsoleRegistry()
	const n = 50
	var wins int64
	var mu sync.Mutex
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if r.acquire("s1") {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		}()
	}
	close(start)
	wg.Wait()
	if wins != 1 {
		t.Fatalf("동시 acquire 중 정확히 1개만 성공해야 한다, got %d", wins)
	}
}

// 슬롯이 비어 있으면 acquireWithin 은 grace 를 기다리지 않고 즉시 true 여야 한다.
func TestConsoleRegistry_AcquireWithin_ImmediateWhenFree(t *testing.T) {
	r := newConsoleRegistry()
	if !r.acquireWithin(context.Background(), "s1", time.Second) {
		t.Fatal("빈 슬롯의 acquireWithin 은 즉시 true 여야 한다")
	}
}

// handoff: 기존 보유자(준비 probe)가 grace 안에 release 하면 새 연결(실제 터미널)은
// 거부되지 않고 슬롯을 인수해야 한다. 이 재시도가 없으면 probe→터미널 경합에서 화면
// 터미널이 close 1000 을 받아 영구히 닫힌다(코드리뷰 P2).
func TestConsoleRegistry_AcquireWithin_SucceedsWhenReleasedDuringGrace(t *testing.T) {
	r := newConsoleRegistry()
	if !r.acquire("s1") {
		t.Fatal("사전 점유 실패")
	}
	go func() {
		time.Sleep(80 * time.Millisecond)
		r.release("s1")
	}()
	start := time.Now()
	if !r.acquireWithin(context.Background(), "s1", 2*time.Second) {
		t.Fatal("grace 안에 release 된 슬롯은 인수되어야 한다(handoff)")
	}
	if elapsed := time.Since(start); elapsed < 50*time.Millisecond {
		t.Fatalf("release 전에 성급히 획득함, elapsed=%v", elapsed)
	}
}

// 기존 보유자가 grace 를 넘겨 계속 점유하면(진짜 두 번째 창) acquireWithin 은 false —
// 호출부가 close 1000 으로 거절해 기존 창 우선을 유지한다.
func TestConsoleRegistry_AcquireWithin_FailsWhenHeldBeyondGrace(t *testing.T) {
	r := newConsoleRegistry()
	if !r.acquire("s1") {
		t.Fatal("사전 점유 실패")
	}
	if r.acquireWithin(context.Background(), "s1", 150*time.Millisecond) {
		t.Fatal("grace 를 넘겨 점유 중이면 acquireWithin 은 false 여야 한다")
	}
}

// 클라이언트가 대기 중 연결을 끊으면(ctx 취소) grace 만료 전에 즉시 포기해야 한다.
func TestConsoleRegistry_AcquireWithin_CtxCancel(t *testing.T) {
	r := newConsoleRegistry()
	if !r.acquire("s1") {
		t.Fatal("사전 점유 실패")
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	if r.acquireWithin(ctx, "s1", 10*time.Second) {
		t.Fatal("ctx 취소 시 acquireWithin 은 false 여야 한다")
	}
}

// nil 리시버는 가드 비활성으로 즉시 통과.
func TestConsoleRegistry_AcquireWithin_NilReceiver(t *testing.T) {
	var r *consoleRegistry
	if !r.acquireWithin(context.Background(), "s1", time.Second) {
		t.Fatal("nil 레지스트리 acquireWithin 은 true 여야 한다")
	}
}
