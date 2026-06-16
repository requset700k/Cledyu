package lock

import (
	"context"
	"sync"
	"testing"
	"time"
)

// MemLocker는 같은 키 요청을 직렬화한다(임계영역 동시 진입 0).
func TestMemLocker_Serializes(t *testing.T) {
	l := NewMemLocker()
	var inside, maxInside int
	var mu sync.Mutex
	var wg sync.WaitGroup

	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			release, ok := l.Acquire(context.Background(), "user-1")
			if !ok {
				t.Error("MemLocker.Acquire must always succeed")
				return
			}
			mu.Lock()
			inside++
			if inside > maxInside {
				maxInside = inside
			}
			mu.Unlock()

			time.Sleep(time.Millisecond) // 임계영역 점유

			mu.Lock()
			inside--
			mu.Unlock()
			release()
		}()
	}
	wg.Wait()

	if maxInside != 1 {
		t.Errorf("expected at most 1 goroutine in critical section, saw %d", maxInside)
	}
}

// 다른 키는 동시에 진입 가능하다(키별 독립 락).
func TestMemLocker_DifferentKeysConcurrent(t *testing.T) {
	l := NewMemLocker()
	r1, ok1 := l.Acquire(context.Background(), "a")
	r2, ok2 := l.Acquire(context.Background(), "b") // 블로킹 없이 즉시 획득돼야 함
	if !ok1 || !ok2 {
		t.Fatal("different keys must be independently lockable")
	}
	r1()
	r2()
}
