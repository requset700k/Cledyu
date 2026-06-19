package kubevirt

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// 렌더 결과가 유효한 YAML 인지 + 공통부(autologin getty, lab 사용자)가 유지되는지 확인한다.
func TestRenderCloudInit_Base(t *testing.T) {
	out := renderCloudInit("abc123", "ssh-ed25519 AAAA test@cledyu", BootInit{})

	var parsed map[string]any
	if err := yaml.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("rendered cloud-init is not valid YAML: %v\n%s", err, out)
	}
	for _, want := range []string{
		"hostname: session-abc123",
		"ssh_authorized_keys",
		"ssh-ed25519 AAAA test@cledyu",
		"--autologin lab",
		"systemctl restart serial-getty@ttyS0.service",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in cloud-init:\n%s", want, out)
		}
	}
	if strings.Contains(out, "packages:") {
		t.Error("empty init must not emit packages section")
	}
}

// ssh 공개키 미설정 시 키 블록이 생략된다(기존 동작 보존).
func TestRenderCloudInit_NoSSHKey(t *testing.T) {
	out := renderCloudInit("abc123", "", BootInit{})
	if strings.Contains(out, "ssh_authorized_keys") {
		t.Errorf("expected no ssh key block:\n%s", out)
	}
}

// VM 내부 DNS upstream 이 비어 있으면 cloud-init 중 apt/curl/get.k3s.io 가 실패한다.
// bootcmd 는 packages/runcmd 보다 먼저 실행되므로, 랩별 초기화 전에 systemd-resolved 를 보정해야 한다.
func TestRenderCloudInit_ConfiguresDNSBeforePackages(t *testing.T) {
	init := BootInit{
		Packages: []string{"curl"},
		Runcmd:   []string{"curl -sfL https://get.k3s.io -o /tmp/k3s-install.sh"},
	}
	out := renderCloudInit("abc123", "", init)

	var parsed struct {
		Bootcmd  []string `yaml:"bootcmd"`
		Packages []string `yaml:"packages"`
		Runcmd   []string `yaml:"runcmd"`
	}
	if err := yaml.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("rendered cloud-init is not valid YAML: %v\n%s", err, out)
	}
	if len(parsed.Bootcmd) == 0 {
		t.Fatalf("expected DNS bootcmd before packages/runcmd:\n%s", out)
	}
	joinedBootcmd := strings.Join(parsed.Bootcmd, "\n")
	for _, want := range []string{"systemd-resolved", "DNS=8.8.8.8 1.1.1.1", "Domains=~."} {
		if !strings.Contains(joinedBootcmd, want) {
			t.Errorf("expected %q in DNS bootcmd, got %v", want, parsed.Bootcmd)
		}
	}
	if len(parsed.Packages) != 1 || parsed.Packages[0] != "curl" {
		t.Errorf("unexpected packages: %v", parsed.Packages)
	}
	if len(parsed.Runcmd) == 0 || !strings.Contains(parsed.Runcmd[len(parsed.Runcmd)-1], "get.k3s.io") {
		t.Errorf("expected lab runcmd to remain after common boot setup, got %v", parsed.Runcmd)
	}
}

// 랩별 init 의 packages 와 runcmd 가 주입되고, 공통 runcmd(getty) 뒤에 붙는지 확인한다.
func TestRenderCloudInit_WithInit(t *testing.T) {
	init := BootInit{
		Packages: []string{"unzip", "ansible-core"},
		Runcmd: []string{
			"curl -fsSL https://code-server.dev/install.sh -o /tmp/i.sh",
			`printf 'bind-addr: 0.0.0.0:13337\nauth: none\n' > /home/lab/.config/code-server/config.yaml`,
		},
	}
	out := renderCloudInit("abc123", "", init)

	var parsed struct {
		Packages []string `yaml:"packages"`
		Runcmd   []string `yaml:"runcmd"`
	}
	if err := yaml.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("rendered cloud-init is not valid YAML: %v\n%s", err, out)
	}
	if len(parsed.Packages) != 2 || parsed.Packages[0] != "unzip" {
		t.Errorf("unexpected packages: %v", parsed.Packages)
	}
	// runcmd 순서: 공통(daemon-reload, getty) → 랩 init. 터미널이 먼저 열리도록 보장.
	if len(parsed.Runcmd) != 4 {
		t.Fatalf("expected 4 runcmd entries, got %d: %v", len(parsed.Runcmd), parsed.Runcmd)
	}
	if !strings.Contains(parsed.Runcmd[1], "serial-getty") {
		t.Errorf("expected getty restart before lab init, got %v", parsed.Runcmd)
	}
	// %q 인용을 거친 셸 명령(따옴표·리다이렉션 포함)이 원형 그대로 복원되는지 확인.
	if parsed.Runcmd[3] != init.Runcmd[1] {
		t.Errorf("runcmd round-trip mismatch:\n got %q\nwant %q", parsed.Runcmd[3], init.Runcmd[1])
	}
}
