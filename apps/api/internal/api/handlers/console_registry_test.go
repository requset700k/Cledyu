package handlers

import (
	"sync"
	"testing"
	"time"
)

// fakeConsoleConn은 consoleRegistry가 이전 홀더에 보낸 정상 종료(supersede) 통보를 센다.
type fakeConsoleConn struct {
	mu        sync.Mutex
	supersede int
}

func (f *fakeConsoleConn) WriteControl(int, []byte, time.Time) error {
	f.mu.Lock()
	f.supersede++
	f.mu.Unlock()
	return nil
}

func (f *fakeConsoleConn) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.supersede
}

// last-wins: 두 번째 연결이 붙으면 첫 번째 홀더가 supersede(정상 종료) 통보를 받아야 한다.
func TestConsoleRegistry_LastWinsSupersedesPrevious(t *testing.T) {
	r := newConsoleRegistry()
	a, b := &fakeConsoleConn{}, &fakeConsoleConn{}

	relA := r.acquire("s1", a)
	if a.count() != 0 {
		t.Fatal("첫 연결은 supersede 통보를 받지 않아야 한다")
	}

	relB := r.acquire("s1", b)
	if a.count() != 1 {
		t.Fatal("두 번째 연결이 붙으면 첫 홀더는 supersede 통보를 받아야 한다(last-wins)")
	}
	if b.count() != 0 {
		t.Fatal("새 홀더는 자기 자신을 supersede 하지 않는다")
	}

	_ = relA
	_ = relB
}

// compare-and-delete: 인수당한 옛 홀더의 release는 새 홀더의 등록을 지우면 안 된다.
// 지워버리면 다음 연결이 활성 홀더를 인식 못 해 supersede 통보가 누락된다.
func TestConsoleRegistry_StaleReleaseDoesNotEvictNewHolder(t *testing.T) {
	r := newConsoleRegistry()
	a, b, c := &fakeConsoleConn{}, &fakeConsoleConn{}, &fakeConsoleConn{}

	relA := r.acquire("s1", a) // active=a
	_ = r.acquire("s1", b)     // active=b, a superseded

	relA() // 옛 홀더 a의 release — active(b)를 지우면 안 된다

	// c가 붙으면 여전히 활성인 b가 supersede 통보를 받아야 한다.
	r.acquire("s1", c)
	if b.count() != 1 {
		t.Fatal("옛 홀더 release 후에도 활성 홀더(b)가 유지되어 supersede 통보를 받아야 한다")
	}
}

// 정상 release 후에는 슬롯이 비어, 다음 acquire가 아무도 supersede 하지 않아야 한다.
func TestConsoleRegistry_ReleaseFreesSlot(t *testing.T) {
	r := newConsoleRegistry()
	a, b := &fakeConsoleConn{}, &fakeConsoleConn{}

	relA := r.acquire("s1", a)
	relA() // active=a 를 정상 반납

	r.acquire("s1", b)
	if b.count() != 0 || a.count() != 0 {
		t.Fatalf("빈 슬롯 acquire 는 supersede 가 없어야 한다, a=%d b=%d", a.count(), b.count())
	}
}

// 다른 세션은 서로 간섭하지 않는다.
func TestConsoleRegistry_SessionsIndependent(t *testing.T) {
	r := newConsoleRegistry()
	a, b := &fakeConsoleConn{}, &fakeConsoleConn{}
	r.acquire("s1", a)
	r.acquire("s2", b)
	if a.count() != 0 || b.count() != 0 {
		t.Fatal("서로 다른 세션의 acquire 는 supersede 를 유발하지 않아야 한다")
	}
}

// nil 리시버(테스트가 &Handler{} 리터럴로 만든 경우)는 가드 비활성으로 no-op release 를 돌려준다.
func TestConsoleRegistry_NilReceiverIsSafe(t *testing.T) {
	var r *consoleRegistry
	release := r.acquire("s1", &fakeConsoleConn{})
	release() // panic 하면 실패
}

// 동시 acquire: 마지막 하나만 활성으로 남고 나머지는 각각 한 번씩 supersede 통보를 받아야 한다.
// 즉 supersede 총합은 (N-1) 이어야 한다(정확히 하나의 승자).
func TestConsoleRegistry_ConcurrentLastWins(t *testing.T) {
	r := newConsoleRegistry()
	const n = 50
	conns := make([]*fakeConsoleConn, n)
	for i := range conns {
		conns[i] = &fakeConsoleConn{}
	}

	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-start
			r.acquire("s1", conns[idx])
		}(i)
	}
	close(start)
	wg.Wait()

	total := 0
	for _, c := range conns {
		total += c.count()
	}
	if total != n-1 {
		t.Fatalf("동시 last-wins supersede 총합은 %d 여야 한다, got %d", n-1, total)
	}
}
