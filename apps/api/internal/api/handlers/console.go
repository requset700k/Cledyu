package handlers

import (
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/requset700k/cledyu/api/internal/ec2"
	"github.com/requset700k/cledyu/api/internal/session"
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

// Console은 세션 VM의 라이브 터미널을 브라우저 xterm.js에 양방향 프록시한다.
// 프로바이더에 따라 접속 방식이 다르다:
//   - KubeVirt: virtctl SerialConsole(ttyS0) — ns=`lab-<sessionID>`, name=`session-vm`.
//   - EC2 오버플로우: tailnet(MagicDNS `<prefix>-<sessionID>`) 경유 SSH PTY 셸.
//
// GET /api/v1/sessions/:id/ws
func (h *Handler) Console(c *gin.Context) {
	sessionID := c.Param("id")

	// 소유자 검증(WS 업그레이드 전) — 세션 ID 추측만으로 타인의 터미널에 붙을 수 없게 한다.
	// 소유자 정보는 프로바이더가 영속화한 세션 메타에서 읽는다. 매니저가 없으면 fail-closed.
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

	if sess.Provider == session.ProviderEC2 {
		h.ec2Console(c, sessionID)
		return
	}
	h.kubevirtConsole(c, sessionID)
}

// kubevirtConsole은 KubeVirt 세션 VM의 serial console(ttyS0)을 WS에 프록시한다.
func (h *Handler) kubevirtConsole(c *gin.Context, sessionID string) {
	if h.virt == nil {
		h.err(c, http.StatusServiceUnavailable, "live terminal unavailable (kube client not configured)")
		return
	}
	ns, vm := "lab-"+sessionID, "session-vm"

	ws, err := wsUpgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		h.log.Warn("ws upgrade failed", zap.Error(err))
		return
	}
	defer ws.Close() //nolint:errcheck

	con, err := h.virt.VirtualMachineInstance(ns).SerialConsole(vm, &kvcorev1.SerialConsoleOptions{
		ConnectionTimeout: 10 * time.Second,
	})
	if err != nil {
		h.log.Warn("serial console connect failed", zap.String("ns", ns), zap.String("vm", vm), zap.Error(err))
		_ = ws.WriteMessage(websocket.TextMessage, []byte("\r\n[VM 콘솔 연결 실패: "+err.Error()+"]\r\n"))
		return
	}
	vmConn := con.AsConn()
	proxyTerminal(ws, vmConn)
}

// ec2Console은 EC2 세션 인스턴스에 tailnet SSH PTY로 접속해 WS에 프록시한다.
// tailnet 미가입(authkey 미설정)·아직 ready 아님이면 VMIAddress가 ErrNotFound → 503.
func (h *Handler) ec2Console(c *gin.Context, sessionID string) {
	addr, err := h.sessions.VMIAddress(c.Request.Context(), sessionID)
	if err != nil {
		if errors.Is(err, session.ErrNotFound) {
			h.err(c, http.StatusServiceUnavailable, "ec2 live terminal not ready (tailnet not joined)")
			return
		}
		h.log.Error("ec2 console address", zap.String("session_id", sessionID), zap.Error(err))
		h.err(c, http.StatusInternalServerError, "live terminal failed")
		return
	}

	ws, err := wsUpgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		h.log.Warn("ws upgrade failed", zap.Error(err))
		return
	}
	defer ws.Close() //nolint:errcheck

	term, err := ec2.DialTerminal(c.Request.Context(), addr, ec2.TerminalConfig{
		User:     h.cfg.AWS.LiveTerminalSSHUser,
		Password: h.cfg.AWS.LiveTerminalSSHPassword,
	})
	if err != nil {
		h.log.Warn("ec2 ssh terminal connect failed", zap.String("addr", addr), zap.Error(err))
		_ = ws.WriteMessage(websocket.TextMessage, []byte("\r\n[VM 터미널 연결 실패: "+err.Error()+"]\r\n"))
		return
	}
	proxyTerminal(ws, term)
}

// proxyTerminal은 WebSocket과 VM 연결(serial console 또는 SSH PTY)을 양방향으로 프록시한다.
// keepalive ping으로 유휴 연결 끊김을 막고, 한쪽이 닫히면 다른 쪽도 정리한다.
func proxyTerminal(ws *websocket.Conn, conn io.ReadWriteCloser) {
	defer conn.Close() //nolint:errcheck

	// http.Server의 ReadTimeout/WriteTimeout(15s)이 장수명 WS를 끊지 않도록 deadline 해제.
	_ = ws.SetReadDeadline(time.Time{})
	_ = ws.SetWriteDeadline(time.Time{})

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
				_ = conn.Close()
				return
			}
			if _, err := conn.Write(data); err != nil {
				return
			}
		}
	}()

	// VM → 브라우저 (출력). 이 루프가 끝나면 핸들러가 반환되며 연결 정리.
	buf := make([]byte, 4096)
	for {
		n, err := conn.Read(buf)
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
