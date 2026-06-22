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
			serveTestSSH(serverConn, serverSigner, clientSigner.PublicKey(), serverErr)
		}()
		return net.Dial("tcp", listener.Addr().String())
	})
	runner := newSSHRunner(connector, clientSigner)

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

func TestSSHRunnerAppliesContextToPortForwardConnection(t *testing.T) {
	connector := connectorFunc(func(ctx context.Context, _ string) (net.Conn, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	runner := newSSHRunner(connector, testSigner(t))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	if _, err := runner.Run(ctx, "abc123"); err == nil {
		t.Fatal("Run() error = nil, want connection deadline error")
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

func serveTestSSH(conn net.Conn, hostSigner ssh.Signer, allowedKey ssh.PublicKey, result chan<- error) {
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
			if request.Type != "exec" || ssh.Unmarshal(request.Payload, &payload) != nil || payload.Command != fixedListCommand {
				_ = request.Reply(false, nil)
				result <- fmt.Errorf("unexpected SSH request type=%q command=%q", request.Type, payload.Command)
				return
			}
			_ = request.Reply(true, nil)
			_, _ = channel.Write(emptySnapshot)
			_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
			_ = channel.Close()
			result <- nil
			return
		}
	}
	result <- fmt.Errorf("SSH session channel was not opened")
}
