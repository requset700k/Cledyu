package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/requset700k/cledyu/api/internal/config"
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

const (
	// terminalPingPeriod는 유휴 WS 연결이 중간 프록시에 끊기지 않게 보내는 keepalive ping 주기다.
	terminalPingPeriod = 20 * time.Second
	// terminalPongWait는 이 시간 동안 pong(또는 다른 프레임)이 없으면 피어가 죽은 것으로 보고 읽기를
	// 깨우는 read deadline이다. 죽은 연결이 콘솔 슬롯을 무한히 점유하는 것을 막는다. ping 주기의
	// 배수라 일시적 지연에는 관대하되(idle 터미널 유지), 실제 절단은 이 안에 회수된다.
	terminalPongWait = 60 * time.Second
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

// browserOriginAllowed는 브라우저 origin 하나를 허용할지 판정한다. ws CheckOrigin(아래)과
// router.go 의 CORS AllowOrigins 가 이 판정을 공유해, "허용 origin 집합"의 단일 소스가
// cfg.FrontendURL(운영 app.cledyu.com, 개발 app.cledyu.local) 하나가 되도록 한다.
// 빈 origin(비브라우저/서버-투-서버 호출, 브라우저는 same-origin 이어도 Origin 헤더를 보낸다)과
// 로컬 dev 서버는 환경 불문 항상 허용한다. FrontendURL 끝의 슬래시는 비교 전에 제거한다.
func browserOriginAllowed(cfg *config.Config, origin string) bool {
	if origin == "" || origin == "http://localhost:3000" {
		return true
	}
	frontendURL := ""
	if cfg != nil {
		frontendURL = strings.TrimSuffix(cfg.FrontendURL, "/")
	}
	return frontendURL != "" && origin == frontendURL
}

// wsUpgrader는 h.cfg 를 closure 로 캡처한 CheckOrigin 을 가진 업그레이더를 만든다.
// WS Upgrade는 CORS 미들웨어를 타지 않으므로 여기서 Origin을 직접 검사한다.
// Subprotocols 를 광고하면 gorilla 가 클라이언트 요청과 일치할 때 응답 헤더로 echo 한다(미요청 시 빈 값).
func (h *Handler) wsUpgrader() websocket.Upgrader {
	return websocket.Upgrader{
		Subprotocols: []string{terminalSubprotocolV2},
		CheckOrigin: func(r *http.Request) bool {
			return browserOriginAllowed(h.cfg, r.Header.Get("Origin"))
		},
	}
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

	upgrader := h.wsUpgrader()
	ws, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		h.log.Warn("ws upgrade failed", zap.Error(err))
		return
	}
	defer ws.Close() //nolint:errcheck

	// 세션당 활성 콘솔을 최근 연결 우선(last-wins)으로 1개만 유지한다. KubeVirt serial
	// console(ttyS0)은 VM당 배타 접속이라 두 번째 연결이 붙으면 첫 번째가 끊기고, 두 창이 서로를
	// 축출하며 무한 재연결한다. acquire는 새 연결을 활성으로 등록하고 이전 홀더에 정상 종료(1000)를
	// 통보해 그 클라이언트가 재연결을 멈추게 한다(runtime-api-origin.mjs shouldReconnect). 이전 연결의
	// 실제 정리는 아래 새 SerialConsole이 배타락으로 이전 콘솔을 밀어내며 일어난다. 거절이 아니라
	// 인수라, 네트워크 blip 후 같은 창의 재연결과 준비 probe→터미널 handoff 모두 자연스럽게 복구된다.
	release := h.consoles.acquire(sessionID, ws)
	defer release()

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
			h.met.wsConnectionDrops.WithLabelValues("kubevirt", "error").Inc()
		}
		return
	}
	vmConn := con.AsConn()
	reason := proxyTerminal(ws, vmConn, serialConsoleCols, serialConsoleRows)

	// proxyTerminal 종료 = 연결 끊김
	if h.met != nil {
		h.met.wsConnectionDrops.WithLabelValues("kubevirt", reason).Inc()
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

	upgrader := h.wsUpgrader()
	ws, err := upgrader.Upgrade(c.Writer, c.Request, nil)
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
			h.met.wsConnectionDrops.WithLabelValues("ec2", "error").Inc()
		}
		return
	}
	reason := proxyTerminal(ws, term, 0, 0)

	// proxyTerminal 종료 = 연결 끊김
	if h.met != nil {
		h.met.wsConnectionDrops.WithLabelValues("ec2", reason).Inc()
	}
}

