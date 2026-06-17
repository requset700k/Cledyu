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
