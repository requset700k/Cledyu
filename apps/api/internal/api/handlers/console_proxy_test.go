package handlers

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// fakeVMConn은 PTY/serial 연결을 흉내내, proxyTerminal 이 셸로 쓴 입력과 적용한 리사이즈를 기록한다.
// Read 는 Close 까지 블록해 실제 라이브 터미널처럼 출력 없이 대기한다. Resize 구현으로 terminalResizer
// 를 만족(EC2 SSH PTY 역할)하지만, 비-v2 연결에서는 호출되지 않아야 한다.
type fakeVMConn struct {
	mu      sync.Mutex
	writes  []byte
	resizes [][2]int
	done    chan struct{}
	once    sync.Once
}

func newFakeVMConn() *fakeVMConn { return &fakeVMConn{done: make(chan struct{})} }

func (f *fakeVMConn) Read(p []byte) (int, error) { <-f.done; return 0, io.EOF }

func (f *fakeVMConn) Write(p []byte) (int, error) {
	f.mu.Lock()
	f.writes = append(f.writes, p...)
	f.mu.Unlock()
	return len(p), nil
}

func (f *fakeVMConn) Close() error { f.once.Do(func() { close(f.done) }); return nil }

func (f *fakeVMConn) Resize(cols, rows int) error {
	f.mu.Lock()
	f.resizes = append(f.resizes, [2]int{cols, rows})
	f.mu.Unlock()
	return nil
}

func (f *fakeVMConn) snapshot() (string, [][2]int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return string(f.writes), append([][2]int(nil), f.resizes...)
}

func newProxyTestServer(t *testing.T, conn *fakeVMConn, pinnedCols, pinnedRows int) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := wsUpgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		proxyTerminal(ws, conn, pinnedCols, pinnedRows)
	}))
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http")
}

func eventually(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal(msg)
}

// 리사이즈 전파(버그의 근본 원인)가 동작하는지 머지된 proxyTerminal 을 실 WebSocket 으로 구동해 검증한다:
// v2 클라이언트의 리사이즈 프레임이 PTY winsize 까지 도달하고, 키 입력은 raw 로 전달되며, 제어 프레임이
// 셸 입력으로 새지 않는다. 또 serial 권위 pin(80x24)이 v2 클라이언트에 먼저 통보된다.
func TestProxyTerminal_V2_ResizePropagatesAndInputContract(t *testing.T) {
	conn := newFakeVMConn()
	url := newProxyTestServer(t, conn, serialConsoleCols, serialConsoleRows)

	d := websocket.Dialer{Subprotocols: []string{terminalSubprotocolV2}}
	c, resp, err := d.Dial(url, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close() //nolint:errcheck
	if got := resp.Header.Get("Sec-WebSocket-Protocol"); got != terminalSubprotocolV2 {
		t.Fatalf("subprotocol 미협상: %q", got)
	}

	// 서버가 v2 클라이언트에 권위 pin(80x24)을 먼저 보낸다(serial 정합).
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	mt, msg, err := c.ReadMessage()
	if err != nil {
		t.Fatalf("pin 읽기: %v", err)
	}
	if mt != websocket.TextMessage || !strings.Contains(string(msg), `"type":"resize"`) || !strings.Contains(string(msg), `"cols":80`) {
		t.Fatalf("권위 pin 프레임 불일치: (%d) %q", mt, msg)
	}

	// 리사이즈 제어 프레임(Text) → PTY winsize 반영. 키 입력(Binary) → raw 전달.
	if err := c.WriteMessage(websocket.TextMessage, []byte(`{"type":"resize","cols":100,"rows":30}`)); err != nil {
		t.Fatalf("resize 전송: %v", err)
	}
	if err := c.WriteMessage(websocket.BinaryMessage, []byte("echo hi\n")); err != nil {
		t.Fatalf("키입력 전송: %v", err)
	}

	eventually(t, func() bool {
		w, r := conn.snapshot()
		return len(r) == 1 && r[0] == [2]int{100, 30} && strings.Contains(w, "echo hi\n")
	}, "리사이즈가 PTY 에 반영되지 않거나 키 입력이 전달되지 않음")

	// 제어 프레임이 셸 입력으로 새어나가면 안 된다.
	if w, _ := conn.snapshot(); strings.Contains(w, "resize") {
		t.Errorf("리사이즈 제어 프레임이 셸 입력으로 누출: %q", w)
	}
}

// 구 웹↔새 API 스큐: subprotocol 미협상 연결은 모든 TextMessage 를 raw 입력으로 전달해야 한다.
// 사용자가 resize 처럼 보이는 JSON 을 붙여넣어도 제어 프레임으로 소비되지 않고 셸로 가야 한다.
func TestProxyTerminal_NonV2_ResizeJSONPassesThroughRaw(t *testing.T) {
	conn := newFakeVMConn()
	url := newProxyTestServer(t, conn, serialConsoleCols, serialConsoleRows)

	c, resp, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close() //nolint:errcheck
	if got := resp.Header.Get("Sec-WebSocket-Protocol"); got != "" {
		t.Fatalf("미요청인데 subprotocol 협상됨: %q", got)
	}

	payload := `{"type":"resize","cols":100,"rows":30}`
	if err := c.WriteMessage(websocket.TextMessage, []byte(payload)); err != nil {
		t.Fatalf("write: %v", err)
	}

	eventually(t, func() bool {
		w, _ := conn.snapshot()
		return strings.Contains(w, payload)
	}, "비-v2 Text 입력이 raw 로 전달되지 않음")

	if _, r := conn.snapshot(); len(r) != 0 {
		t.Errorf("비-v2 연결인데 Resize 호출됨: %v", r)
	}
}
