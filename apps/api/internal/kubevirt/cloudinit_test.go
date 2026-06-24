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

// .hushlogin/.bash_profile이 lab 사용자 소유로 생성되는지, 그리고 write_files가
// users-groups보다 먼저 실행돼 "Unknown user or group: lab" 으로 실패하지 않도록
// defer: true 로 users-groups 모듈 뒤로 미뤄지는지 확인한다.
func TestRenderCloudInit_ClearsSerialConsoleOnLogin(t *testing.T) {
	out := renderCloudInit("abc123", "", BootInit{})

	var parsed struct {
		WriteFiles []struct {
			Path    string `yaml:"path"`
			Content string `yaml:"content"`
			Defer   bool   `yaml:"defer"`
		} `yaml:"write_files"`
	}
	if err := yaml.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("rendered cloud-init is not valid YAML: %v\n%s", err, out)
	}

	files := map[string]struct {
		content string
		deferIt bool
	}{}
	for _, f := range parsed.WriteFiles {
		files[f.Path] = struct {
			content string
			deferIt bool
		}{content: f.Content, deferIt: f.Defer}
	}
	hushlogin, ok := files["/home/lab/.hushlogin"]
	if !ok {
		t.Fatalf("expected .hushlogin to suppress Ubuntu MOTD, got files %v", files)
	}
	if !hushlogin.deferIt {
		t.Fatal("expected .hushlogin write to be deferred until the lab user exists")
	}
	profileFile, ok := files["/home/lab/.bash_profile"]
	if !ok {
		t.Fatalf("expected .bash_profile, got files %v", files)
	}
	if !profileFile.deferIt {
		t.Fatal("expected .bash_profile write to be deferred until the lab user exists")
	}
	profile := profileFile.content
	for _, want := range []string{". /home/lab/.bashrc", `\033[H\033[2J\033[3J`, "cd /home/lab"} {
		if !strings.Contains(profile, want) {
			t.Fatalf("expected %q in .bash_profile, got:\n%s", want, profile)
		}
	}
}

// 학생 터미널 프롬프트에는 내부 세션 hostname(session-xxxx)이 보이지 않아야 한다.
// VM/namespace 이름은 운영·디버깅 식별자로 유지하되, 학습자 화면에서는 Cledyu 라벨만 노출한다.
func TestRenderCloudInit_UsesLearnerFriendlyPrompt(t *testing.T) {
	out := renderCloudInit("abc123", "", BootInit{})

	var parsed struct {
		WriteFiles []struct {
			Path    string `yaml:"path"`
			Content string `yaml:"content"`
			Append  bool   `yaml:"append"`
		} `yaml:"write_files"`
	}
	if err := yaml.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("rendered cloud-init is not valid YAML: %v\n%s", err, out)
	}

	files := map[string]struct {
		content string
		append  bool
	}{}
	for _, f := range parsed.WriteFiles {
		files[f.Path] = struct {
			content string
			append  bool
		}{content: f.Content, append: f.Append}
	}
	profile := files["/home/lab/.bash_profile"].content
	if profile == "" {
		t.Fatalf("expected .bash_profile in cloud-init:\n%s", out)
	}
	if !strings.Contains(profile, ". /home/lab/.bashrc") {
		t.Fatalf("login shell must source .bashrc for the shared prompt, got:\n%s", profile)
	}

	bashrcFile := files["/home/lab/.bashrc"]
	bashrc := bashrcFile.content
	if bashrc == "" {
		t.Fatalf("expected .bashrc for non-login interactive shells:\n%s", out)
	}
	if !bashrcFile.append {
		t.Fatal("expected .bashrc write_files entry to append so Ubuntu skel .bashrc stays intact")
	}
	if !strings.Contains(bashrc, `PS1='\[\033[1;32m\]Cledyu\[\033[0m\] \w ➜ '`) {
		t.Fatalf("expected Cledyu prompt in .bashrc, got:\n%s", bashrc)
	}
	for path, content := range map[string]string{"/home/lab/.bash_profile": profile, "/home/lab/.bashrc": bashrc} {
		if strings.Contains(content, "\\h") || strings.Contains(content, "session-") {
			t.Fatalf("prompt must not expose session hostname in %s, got:\n%s", path, content)
		}
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
	if len(parsed.Runcmd) == 0 || !strings.Contains(parsed.Runcmd[0], "get.k3s.io") {
		t.Errorf("expected lab runcmd to run before terminal autologin setup, got %v", parsed.Runcmd)
	}
}

// 랩별 init 의 packages 와 runcmd 가 주입되고, 터미널 autologin 은 init 완료 후 활성화되는지 확인한다.
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
	// runcmd 순서: 랩 init → 공통(daemon-reload, getty). 터미널이 init 완료 전에 열리지 않도록 보장.
	if len(parsed.Runcmd) != 4 {
		t.Fatalf("expected 4 runcmd entries, got %d: %v", len(parsed.Runcmd), parsed.Runcmd)
	}
	if !strings.Contains(parsed.Runcmd[3], "serial-getty") {
		t.Errorf("expected getty restart after lab init, got %v", parsed.Runcmd)
	}
	// %q 인용을 거친 셸 명령(따옴표·리다이렉션 포함)이 원형 그대로 복원되는지 확인.
	if parsed.Runcmd[1] != init.Runcmd[1] {
		t.Errorf("runcmd round-trip mismatch:\n got %q\nwant %q", parsed.Runcmd[1], init.Runcmd[1])
	}
}
