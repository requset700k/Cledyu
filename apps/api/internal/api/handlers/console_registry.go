package handlers

import "sync"

// consoleRegistry는 세션당 활성 라이브 터미널 연결을 최대 1개로 제한한다(기존 창 우선).
//
// KubeVirt serial console(ttyS0)은 VM당 배타 접속이라, 같은 세션에 두 번째 연결이 붙으면
// 첫 번째가 강제로 끊긴다. 같은 계정으로 두 브라우저 창을 열고 랩에 접속하면 두 콘솔이
// 서로를 축출하고, 클라이언트는 비정상 종료를 재연결로 처리해(runtime-api-origin.mjs
// shouldReconnect) 두 창이 무한 재연결 핑퐁에 빠진다. acquire 로 세션당 한 연결만
// 통과시켜 이 루프를 원천 차단한다.
type consoleRegistry struct {
	mu     sync.Mutex
	active map[string]struct{}
}

func newConsoleRegistry() *consoleRegistry {
	return &consoleRegistry{active: make(map[string]struct{})}
}

// acquire는 sessionID에 활성 콘솔이 없을 때만 슬롯을 점유하고 true를 돌려준다.
// 이미 활성이면 false — 호출부는 새 연결을 정상 종료(close 1000)로 닫아 재연결을 막는다.
// nil 리시버는 가드 비활성(테스트가 &Handler{} 리터럴로 만든 경우)으로 항상 통과시킨다.
func (r *consoleRegistry) acquire(sessionID string) bool {
	if r == nil {
		return true
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.active[sessionID]; ok {
		return false
	}
	r.active[sessionID] = struct{}{}
	return true
}

// release는 연결 종료 시 슬롯을 반납해 다음 창이 이어받을 수 있게 한다. nil 리시버는 no-op.
func (r *consoleRegistry) release(sessionID string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.active, sessionID)
}
