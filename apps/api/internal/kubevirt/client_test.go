package kubevirt

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNewClientsLoadsKubeconfigPathList(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "first.yaml")
	second := filepath.Join(dir, "second.yaml")

	kubeconfig := []byte(`
apiVersion: v1
kind: Config
clusters:
- name: cledyu
  cluster:
    server: https://127.0.0.1:6443
    insecure-skip-tls-verify: true
contexts:
- name: cledyu
  context:
    cluster: cledyu
    user: lab
current-context: cledyu
users:
- name: lab
  user:
    token: test-token
`)

	if err := os.WriteFile(first, kubeconfig, 0o600); err != nil {
		t.Fatalf("write first kubeconfig: %v", err)
	}
	if err := os.WriteFile(second, kubeconfig, 0o600); err != nil {
		t.Fatalf("write second kubeconfig: %v", err)
	}

	_, _, err := newClients(first + string(os.PathListSeparator) + second)
	if err != nil {
		t.Fatalf("newClients should load KUBECONFIG path-list: %v", err)
	}
}
