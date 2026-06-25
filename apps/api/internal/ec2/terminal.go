package ec2

import (
	"context"
	"fmt"
	"io"
	"net"
	"time"

	"golang.org/x/crypto/ssh"
)

const (
	defaultSSHPort = "22"
	ptyTermType    = "xterm-256color"
	// 프론트 xterm 은 현재 리사이즈 프로토콜을 보내지 않으므로(시리얼 콘솔과 동일 raw 입력 계약)
	// 합리적인 고정 PTY 크기로 연다. 동적 리사이즈는 WS 제어 프레임 도입 시 후속.
	ptyCols = 120
	ptyRows = 40
)

// DialFunc는 host:port 로의 TCP 연결을 여는 다이얼러다. net.Dialer.DialContext 와 시그니처가 같다.
type DialFunc func(ctx context.Context, network, addr string) (net.Conn, error)

// TerminalConfig는 EC2 세션 라이브 터미널 SSH 접속 설정이다.
type TerminalConfig struct {
	User     string
	Password string        // 일반 sshd 폴백용. Tailscale SSH(accept) 면 none 인증이 먼저 통과한다.
	Port     string        // 기본 "22". 테스트에서 오버라이드.
	Timeout  time.Duration // dial+handshake 상한. 0이면 10s.
	// Dial: nil 이면 기본 net.Dialer. EC2 세션은 tailnet MagicDNS 로만 도달하므로 main 이
	// tsnet 다이얼러를 주입한다(클러스터 파드는 tailnet 에 직접 못 붙기 때문).
	Dial DialFunc
}

// Terminal은 PTY 가 붙은 SSH 셸 세션을 io.ReadWriteCloser 로 노출한다.
// Read = 셸 출력, Write = 셸 입력, Close = 세션·연결 종료. console 핸들러가 WS 와 양방향 프록시한다.
type Terminal struct {
	client  *ssh.Client
	session *ssh.Session
	stdin   io.WriteCloser
	stdout  io.Reader
}

// DialTerminal은 host(EC2 세션의 tailnet MagicDNS 호스트네임)에 SSH 접속해 PTY 셸을 연다.
// host key 는 핀하지 않는다 — 세션 VM 은 ephemeral 이고 암호화된 tailnet 으로만 도달하기 때문이다.
func DialTerminal(ctx context.Context, host string, tc TerminalConfig) (*Terminal, error) {
	timeout := tc.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	port := tc.Port
	if port == "" {
		port = defaultSSHPort
	}
	addr := net.JoinHostPort(host, port)

	cfg := &ssh.ClientConfig{
		User:            tc.User,
		Auth:            []ssh.AuthMethod{ssh.Password(tc.Password)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), //nolint:gosec // ephemeral VM over private tailnet
		Timeout:         timeout,
	}

	// dial timeout 은 ctx 로 강제한다 — 주입 다이얼러(tsnet)는 deadline 없는 요청 ctx 로 호출될 수
	// 있어, net.Dialer.Timeout 만으로는 멈춘 연결에서 핸들러가 무한 대기할 수 있다.
	dialCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	dial := tc.Dial
	if dial == nil {
		dial = (&net.Dialer{}).DialContext
	}
	conn, err := dial(dialCtx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", addr, err)
	}
	sshConn, chans, reqs, err := ssh.NewClientConn(conn, addr, cfg)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("ssh handshake %s: %w", addr, err)
	}
	client := ssh.NewClient(sshConn, chans, reqs)

	sess, err := client.NewSession()
	if err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("new ssh session: %w", err)
	}

	modes := ssh.TerminalModes{ssh.ECHO: 1, ssh.TTY_OP_ISPEED: 14400, ssh.TTY_OP_OSPEED: 14400}
	if err := sess.RequestPty(ptyTermType, ptyRows, ptyCols, modes); err != nil {
		_ = sess.Close()
		_ = client.Close()
		return nil, fmt.Errorf("request pty: %w", err)
	}

	stdin, err := sess.StdinPipe()
	if err != nil {
		_ = sess.Close()
		_ = client.Close()
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := sess.StdoutPipe()
	if err != nil {
		_ = sess.Close()
		_ = client.Close()
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}

	if err := sess.Shell(); err != nil {
		_ = sess.Close()
		_ = client.Close()
		return nil, fmt.Errorf("start shell: %w", err)
	}

	return &Terminal{client: client, session: sess, stdin: stdin, stdout: stdout}, nil
}

func (t *Terminal) Read(p []byte) (int, error)  { return t.stdout.Read(p) }
func (t *Terminal) Write(p []byte) (int, error) { return t.stdin.Write(p) }

// Close는 셸 세션과 SSH 연결을 닫는다(진행 중인 Read 는 EOF 로 풀린다).
func (t *Terminal) Close() error {
	_ = t.session.Close()
	return t.client.Close()
}
