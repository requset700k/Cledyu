package vmfiles

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"

	"k8s.io/client-go/rest"
)

func TestNewKubeVirtFileListRunnerLoadsPrivateKey(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}
	encoded, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatalf("MarshalPKCS8PrivateKey() error = %v", err)
	}
	keyPath := filepath.Join(t.TempDir(), "id_ed25519")
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{
		Type:  "PRIVATE KEY",
		Bytes: encoded,
	}), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	runner, err := NewKubeVirtFileListRunner(&rest.Config{Host: "https://kube.example"}, keyPath)
	if err != nil {
		t.Fatalf("NewKubeVirtFileListRunner() error = %v", err)
	}
	if runner == nil {
		t.Fatal("NewKubeVirtFileListRunner() = nil")
	}
}

func TestNewKubeVirtFileListRunnerRejectsMissingKey(t *testing.T) {
	_, err := NewKubeVirtFileListRunner(
		&rest.Config{Host: "https://kube.example"},
		filepath.Join(t.TempDir(), "missing"),
	)
	if err == nil {
		t.Fatal("NewKubeVirtFileListRunner() error = nil for missing key")
	}
}