// proxyTerminal은 WebSocket과 VM 연결(serial console 또는 SSH PTY)을 양방향으로 프록시한다.
// keepalive ping으로 유휴 연결 끊김을 막고, 한쪽이 닫히면 다른 쪽도 정리한다.
//
// 리사이즈 프로토콜: 브라우저는 키 입력을 BinaryMessage 로, 터미널 크기 변경을 TextMessage(JSON
// {"type":"resize","cols","rows"})로 보낸다. conn 이 terminalResizer 면 그 크기를 PTY 에 적용한다.
// pinnedCols/pinnedRows>0 이면(serial 처럼 리사이즈 불가한 연결) 시작 시 그 권위 크기를 브라우저에 통보한다.
func proxyTerminal(ws *websocket.Conn, conn io.ReadWriteCloser, pinnedCols, pinnedRows int) (closeReason string) {
	defer conn.Close() //nolint:errcheck

	// WriteDeadline은 해제(http.Server의 ReadTimeout/WriteTimeout이 장수명 WS를 끊지 않도록).
	// ReadDeadline은 pong 기반으로 둔다: pong(또는 어떤 프레임)이 오면 갱신하고, terminalPongWait
	// 동안 무응답이면 ReadMessage를 깨워 죽은 연결을 회수한다. 이게 없으면 절단된 피어의 핸들러가
	// conn.Read에서 영원히 블록돼 콘솔 슬롯(consoleRegistry)을 무한 점유하고, 같은 사용자의 재연결이
	// 새 활성 홀더로 인수되지 못한다. keepalive ping(아래)이 pong을 유도해 정상 연결은 계속 갱신된다.
	_ = ws.SetWriteDeadline(time.Time{})
	_ = ws.SetReadDeadline(time.Now().Add(terminalPongWait))
	ws.SetPongHandler(func(string) error {
		return ws.SetReadDeadline(time.Now().Add(terminalPongWait))
	})

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

	// keepalive ping — 유휴 연결이 중간 프록시에 의해 끊기는 것을 방지하고, 응답(pong)으로 피어
	// 생존을 확인한다. ping write가 실패하면 피어가 사라진 것이므로 ws·conn을 닫아 양쪽 루프를 깨우고
	// 핸들러가 반환·슬롯을 반납하게 한다(죽은 홀더가 콘솔 슬롯을 점유한 채 남지 않도록).
	stopPing := make(chan struct{})
	defer close(stopPing)
	go func() {
		t := time.NewTicker(terminalPingPeriod)
		defer t.Stop()
		for {
			select {
			case <-stopPing:
				return
			case <-t.C:
				if err := ws.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second)); err != nil {
					_ = ws.Close()
					_ = conn.Close()
					return
				}
			}
		}
	}()

	// 브라우저 → VM (키 입력·리사이즈). 읽기 에러(브라우저 종료) 시 VM 연결을 닫아 아래 루프도 종료시킨다.
	var clientDisconnected atomic.Bool

	resizer, canResize := conn.(terminalResizer)
	go func() {
		for {
			mt, data, err := ws.ReadMessage()
			if err != nil {
				clientDisconnected.Store(true) // 플래그 설정
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
				return "normal"
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) || clientDisconnected.Load() {
				return "normal" // VM 쪽 정상 종료
			}
			return "error" // 그 외(연결 리셋, 타임아웃 등)는 비정상
		}
	}
}
