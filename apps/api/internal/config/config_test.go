package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadUsesKubeconfigEnvFallback(t *testing.T) {
	t.Setenv("KUBECONFIG", "/tmp/cledyu-test-kubeconfig.yaml")
	t.Setenv("CLEDYU_KUBEVIRT_KUBECONFIG", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got, want := cfg.KubeVirt.Kubeconfig, "/tmp/cledyu-test-kubeconfig.yaml"; got != want {
		t.Fatalf("KubeVirt.Kubeconfig = %q, want %q", got, want)
	}
}

func TestLoadReadsKeycloakClientSecretEnv(t *testing.T) {
	t.Setenv("CLEDYU_KEYCLOAK_CLIENT_SECRET", "dummy-web-secret")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got, want := cfg.Keycloak.ClientSecret, "dummy-web-secret"; got != want {
		t.Fatalf("Keycloak.ClientSecret = %q, want %q", got, want)
	}
}

func TestLoadDefaultsProvisionTimeoutToTenMinutes(t *testing.T) {
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got, want := cfg.KubeVirt.ProvisionTimeoutMinutes, 10; got != want {
		t.Fatalf("KubeVirt.ProvisionTimeoutMinutes = %d, want %d", got, want)
	}
}

func TestLoadReadsVMFileListSSHSettings(t *testing.T) {
	t.Setenv("CLEDYU_KUBEVIRT_FILE_LIST_SSH_PUBLIC_KEY", testEd25519PublicKey+" api@cledyu")
	t.Setenv("CLEDYU_KUBEVIRT_FILE_LIST_SSH_PRIVATE_KEY_PATH", "/tmp/file-list-key")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got, want := cfg.KubeVirt.FileListSSHPublicKey, testEd25519PublicKey+" api@cledyu"; got != want {
		t.Fatalf("FileListSSHPublicKey = %q, want %q", got, want)
	}
	if got, want := cfg.KubeVirt.FileListSSHPrivateKeyPath, "/tmp/file-list-key"; got != want {
		t.Fatalf("FileListSSHPrivateKeyPath = %q, want %q", got, want)
	}
}

func TestLoadReadsVMFileListPublicKeyFromMountedFile(t *testing.T) {
	keyPath := filepath.Join(t.TempDir(), "public_key")
	if err := os.WriteFile(keyPath, []byte(testEd25519PublicKey+" api@cledyu\n"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	t.Setenv("CLEDYU_KUBEVIRT_FILE_LIST_SSH_PUBLIC_KEY", "")
	t.Setenv("CLEDYU_KUBEVIRT_FILE_LIST_SSH_PUBLIC_KEY_PATH", keyPath)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got, want := cfg.KubeVirt.FileListSSHPublicKey, testEd25519PublicKey+" api@cledyu"; got != want {
		t.Fatalf("FileListSSHPublicKey = %q, want %q", got, want)
	}
}

func TestLoadRejectsSharedLabAndFileListSSHKeyMaterial(t *testing.T) {
	t.Setenv("CLEDYU_KUBEVIRT_LAB_SSH_PUBLIC_KEY", testEd25519PublicKey+" validation-engine@cledyu")
	t.Setenv("CLEDYU_KUBEVIRT_FILE_LIST_SSH_PUBLIC_KEY", testEd25519PublicKey+" api-file-list@cledyu")

	_, err := Load()
	if err == nil {
		t.Fatal("Load() error = nil, want duplicate SSH key material error")
	}
	if !strings.Contains(err.Error(), "must use distinct key material") {
		t.Fatalf("Load() error = %v, want distinct key material error", err)
	}
}

func TestLoadRejectsMalformedFileListSSHKey(t *testing.T) {
	t.Setenv("CLEDYU_KUBEVIRT_FILE_LIST_SSH_PUBLIC_KEY", "ssh-ed25519 not-a-valid-key api-file-list@cledyu")

	_, err := Load()
	if err == nil {
		t.Fatal("Load() error = nil, want malformed file-list SSH public key error")
	}
	if !strings.Contains(err.Error(), "file-list SSH public key") {
		t.Fatalf("Load() error = %v, want file-list SSH public key error", err)
	}
}

func TestLoadRejectsFileListSSHKeyWithAuthorizedKeysOptions(t *testing.T) {
	t.Setenv("CLEDYU_KUBEVIRT_FILE_LIST_SSH_PUBLIC_KEY", `command="/bin/true" `+testEd25519PublicKey+" api-file-list@cledyu")

	_, err := Load()
	if err == nil {
		t.Fatal("Load() error = nil, want file-list SSH public key options error")
	}
	if !strings.Contains(err.Error(), "must not include authorized_keys options") {
		t.Fatalf("Load() error = %v, want authorized_keys options error", err)
	}
}

const testEd25519PublicKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGIZUWVKVjoJzh6dirTVWAtLYewp+SXW54f3uiS8tCFj"
