package vmfiles

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"net"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

type connectorFunc func(context.Context, string) (net.Conn, error)

func (f connectorFunc) Connect(ctx context.Context, sessionID string) (net.Conn, error) {
	return f(ctx, sessionID)
}

func TestSSHRunnerUsesFixedCommandAndReturnsBoundedOutput(t *testing.T) {
	clientSigner := testSigner(t)
	serverSigner := testSigner(t)
	serverErr := make(chan error, 1)
	connector := testSSHConnector(t, serverSigner, clientSigner.PublicKey(), fixedListCommand, emptySnapshot, serverErr)
	runner, err := NewSSHRunner(connector, clientSigner, ssh.FixedHostKey(serverSigner.PublicKey()))
	if err != nil {
		t.Fatalf("NewSSHRunner() error = %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	got, err := runner.Run(ctx, "abc123")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if string(got) != string(emptySnapshot) {
		t.Fatalf("Run() output = %q, want %q", got, emptySnapshot)
	}
	if err := <-serverErr; err != nil {
		t.Fatalf("SSH server error = %v", err)
	}
}

func TestSSHRunnerReadsSpecificFileWithBoundedCommand(t *testing.T) {
	clientSigner := testSigner(t)
	serverSigner := testSigner(t)
	serverErr := make(chan error, 1)
	const wantCommand = "read work/app.log"
	const wantOutput = `{"path":"work/app.log","content":"hello\n","truncated":false}` + "\n"
	connector := testSSHConnector(t, serverSigner, clientSigner.PublicKey(), wantCommand, []byte(wantOutput), serverErr)
	runner, err := NewSSHRunner(connector, clientSigner, ssh.FixedHostKey(serverSigner.PublicKey()))
	if err != nil {
		t.Fatalf("NewSSHRunner() error = %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	got, err := runner.Read(ctx, "abc123", "work/app.log")
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if string(got) != wantOutput {
		t.Fatalf("Read() output = %q, want %q", got, wantOutput)
	}
	if err := <-serverErr; err != nil {
		t.Fatalf("SSH server error = %v", err)
	}
}

func TestSSHRunnerRejectsUnsafeReadPath(t *testing.T) {
	runner, err := NewSSHRunner(connectorFunc(func(context.Context, string) (net.Conn, error) {
		t.Fatal("connector must not be called for unsafe read path")
		return nil, fmt.Errorf("unexpected connector call")
	}), testSigner(t), ssh.InsecureIgnoreHostKey())
	if err != nil {
		t.Fatalf("NewSSHRunner() error = %v", err)
	}
	for _, path := range []string{"", "/etc/passwd", "../secret", "work/../secret", ".ssh/id_rsa", "work/.env", "work\\app.log"} {
		if _, err := runner.Read(context.Background(), "abc123", path); err == nil {
			t.Fatalf("Read(%q) error = nil, want unsafe path error", path)
		}
	}
}

func testSSHConnector(
	t *testing.T,
	serverSigner ssh.Signer,
	allowedKey ssh.PublicKey,
	expectedCommand string,
	output []byte,
	serverErr chan<- error,
) Connector {
	t.Helper()
	connector := connectorFunc(func(_ context.Context, sessionID string) (net.Conn, error) {
		if sessionID != "abc123" {
			return nil, fmt.Errorf("sessionID = %q, want abc123", sessionID)
		}
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return nil, err
		}
		go func() {
			defer listener.Close() //nolint:errcheck
			serverConn, err := listener.Accept()
			if err != nil {
				serverErr <- err
				return
			}
			serveTestSSH(serverConn, serverSigner, allowedKey, expectedCommand, output, serverErr)
		}()
		return net.Dial("tcp", listener.Addr().String())
	})
	return connector
}

func TestSSHRunnerAppliesContextToPortForwardConnection(t *testing.T) {
	connector := connectorFunc(func(ctx context.Context, _ string) (net.Conn, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	runner, err := NewSSHRunner(connector, testSigner(t), ssh.InsecureIgnoreHostKey())
	if err != nil {
		t.Fatalf("NewSSHRunner() error = %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	if _, err := runner.Run(ctx, "abc123"); err == nil {
		t.Fatal("Run() error = nil, want connection deadline error")
	}
}

func TestNewSSHRunnerRejectsMissingDependencies(t *testing.T) {
	if _, err := NewSSHRunner(nil, testSigner(t), ssh.InsecureIgnoreHostKey()); err == nil {
		t.Fatal("NewSSHRunner() error = nil for missing connector")
	}
	connector := connectorFunc(func(context.Context, string) (net.Conn, error) {
		return nil, fmt.Errorf("not called")
	})
	if _, err := NewSSHRunner(connector, nil, ssh.InsecureIgnoreHostKey()); err == nil {
		t.Fatal("NewSSHRunner() error = nil for missing signer")
	}
	if _, err := NewSSHRunner(connector, testSigner(t), nil); err == nil {
		t.Fatal("NewSSHRunner() error = nil for missing host key callback")
	}
}

func testSigner(t *testing.T) ssh.Signer {
	t.Helper()
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}
	signer, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		t.Fatalf("NewSignerFromKey() error = %v", err)
	}
	return signer
}

func serveTestSSH(
	conn net.Conn,
	hostSigner ssh.Signer,
	allowedKey ssh.PublicKey,
	expectedCommand string,
	output []byte,
	result chan<- error,
) {
	defer conn.Close() //nolint:errcheck
	config := &ssh.ServerConfig{
		PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if string(key.Marshal()) != string(allowedKey.Marshal()) {
				return nil, fmt.Errorf("unexpected client key")
			}
			return nil, nil
		},
	}
	config.AddHostKey(hostSigner)
	_, channels, requests, err := ssh.NewServerConn(conn, config)
	if err != nil {
		result <- err
		return
	}
	go ssh.DiscardRequests(requests)

	for channelRequest := range channels {
		if channelRequest.ChannelType() != "session" {
			_ = channelRequest.Reject(ssh.UnknownChannelType, "session required")
			continue
		}
		channel, channelRequests, err := channelRequest.Accept()
		if err != nil {
			result <- err
			return
		}
		for request := range channelRequests {
			var payload struct{ Command string }
			if request.Type != "exec" || ssh.Unmarshal(request.Payload, &payload) != nil || payload.Command != expectedCommand {
				_ = request.Reply(false, nil)
				result <- fmt.Errorf("unexpected SSH request type=%q command=%q", request.Type, payload.Command)
				return
			}
			_ = request.Reply(true, nil)
			_, _ = channel.Write(output)
			_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
			_ = channel.Close()
			result <- nil
			return
		}
	}
	result <- fmt.Errorf("SSH session channel was not opened")
}
