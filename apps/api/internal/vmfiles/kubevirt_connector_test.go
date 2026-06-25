package vmfiles

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"k8s.io/client-go/rest"
)

func TestKubeVirtConnectorDialsSessionVMPortForward(t *testing.T) {
	const token = "test-service-account-token"
	upgrader := websocket.Upgrader{
		Subprotocols: []string{"plain.kubevirt.io"},
		CheckOrigin:  func(*http.Request) bool { return true },
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		const wantPath = "/apis/subresources.kubevirt.io/v1/namespaces/lab-abc123/virtualmachineinstances/session-vm/portforward/22/tcp"
		if r.URL.Path != wantPath {
			t.Errorf("request path = %q, want %q", r.URL.Path, wantPath)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+token {
			t.Errorf("Authorization = %q, want bearer token", got)
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("Upgrade() error = %v", err)
			return
		}
		defer conn.Close() //nolint:errcheck
		messageType, payload, err := conn.ReadMessage()
		if err != nil {
			t.Errorf("ReadMessage() error = %v", err)
			return
		}
		if messageType != websocket.BinaryMessage || string(payload) != "ping" {
			t.Errorf("message = (%d, %q), want binary ping", messageType, payload)
			return
		}
		if err := conn.WriteMessage(websocket.BinaryMessage, []byte("pong")); err != nil {
			t.Errorf("WriteMessage() error = %v", err)
		}
	}))
	defer server.Close()

	connector, err := NewKubeVirtConnector(&rest.Config{
		Host:        server.URL,
		BearerToken: token,
	})
	if err != nil {
		t.Fatalf("NewKubeVirtConnector() error = %v", err)
	}
	conn, err := connector.Connect(context.Background(), "abc123")
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	defer conn.Close() //nolint:errcheck

	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	got, err := io.ReadAll(io.LimitReader(conn, 4))
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	if string(got) != "pong" {
		t.Fatalf("Read() = %q, want pong", got)
	}
}

func TestKubeVirtConnectorCancelsHandshakeWithContext(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer server.Close()

	connector, err := NewKubeVirtConnector(&rest.Config{Host: server.URL})
	if err != nil {
		t.Fatalf("NewKubeVirtConnector() error = %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	_, err = connector.Connect(ctx, "abc123")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Connect() error = %v, want context deadline exceeded", err)
	}
}
