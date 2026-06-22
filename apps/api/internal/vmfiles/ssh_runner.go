package vmfiles

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"

	"golang.org/x/crypto/ssh"
)

const fixedListCommand = "list"

// Connector는 세션 VM의 22번 포트로 인증된 전송 연결을 연다.
type Connector interface {
	Connect(context.Context, string) (net.Conn, error)
}

// SSHRunner는 인증된 전송 연결을 통해 VM의 강제 파일 목록 명령만 실행한다.
type SSHRunner struct {
	connector       Connector
	signer          ssh.Signer
	hostKeyCallback ssh.HostKeyCallback
}

// NewSSHRunner는 전송 연결과 이미 파싱된 전용 signer를 받아 제한 SSH 실행기를 만든다.
// KubeVirt 연결, key 파일 로딩, host key 신뢰 정책은 후속 연결 계층이 담당한다.
func NewSSHRunner(connector Connector, signer ssh.Signer, hostKeyCallback ssh.HostKeyCallback) (*SSHRunner, error) {
	if connector == nil {
		return nil, errors.New("VM file-list connector is unavailable")
	}
	if signer == nil {
		return nil, errors.New("VM file-list SSH signer is unavailable")
	}
	if hostKeyCallback == nil {
		return nil, errors.New("VM file-list SSH host key callback is unavailable")
	}
	return &SSHRunner{
		connector:       connector,
		signer:          signer,
		hostKeyCallback: hostKeyCallback,
	}, nil
}

func (r *SSHRunner) Run(ctx context.Context, sessionID string) ([]byte, error) {
	if r == nil || r.connector == nil || r.signer == nil {
		return nil, errors.New("VM file-list SSH runner is unavailable")
	}
	conn, err := r.connector.Connect(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	defer conn.Close() //nolint:errcheck
	if deadline, ok := ctx.Deadline(); ok {
		if err := conn.SetDeadline(deadline); err != nil {
			return nil, fmt.Errorf("set VM SSH deadline: %w", err)
		}
	}

	// 연결이 열린 이후 context가 취소되면 SSH handshake와 명령 실행도 중단한다.
	stopWatch := make(chan struct{})
	defer close(stopWatch)
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-stopWatch:
		}
	}()

	sshConfig := &ssh.ClientConfig{
		User:            "lab",
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(r.signer)},
		HostKeyCallback: r.hostKeyCallback,
	}
	clientConn, channels, requests, err := ssh.NewClientConn(conn, "session-vm:22", sshConfig)
	if err != nil {
		return nil, fmt.Errorf("establish VM SSH connection: %w", err)
	}
	client := ssh.NewClient(clientConn, channels, requests)
	defer client.Close() //nolint:errcheck

	session, err := client.NewSession()
	if err != nil {
		return nil, fmt.Errorf("create VM SSH session: %w", err)
	}
	defer session.Close() //nolint:errcheck
	session.Stderr = io.Discard
	stdout, err := session.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("open VM file-list output: %w", err)
	}
	if err := session.Start(fixedListCommand); err != nil {
		return nil, fmt.Errorf("start VM file-list command: %w", err)
	}

	output, err := io.ReadAll(io.LimitReader(stdout, MaxPayloadBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read VM file-list output: %w", err)
	}
	if len(output) > MaxPayloadBytes {
		_ = session.Close()
		return nil, fmt.Errorf("VM file-list output exceeds %d bytes", MaxPayloadBytes)
	}
	if err := session.Wait(); err != nil {
		return nil, fmt.Errorf("wait for VM file-list command: %w", err)
	}
	return output, nil
}
