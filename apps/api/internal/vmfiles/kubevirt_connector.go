package vmfiles

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"time"

	"github.com/gorilla/websocket"
	"k8s.io/client-go/rest"
	kubevirtv1 "kubevirt.io/api/core/v1"
	"kubevirt.io/client-go/subresources"
)

const websocketBufferSize = 10 * 1024

// KubeVirtConnector는 Kubernetes 인증 wrapper를 거쳐 세션 VMI의 SSH port-forward를 연다.
// KubeVirt client-go의 blocking helper와 달리 WebSocket handshake에 요청 context를 직접 전달한다.
// 파일 탐색은 학생 브라우저의 수동 새로고침에서 시작되므로, 요청 취소·timeout이 즉시 하위
// port-forward dial까지 전파되어야 API worker와 동시성 슬롯이 누수되지 않는다.
type KubeVirtConnector struct {
	config    *rest.Config
	tlsConfig *tls.Config
}

func NewKubeVirtConnector(config *rest.Config) (*KubeVirtConnector, error) {
	if config == nil {
		return nil, errors.New("KubeVirt REST config is unavailable")
	}
	tlsConfig, err := rest.TLSConfigFor(config)
	if err != nil {
		return nil, fmt.Errorf("build KubeVirt TLS config: %w", err)
	}
	return &KubeVirtConnector{
		config:    rest.CopyConfig(config),
		tlsConfig: tlsConfig,
	}, nil
}

func (c *KubeVirtConnector) Connect(ctx context.Context, sessionID string) (net.Conn, error) {
	if c == nil || c.config == nil {
		return nil, errors.New("KubeVirt connector is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if sessionID == "" {
		return nil, errors.New("session ID is required")
	}
	if !isValidKubeVirtSessionID(sessionID) {
		return nil, fmt.Errorf("invalid session ID %q", sessionID)
	}

	// 세션 VM 이름은 Session API가 생성하는 KubeVirt 매니페스트 계약이다.
	// namespace: lab-{sessionID}, VMI: session-vm, SSH: guest 22/tcp.
	// 별도 discovery를 넣지 않는 이유는 파일 접근 경로를 해당 세션 VM 하나로 고정해
	// 다른 VMI나 namespace로 확장되는 실수를 줄이기 위해서다.
	address, err := portForwardURL(c.config.Host, sessionID)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, fmt.Errorf("create KubeVirt port-forward request: %w", err)
	}

	// KubeVirt subresource는 WebSocket 업그레이드 후 plain.kubevirt.io binary stream을
	// 흘려준다. rest.HTTPWrappersForConfig를 통과시켜 ServiceAccount bearer token,
	// impersonation, proxy, TLS 설정을 일반 Kubernetes client와 동일하게 적용한다.
	dialer := &websocket.Dialer{
		Proxy:           http.ProxyFromEnvironment,
		TLSClientConfig: c.tlsConfig,
		ReadBufferSize:  websocketBufferSize,
		WriteBufferSize: websocketBufferSize,
		Subprotocols:    []string{subresources.PlainStreamProtocolName},
	}
	if c.config.Proxy != nil {
		dialer.Proxy = c.config.Proxy
	}
	roundTripper := &websocketDialRoundTripper{dialer: dialer}
	wrapped, err := rest.HTTPWrappersForConfig(c.config, roundTripper)
	if err != nil {
		return nil, fmt.Errorf("wrap KubeVirt port-forward transport: %w", err)
	}
	response, err := wrapped.RoundTrip(request)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		if deadline, ok := ctx.Deadline(); ok && !time.Now().Before(deadline) {
			return nil, context.DeadlineExceeded
		}
		return nil, fmt.Errorf("dial KubeVirt port-forward: %w", err)
	}
	if response == nil || response.StatusCode != http.StatusSwitchingProtocols || roundTripper.conn == nil {
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		status := "no response"
		if response != nil {
			status = response.Status
		}
		return nil, fmt.Errorf("KubeVirt port-forward rejected: %s", status)
	}
	return &binaryWebsocketConn{Conn: roundTripper.conn}, nil
}

func portForwardURL(host, sessionID string) (string, error) {
	u, err := url.Parse(host)
	if err != nil {
		return "", fmt.Errorf("parse KubeVirt API host: %w", err)
	}
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	case "http":
		u.Scheme = "ws"
	default:
		return "", fmt.Errorf("unsupported KubeVirt API scheme %q", u.Scheme)
	}
	u.Path = path.Join(
		u.Path,
		"apis",
		"subresources.kubevirt.io",
		kubevirtv1.ApiStorageVersion,
		"namespaces",
		"lab-"+sessionID,
		"virtualmachineinstances",
		"session-vm",
		"portforward",
		"22",
		"tcp",
	)
	return u.String(), nil
}

// isValidKubeVirtSessionID는 Session API가 생성하는 4바이트 random hex ID만 허용한다.
// 이 값은 KubeVirt API URL path와 namespace 이름에 들어가므로 사용자 입력이 섞이는 후속
// HTTP 경로에서도 slash/dot 정규화가 발생하기 전에 차단한다.
func isValidKubeVirtSessionID(sessionID string) bool {
	if len(sessionID) != 8 {
		return false
	}
	for _, r := range sessionID {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

// websocketDialRoundTripper는 rest.HTTPWrappersForConfig가 인증 헤더를 붙인 요청을 실제
// gorilla/websocket dial로 넘긴다. http.RoundTripper 모양을 맞추기 위한 얇은 어댑터다.
type websocketDialRoundTripper struct {
	dialer *websocket.Dialer
	conn   *websocket.Conn
}

func (r *websocketDialRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	conn, response, err := r.dialer.DialContext(request.Context(), request.URL.String(), request.Header)
	if err != nil {
		return response, err
	}
	r.conn = conn
	return response, nil
}

// binaryWebsocketConn은 KubeVirt plain stream의 binary message를 net.Conn으로 노출한다.
// SSH client는 net.Conn만 알면 되므로, WebSocket framing은 여기서 숨긴다.
// text/control frame은 포트포워드 데이터가 아니어서 무시하고 binary payload만 전달한다.
type binaryWebsocketConn struct {
	*websocket.Conn
	reader io.Reader
}

func (c *binaryWebsocketConn) Read(buffer []byte) (int, error) {
	for {
		if c.reader == nil {
			messageType, reader, err := c.NextReader()
			if err != nil {
				return 0, err
			}
			if messageType != websocket.BinaryMessage {
				continue
			}
			c.reader = reader
		}
		n, err := c.reader.Read(buffer)
		if errors.Is(err, io.EOF) {
			c.reader = nil
			if n > 0 {
				return n, nil
			}
			continue
		}
		return n, err
	}
}

func (c *binaryWebsocketConn) Write(buffer []byte) (int, error) {
	writer, err := c.NextWriter(websocket.BinaryMessage)
	if err != nil {
		return 0, err
	}
	n, writeErr := writer.Write(buffer)
	closeErr := writer.Close()
	if writeErr != nil {
		return n, writeErr
	}
	return n, closeErr
}

func (c *binaryWebsocketConn) LocalAddr() net.Addr  { return c.UnderlyingConn().LocalAddr() }
func (c *binaryWebsocketConn) RemoteAddr() net.Addr { return c.UnderlyingConn().RemoteAddr() }
func (c *binaryWebsocketConn) SetDeadline(deadline time.Time) error {
	if err := c.SetReadDeadline(deadline); err != nil {
		return err
	}
	return c.SetWriteDeadline(deadline)
}
