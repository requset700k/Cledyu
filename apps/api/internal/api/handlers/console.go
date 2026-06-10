package handlers

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"go.uber.org/zap"
	kvcorev1 "kubevirt.io/client-go/kubevirt/typed/core/v1"
)

// wsUpgrader는 브라우저 WebSocket 업그레이드를 처리한다.
// WS Upgrade는 CORS 미들웨어를 타지 않으므로 여기서 Origin을 직접 검사한다.
var wsUpgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		switch r.Header.Get("Origin") {
		case "", "http://localhost:3000", "https://app.cledyu.local":
			return true
		default:
			return false
		}
	},
}

// Console은 세션 VM의 serial console(ttyS0)을 브라우저 xterm.js 터미널에 양방향 프록시한다.
// 세션의 VM은 kubevirt.Manager가 ns=`lab-<sessionID>`, name=`session-vm` 형태로 생성한다.
// GET /api/v1/sessions/:id/ws
func (h *Handler) Console(c *gin.Context) {
	if h.virt == nil {
		h.err(c, http.StatusServiceUnavailable, "live terminal unavailable (kube client not configured)")
		return
	}
	sessionID := c.Param("id")
	ns, vm := "lab-"+sessionID, "session-vm"

	// 소유자 검증(WS 업그레이드 전) — 세션 ID 추측만으로 타인의 터미널에 붙을 수 없게 한다.
	// 소유자 정보는 namespace annotation(영속)에서 읽는다. sessions 매니저가 없으면(h.virt 만
	// 있는 비정상 조합) 검사가 불가능하므로 fail-closed 로 차단한다.
	if h.sessions == nil {
		h.err(c, http.StatusServiceUnavailable, "session manager not configured")
		return
	}
	sess, err := h.sessions.Get(c.Request.Context(), sessionID)
	if err != nil {
		h.err(c, http.StatusNotFound, "session not found")
		return
	}
	if h.denyIfNotSessionOwner(c, sess) {
		return
	}

	ws, err := wsUpgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		h.log.Warn("ws upgrade failed", zap.Error(err))
		return
	}
	defer ws.Close() //nolint:errcheck

	// http.Server의 ReadTimeout/WriteTimeout(15s)이 장수명 WS를 끊지 않도록 deadline 해제.
	_ = ws.SetReadDeadline(time.Time{})
	_ = ws.SetWriteDeadline(time.Time{})

	con, err := h.virt.VirtualMachineInstance(ns).SerialConsole(vm, &kvcorev1.SerialConsoleOptions{
		ConnectionTimeout: 10 * time.Second,
	})
	if err != nil {
		h.log.Warn("serial console connect failed", zap.String("ns", ns), zap.String("vm", vm), zap.Error(err))
		_ = ws.WriteMessage(websocket.TextMessage, []byte("\r\n[VM 콘솔 연결 실패: "+err.Error()+"]\r\n"))
		return
	}
	vmConn := con.AsConn()
	defer vmConn.Close() //nolint:errcheck

	// keepalive ping — 유휴 연결이 중간 프록시에 의해 끊기는 것을 방지.
	stopPing := make(chan struct{})
	defer close(stopPing)
	go func() {
		t := time.NewTicker(20 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-stopPing:
				return
			case <-t.C:
				_ = ws.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second))
			}
		}
	}()

	// 브라우저 → VM (키 입력). 읽기 에러(브라우저 종료) 시 VM 연결을 닫아 아래 루프도 종료시킨다.
	go func() {
		for {
			_, data, err := ws.ReadMessage()
			if err != nil {
				_ = vmConn.Close()
				return
			}
			if _, err := vmConn.Write(data); err != nil {
				return
			}
		}
	}()

	// VM → 브라우저 (콘솔 출력). 이 루프가 끝나면 핸들러가 반환되며 연결 정리.
	buf := make([]byte, 4096)
	for {
		n, err := vmConn.Read(buf)
		if n > 0 {
			if werr := ws.WriteMessage(websocket.BinaryMessage, buf[:n]); werr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}
