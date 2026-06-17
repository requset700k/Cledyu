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
