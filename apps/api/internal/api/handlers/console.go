package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
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

// serialConsoleCols/Rows는 KubeVirt serial console(ttyS0)이 가정하는 고정 터미널 크기다.
// serial 라인은 winsize 채널이 없어 동적 리사이즈가 불가능하므로, 백엔드가 이 권위 크기를 브라우저에
// 통보해 xterm 을 거기에 맞춘다(줄바꿈 정합). 추후 라이브 터미널을 SSH PTY 로 옮기면 동적 리사이즈 가능.
const (
	serialConsoleCols = 80
	serialConsoleRows = 24
	// maxTerminalDim은 비정상/악의적 리사이즈 값을 거르는 상한이다.
	maxTerminalDim = 1000
)

// terminalResizer는 PTY 윈도우 크기 변경을 지원하는 연결이다(EC2 SSH PTY 가 구현, serial 은 미구현).
type terminalResizer interface {
	Resize(cols, rows int) error
}

// resizeFrame은 브라우저 ↔ 백엔드가 주고받는 터미널 크기 제어 프레임(WS TextMessage, JSON)이다.
type resizeFrame struct {
	Type string `json:"type"`
	Cols int    `json:"cols"`
	Rows int    `json:"rows"`
}

// parseResizeFrame은 브라우저가 보낸 TextMessage 가 리사이즈 제어 프레임이면 cols/rows 를 돌려준다.
// 키 입력은 BinaryMessage 로 오고, 서버 에러 텍스트('{' 로 시작 안 함)는 JSON 이 아니므로 모두 ok=false.
func parseResizeFrame(data []byte) (cols, rows int, ok bool) {
	if len(data) == 0 || data[0] != '{' {
		return 0, 0, false
	}
	var f resizeFrame
	if err := json.Unmarshal(data, &f); err != nil {
		return 0, 0, false
	}
	if f.Type != "resize" || f.Cols <= 0 || f.Rows <= 0 || f.Cols > maxTerminalDim || f.Rows > maxTerminalDim {
		return 0, 0, false
	}
	return f.Cols, f.Rows, true
}

// terminalSubprotocolV2는 리사이즈 제어 프로토콜을 지원하는 클라이언트/서버를 식별하는 WS subprotocol 이다.
// service-web 과 service-api 는 순서 보장 없는 별도 ArgoCD 앱이라 배포 스큐가 생긴다. 클라이언트가 이
// subprotocol 을 요청하고 서버가 echo 한 경우에만 양쪽이 제어 프레임(resize/pin)을 주고받게 해, 스큐
// 구간(새 웹↔옛 API, 옛 웹↔새 API)에서 제어 JSON 이 셸 입력으로 주입되거나 화면에 출력되는 걸 막는다.
const terminalSubprotocolV2 = "cledyu-term-v2"

// wsUpgrader는 브라우저 WebSocket 업그레이드를 처리한다.
// WS Upgrade는 CORS 미들웨어를 타지 않으므로 여기서 Origin을 직접 검사한다.
// Subprotocols 를 광고하면 gorilla 가 클라이언트 요청과 일치할 때 응답 헤더로 echo 한다(미요청 시 빈 값).
var wsUpgrader = websocket.Upgrader{
	Subprotocols: []string{terminalSubprotocolV2},
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

	// 연결 수립 기록
	if h.met != nil {
		h.met.wsConnectionsEstablished.WithLabelValues("kubevirt").Inc()
	}

	con, err := h.virt.VirtualMachineInstance(ns).SerialConsole(vm, &kvcorev1.SerialConsoleOptions{
		ConnectionTimeout: 10 * time.Second,
	})
	if err != nil {
		h.log.Warn("serial console connect failed", zap.String("ns", ns), zap.String("vm", vm), zap.Error(err))
		_ = ws.WriteMessage(websocket.TextMessage, []byte("\r\n[VM 콘솔 연결 실패: "+err.Error()+"]\r\n"))
		// 연결 끊김 기록
		if h.met != nil {
			h.met.wsConnectionDrops.WithLabelValues("kubevirt").Inc()
		}
		return
	}
	vmConn := con.AsConn()
	proxyTerminal(ws, vmConn, serialConsoleCols, serialConsoleRows)

	// proxyTerminal 종료 = 연결 끊김
	if h.met != nil {
		h.met.wsConnectionDrops.WithLabelValues("kubevirt").Inc()
	}
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

	// 연결 수립 기록
	if h.met != nil {
		h.met.wsConnectionsEstablished.WithLabelValues("ec2").Inc()
	}

	term, err := ec2.DialTerminal(c.Request.Context(), addr, ec2.TerminalConfig{
		User:     h.cfg.AWS.LiveTerminalSSHUser,
		Password: h.cfg.AWS.LiveTerminalSSHPassword,
		Dial:     h.ec2Dial, // tsnet 다이얼러(미설정 시 nil → 기본 net.Dialer, 클러스터에선 도달 불가).
	})
	if err != nil {
		h.log.Warn("ec2 ssh terminal connect failed", zap.String("addr", addr), zap.Error(err))
		_ = ws.WriteMessage(websocket.TextMessage, []byte("\r\n[VM 터미널 연결 실패: "+err.Error()+"]\r\n"))
		if h.met != nil {
			h.met.wsConnectionDrops.WithLabelValues("ec2").Inc()
		}
		return
	}
	proxyTerminal(ws, term, 0, 0)

	// proxyTerminal 종료 = 연결 끊김
	if h.met != nil {
		h.met.wsConnectionDrops.WithLabelValues("ec2").Inc()
	}
}

