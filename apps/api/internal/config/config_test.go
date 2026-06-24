package config

import "testing"

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
	t.Setenv("CLEDYU_KUBEVIRT_FILE_LIST_SSH_PUBLIC_KEY", "ssh-ed25519 AAAA-file-list api@cledyu")
	t.Setenv("CLEDYU_KUBEVIRT_FILE_LIST_SSH_PRIVATE_KEY_PATH", "/tmp/file-list-key")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got, want := cfg.KubeVirt.FileListSSHPublicKey, "ssh-ed25519 AAAA-file-list api@cledyu"; got != want {
		t.Fatalf("FileListSSHPublicKey = %q, want %q", got, want)
	}
	if got, want := cfg.KubeVirt.FileListSSHPrivateKeyPath, "/tmp/file-list-key"; got != want {
		t.Fatalf("FileListSSHPrivateKeyPath = %q, want %q", got, want)
	}
}
