package ec2

import (
	"strings"
	"testing"

	"github.com/requset700k/cledyu/api/internal/config"
	"github.com/requset700k/cledyu/api/internal/session"
)

// SSM Agent 설치는 채점 경로의 전제라 authkey 유무와 무관하게 항상 포함돼야 한다.
func TestRenderCloudInit_InstallsSSMAlways(t *testing.T) {
	out := renderCloudInit("sess1", &config.AWSConfig{}, session.BootInit{}, "")
	if !strings.Contains(out, "amazon-ssm-agent") {
		t.Fatalf("SSM Agent 설치가 누락됨:\n%s", out)
	}
}

// authkey 가 없으면 tailnet 가입·IDE 가 불가하므로 tailscale·code-server 는 빠지고
// SSM 채점 전용으로만 부팅한다(현행 계약 보존).
func TestRenderCloudInit_NoAuthKey_SSMOnly(t *testing.T) {
	out := renderCloudInit("sess1", &config.AWSConfig{}, session.BootInit{}, "")
	if strings.Contains(out, "tailscale") {
		t.Fatalf("authkey 가 없는데 tailscale 이 포함됨:\n%s", out)
	}
	if strings.Contains(out, "code-server") {
		t.Fatalf("authkey 가 없는데 code-server 가 포함됨:\n%s", out)
	}
}

// tailscale 바이너리 설치는 반드시 `tailscale up` 보다 먼저 와야 한다(설치 후 가입).
func TestRenderCloudInit_WithAuthKey_InstallsTailscaleBeforeUp(t *testing.T) {
	cfg := &config.AWSConfig{TailscaleAuthKey: "tskey-test", TailnetHostnamePrefix: "lab"}
	out := renderCloudInit("sess1", cfg, session.BootInit{}, cfg.TailscaleAuthKey)

	install := strings.Index(out, "tailscale.com/install.sh")
	up := strings.Index(out, "tailscale up")
	switch {
	case install < 0:
		t.Fatalf("tailscale 설치가 누락됨:\n%s", out)
	case up < 0:
		t.Fatalf("tailscale up 이 누락됨:\n%s", out)
	case install > up:
		t.Fatalf("tailscale 설치가 up 보다 뒤에 옴 (install=%d up=%d):\n%s", install, up, out)
	}
}

// authkey 가 있으면 브라우저 IDE(code-server)도 best-effort 로 설치한다.
func TestRenderCloudInit_WithAuthKey_InstallsCodeServer(t *testing.T) {
	cfg := &config.AWSConfig{TailscaleAuthKey: "tskey-test"}
	out := renderCloudInit("sess1", cfg, session.BootInit{}, cfg.TailscaleAuthKey)
	if !strings.Contains(out, "code-server.dev/install.sh") {
		t.Fatalf("code-server 설치가 누락됨:\n%s", out)
	}
}

// 랩 콘텐츠는 베이크된 lab-base 전제로 init.packages 에서 curl·unzip 을 뺐다. EC2 는 비베이크 AMI 일
// 수 있으므로 누락 시 보강하는 가드가 있어야 하고, curl 에 의존하는 tailscale 설치·랩 runcmd 보다
// 먼저 와야 한다.
func TestRenderCloudInit_EnsuresCurlBeforeDependents(t *testing.T) {
	cfg := &config.AWSConfig{TailscaleAuthKey: "tskey-test", TailnetHostnamePrefix: "lab"}
	out := renderCloudInit("sess1", cfg, session.BootInit{Runcmd: []string{"curl -sfL https://get.k3s.io | sh"}}, cfg.TailscaleAuthKey)

	guard := strings.Index(out, "command -v $c")
	tsInstall := strings.Index(out, "tailscale.com/install.sh")
	labCurl := strings.Index(out, "get.k3s.io")
	switch {
	case guard < 0:
		t.Fatalf("curl·unzip 보강 가드가 누락됨:\n%s", out)
	case tsInstall < 0 || guard > tsInstall:
		t.Fatalf("curl 가드가 tailscale 설치보다 뒤이거나 누락됨 (guard=%d ts=%d):\n%s", guard, tsInstall, out)
	case labCurl < 0 || guard > labCurl:
		t.Fatalf("curl 가드가 랩 runcmd 보다 뒤이거나 누락됨 (guard=%d lab=%d):\n%s", guard, labCurl, out)
	}
}

// 랩 콘텐츠(BootInit.Runcmd)는 플랫폼 도구 설치가 끝난 뒤에 실행돼야 한다.
func TestRenderCloudInit_LabRuncmdAfterPlatformInstalls(t *testing.T) {
	cfg := &config.AWSConfig{TailscaleAuthKey: "tskey-test"}
	out := renderCloudInit("sess1", cfg, session.BootInit{Runcmd: []string{"echo lab-content-marker"}}, cfg.TailscaleAuthKey)

	ssm := strings.Index(out, "amazon-ssm-agent")
	lab := strings.Index(out, "lab-content-marker")
	if ssm < 0 || lab < 0 || ssm > lab {
		t.Fatalf("랩 runcmd 가 플랫폼 설치보다 먼저거나 누락됨 (ssm=%d lab=%d):\n%s", ssm, lab, out)
	}
}

// 무거운 code-server 설치는 랩 초기화(BootInit.Runcmd, 예: k3s 준비)를 막지 않도록
// 반드시 랩 runcmd 뒤(맨 마지막)에 와야 한다.
func TestRenderCloudInit_CodeServerAfterLabRuncmd(t *testing.T) {
	cfg := &config.AWSConfig{TailscaleAuthKey: "tskey-test"}
	out := renderCloudInit("sess1", cfg, session.BootInit{Runcmd: []string{"echo lab-content-marker"}}, cfg.TailscaleAuthKey)

	lab := strings.Index(out, "lab-content-marker")
	ide := strings.Index(out, "code-server.dev/install.sh")
	if lab < 0 || ide < 0 || ide < lab {
		t.Fatalf("code-server 가 랩 runcmd 보다 앞이거나 누락됨 (lab=%d ide=%d):\n%s", lab, ide, out)
	}
}
