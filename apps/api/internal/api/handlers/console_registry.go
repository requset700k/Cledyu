package handlers

import (
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// consoleConn은 consoleRegistry가 이전 홀더에게 정상 종료를 통보하는 데 필요한 최소 동작이다.
// *websocket.Conn이 이를 만족한다. 인터페이스로 두어 레지스트리를 실제 소켓 없이 단위 테스트한다.
// gorilla의 WriteControl은 다른 메서드와 동시 호출이 안전하므로, 인수한 새 연결의 goroutine에서
// 이전 홀더 소켓에 호출해도 문제없다.
type consoleConn interface {
	WriteControl(messageType int, data []byte, deadline time.Time) error
}

// consoleRegistry는 세션당 활성 라이브 터미널을 최근 연결 우선(last-wins)으로 1개만 유지한다.
//
// KubeVirt serial console(ttyS0)은 VM당 배타 접속이라, 같은 세션에 두 번째 연결이 붙으면 첫 번째가
// 강제로 끊긴다. 가드가 없으면 두 브라우저 창이 서로를 축출하고, 클라이언트는 비정상 종료를 재연결로
// 처리해(runtime-api-origin.mjs shouldReconnect) 무한 재연결 핑퐁에 빠진다.
//
// last-wins: 새 연결이 항상 인수하고, 이전 홀더에는 정상 종료(close 1000) 프레임을 보내 그 클라이언트가
// 재연결을 멈추게 한다. 실제 연결 정리는 새 SerialConsole이 KubeVirt 배타락으로 이전 콘솔을 밀어내며
// 일어나고(기존 자가복구 경로), 피어가 죽어 그 경로가 안 도는 경우엔 proxyTerminal의 keepalive teardown이
// 회수한다. 이 덕에 네트워크 blip 후 같은 창의 재연결이나 준비 probe→터미널 handoff 모두 거절 없이
// 자연스럽게 슬롯을 인수해 복구된다.
type consoleRegistry struct {
	mu     sync.Mutex
	active map[string]consoleConn
}

func newConsoleRegistry() *consoleRegistry {
	return &consoleRegistry{active: make(map[string]consoleConn)}
}

// acquire는 ws를 세션의 활성 콘솔로 등록한다(last-wins). 이전 홀더가 있으면 정상 종료(1000) 프레임을
// 보내 그 클라이언트가 재연결을 멈추게 한다. 돌려준 release는 compare-and-delete라, 이미 더 새로운
// 연결이 인수한 뒤 옛 홀더가 호출해도 새 등록을 지우지 않는다. nil 리시버는 가드 비활성(테스트가
// &Handler{} 리터럴로 만든 경우)으로 no-op release만 돌려준다.
func (r *consoleRegistry) acquire(sessionID string, ws consoleConn) (release func()) {
	if r == nil {
		return func() {}
	}
	r.mu.Lock()
	if prev, ok := r.active[sessionID]; ok && prev != ws {
		// 이전 홀더에 정상 종료를 통보한다. 이 프레임은 새 SerialConsole이 KubeVirt 콘솔을 밀어내기
		// 전에 전송되므로, 이전 클라이언트는 축출로 인한 비정상 종료가 아니라 1000을 받아 재연결하지 않는다.
		_ = prev.WriteControl(
			websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseNormalClosure, "superseded by newer console"),
			time.Now().Add(time.Second),
		)
	}
	r.active[sessionID] = ws
	r.mu.Unlock()

	return func() {
		r.mu.Lock()
		if cur, ok := r.active[sessionID]; ok && cur == ws {
			delete(r.active, sessionID)
		}
		r.mu.Unlock()
	}
}