// proxyTerminal은 WebSocket과 VM 연결(serial console 또는 SSH PTY)을 양방향으로 프록시한다.
// keepalive ping으로 유휴 연결 끊김을 막고, 한쪽이 닫히면 다른 쪽도 정리한다.
//
// 리사이즈 프로토콜: 브라우저는 키 입력을 BinaryMessage 로, 터미널 크기 변경을 TextMessage(JSON
// {"type":"resize","cols","rows"})로 보낸다. conn 이 terminalResizer 면 그 크기를 PTY 에 적용한다.
// pinnedCols/pinnedRows>0 이면(serial 처럼 리사이즈 불가한 연결) 시작 시 그 권위 크기를 브라우저에 통보한다.
func proxyTerminal(ws *websocket.Conn, conn io.ReadWriteCloser, pinnedCols, pinnedRows int) {
	defer conn.Close() //nolint:errcheck

	// http.Server의 ReadTimeout/WriteTimeout(15s)이 장수명 WS를 끊지 않도록 deadline 해제.
	_ = ws.SetReadDeadline(time.Time{})
	_ = ws.SetWriteDeadline(time.Time{})

	// v2 subprotocol 을 협상했을 때만 제어 프레임을 주고받는다(송신 pin·수신 resize 양방향 대칭).
	// 협상 안 된 연결(구 웹↔새 API 스큐)은 모든 TextMessage 를 raw 입력으로 전달해 입력 계약을 지킨다.
	v2 := ws.Subprotocol() == terminalSubprotocolV2

	// 권위 고정 크기 통보(serial). v2 클라이언트에만 보낸다 — 옛 웹은 제어 프레임을 일반 텍스트로
	// 출력하므로 협상 안 된 연결엔 보내지 않아 화면 오염을 막는다.
	// 출력 루프·ping 시작 전이라 동시 writer 경합이 없다.
	if v2 && pinnedCols > 0 && pinnedRows > 0 {
		frame := fmt.Sprintf(`{"type":"resize","cols":%d,"rows":%d}`, pinnedCols, pinnedRows)
		_ = ws.WriteMessage(websocket.TextMessage, []byte(frame))
	}

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

	// 브라우저 → VM (키 입력·리사이즈). 읽기 에러(브라우저 종료) 시 VM 연결을 닫아 아래 루프도 종료시킨다.
	resizer, canResize := conn.(terminalResizer)
	go func() {
		for {
			mt, data, err := ws.ReadMessage()
			if err != nil {
				_ = conn.Close()
				return
			}
			// v2 협상된 클라이언트의 TextMessage 만 제어 프레임으로 해석한다. 비협상(구 웹)은 모든
			// 입력을 raw 로 전달하므로, 그 클라이언트가 보낸 resize JSON 도 셸 입력으로 그대로 흘려보낸다.
			if v2 && mt == websocket.TextMessage {
				if cols, rows, ok := parseResizeFrame(data); ok {
					if canResize {
						_ = resizer.Resize(cols, rows)
					}
					continue
				}
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
