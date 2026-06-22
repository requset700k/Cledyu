package vmfiles

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"

	"golang.org/x/crypto/ssh"
	"kubevirt.io/client-go/kubecli"
)

const fixedListCommand = "list"

// Connector는 세션 VM의 22번 포트로 인증된 전송 연결을 연다.
type Connector interface {
	Connect(context.Context, string) (net.Conn, error)
}

type kubeVirtConnector struct {
	client kubecli.KubevirtClient
}

func (c kubeVirtConnector) Connect(ctx context.Context, sessionID string) (net.Conn, error) {
	type result struct {
		conn net.Conn
		err  error
	}
	resultCh := make(chan result)
	go func() {
		stream, err := c.client.VirtualMachineInstance("lab-"+sessionID).PortForward("session-vm", 22, "tcp")
		if err != nil {
			err = fmt.Errorf("open KubeVirt SSH port-forward: %w", err)
		}
		var conn net.Conn
		if err == nil {
			conn = stream.AsConn()
			if conn == nil {
				err = errors.New("KubeVirt SSH port-forward returned no connection")
			}
		}
		select {
		case resultCh <- result{conn: conn, err: err}:
		case <-ctx.Done():
			if conn != nil {
				_ = conn.Close()
			}
		}
	}()

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case res := <-resultCh:
		return res.conn, res.err
	}
}

// SSHRunner는 KubeVirt API 터널을 통해 VM의 강제 파일 목록 명령만 실행한다.
type SSHRunner struct {
	connector Connector
	signer    ssh.Signer
}

// NewKubeVirtSSHRunner는 Session API 전용 읽기 키를 불러온다.
func NewKubeVirtSSHRunner(client kubecli.KubevirtClient, privateKeyPath string) (*SSHRunner, error) {
	if client == nil {
		return nil, errors.New("KubeVirt client is unavailable")
	}
	if privateKeyPath == "" {
		return nil, errors.New("VM file-list private key path is empty")
	}
	privateKey, err := os.ReadFile(privateKeyPath)
	if err != nil {
		return nil, fmt.Errorf("read VM file-list private key: %w", err)
	}
	signer, err := ssh.ParsePrivateKey(privateKey)
	if err != nil {
		return nil, fmt.Errorf("parse VM file-list private key: %w", err)
	}
	return newSSHRunner(kubeVirtConnector{client: client}, signer), nil
}

func newSSHRunner(connector Connector, signer ssh.Signer) *SSHRunner {
	return &SSHRunner{connector: connector, signer: signer}
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

	// Port-forward 생성 API에는 context를 직접 넘길 수 없다. 연결이 열린 이후에는
	// context 취소 시 연결을 닫아 SSH handshake와 명령 실행을 즉시 중단한다.
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
		User: "lab",
		Auth: []ssh.AuthMethod{ssh.PublicKeys(r.signer)},
		// Kubernetes API가 하나의 임시 VMI로 향하는 터널을 인증·인가한다. VM host key는
		// 세션마다 달라 고정할 수 없으므로 persistent known_hosts 대신 API 터널을
		// 신뢰 경계로 사용한다.
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), //nolint:gosec
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
