package handlers

import (
	"sync"
	"testing"
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
