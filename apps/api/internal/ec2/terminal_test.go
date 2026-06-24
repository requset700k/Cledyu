package ec2

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// startEchoSSHServer는 password 인증 + pty-req + shell(에코)을 처리하는 테스트용 SSH 서버를 띄운다.
// DialTerminal 의 SSH 핸드셰이크·PTY 요청·셸 입출력 프록시 경로를 실제로 검증한다.
func startEchoSSHServer(t *testing.T, user, pass string) string {
	t.Helper()

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("gen host key: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("signer: %v", err)
	}

	cfg := &ssh.ServerConfig{
		PasswordCallback: func(c ssh.ConnMetadata, p []byte) (*ssh.Permissions, error) {
			if c.User() == user && string(p) == pass {
				return &ssh.Permissions{}, nil
			}
			return nil, errAuth
		},
	}
	cfg.AddHostKey(signer)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		conn, aerr := ln.Accept()
		if aerr != nil {
			return
		}
		serveEchoConn(conn, cfg)
	}()

	return ln.Addr().(*net.TCPAddr).IP.String() + ":" + itoa(ln.Addr().(*net.TCPAddr).Port)
}

func serveEchoConn(conn net.Conn, cfg *ssh.ServerConfig) {
	sshConn, chans, reqs, err := ssh.NewServerConn(conn, cfg)
	if err != nil {
		_ = conn.Close()
		return
	}
	defer sshConn.Close() //nolint:errcheck
	go ssh.DiscardRequests(reqs)

	for nc := range chans {
		if nc.ChannelType() != "session" {
			_ = nc.Reject(ssh.UnknownChannelType, "only session")
			continue
		}
		ch, chReqs, aerr := nc.Accept()
		if aerr != nil {
			return
		}
		go func() {
			for req := range chReqs {
				switch req.Type {
				case "pty-req", "shell":
					_ = req.Reply(true, nil)
					if req.Type == "shell" {
						// 에코: 클라이언트 입력을 그대로 되돌려준다.
						go func() { _, _ = io.Copy(ch, ch); _ = ch.Close() }()
					}
				default:
					_ = req.Reply(false, nil)
				}
			}
		}()
	}
}

func TestDialTerminal_PTYRoundTrip(t *testing.T) {
	addr := startEchoSSHServer(t, "lab", "lab")
	host, port, _ := net.SplitHostPort(addr)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	term, err := DialTerminal(ctx, host, TerminalConfig{User: "lab", Password: "lab", Port: port, Timeout: 3 * time.Second})
	if err != nil {
		t.Fatalf("DialTerminal: %v", err)
	}
	defer term.Close()

	if _, err := term.Write([]byte("hello\n")); err != nil {
		t.Fatalf("write: %v", err)
	}

	got := readWithDeadline(t, term, "hello")
	if !strings.Contains(got, "hello") {
		t.Errorf("echo = %q, want to contain %q", got, "hello")
	}
}

func TestDialTerminal_AuthFailure(t *testing.T) {
	addr := startEchoSSHServer(t, "lab", "lab")
	host, port, _ := net.SplitHostPort(addr)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// 잘못된 비밀번호 → 핸드셰이크 실패.
	_, err := DialTerminal(ctx, host, TerminalConfig{User: "lab", Password: "wrong", Port: port, Timeout: 3 * time.Second})
	if err == nil {
		t.Fatal("expected auth failure, got nil")
	}
}

// readWithDeadline은 want 가 보일 때까지(또는 짧은 타임아웃까지) 읽어 누적 문자열을 반환한다.
func readWithDeadline(t *testing.T, r io.Reader, want string) string {
	t.Helper()
	type res struct {
		s string
	}
	ch := make(chan res, 1)
	go func() {
		var sb strings.Builder
		buf := make([]byte, 256)
		for {
			n, err := r.Read(buf)
			if n > 0 {
				sb.Write(buf[:n])
				if strings.Contains(sb.String(), want) {
					ch <- res{sb.String()}
					return
				}
			}
			if err != nil {
				ch <- res{sb.String()}
				return
			}
		}
	}()
	select {
	case rr := <-ch:
		return rr.s
	case <-time.After(3 * time.Second):
		t.Fatal("timed out reading echo")
		return ""
	}
}

var errAuth = sshAuthErr("auth failed")

type sshAuthErr string

func (e sshAuthErr) Error() string { return string(e) }
