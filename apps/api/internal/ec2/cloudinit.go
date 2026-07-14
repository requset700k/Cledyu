package ec2

import (
	"fmt"
	"strings"

	"github.com/requset700k/cledyu/api/internal/config"
	"github.com/requset700k/cledyu/api/internal/session"
)

// renderCloudInit은 세션 EC2 인스턴스의 cloud-init user-data(#cloud-config)를 만든다.
//
// RunInstances 의 user-data 는 Launch Template 의 base user-data 를 병합 없이 대체하므로
// (EC2 는 user-data 를 merge 하지 않는다), 플랫폼 도구(SSM Agent·tailscale·code-server)를
// 여기서 직접 설치한다. 전부 best-effort(`|| true`)·멱등이라 packer 로 미리 구운 AMI 에서도 안전하다.
//   - SSM Agent: 채점(SendCommand) 경로. authkey 유무와 무관하게 항상 설치한다.
//   - tailnet 가입: authkey 가 있으면 tailscale 설치 후 "<prefix>-<sessionID>" MagicDNS 호스트네임으로
//     tailscale up. 이 호스트네임으로 API/검증엔진이 인스턴스에 도달한다(라이브 터미널/IDE 프록시·SSH).
//   - code-server: 브라우저 IDE. tailnet 경유로 프록시되므로 authkey 가 있을 때만, best-effort 로 설치한다.
//   - 랩별 초기화: BootInit 의 packages(apt) 설치와 runcmd 실행(플랫폼 도구 설치 뒤에 온다).
//
// authKey 는 세션이 tailnet 에 가입할 때 쓸 authkey 다. 프로비저너가 세션마다 발급한 one-off 키
// (issue #307)이거나, 미발급 시 정적 폴백(cfg.TailscaleAuthKey)이다. 비면 tailscale·code-server 를
// 생략하고 SSM 채점 전용으로 부팅한다.
func renderCloudInit(sessionID string, cfg *config.AWSConfig, init session.BootInit, authKey string) string {
	var b strings.Builder
	b.WriteString("#cloud-config\n")

	// lab 사용자 — KubeVirt cloud-init 과 동일하게 생성한다. 랩 콘텐츠가 /home/lab 과 `lab` 계정에
	// 의존하고(예: usermod -aG docker lab, /home/lab/... 파일 작성), RunInstances user-data 가
	// Launch Template 의 base user-data 를 대체하므로(EC2 는 병합 안 함), 여기서 만들지 않으면
	// 베이스 AMI 가 미리 굽지 않는 한 오버플로우 세션의 init/채점이 깨진다.
	b.WriteString(`users:
  - name: lab
    lock_passwd: false
    shell: /bin/bash
    sudo: ALL=(ALL) NOPASSWD:ALL
chpasswd:
  expire: false
  list: |
    lab:lab
`)

	if len(init.Packages) > 0 {
		b.WriteString("package_update: true\n")
		b.WriteString("packages:\n")
		for _, p := range init.Packages {
			fmt.Fprintf(&b, "  - %s\n", yamlScalar(p))
		}
	}

	b.WriteString("runcmd:\n")
	// 공통 CLI(curl·unzip) 보장 — on-prem 은 lab-base 이미지에 이를 미리 굽지만, EC2 overflow 는
	// 베이킹되지 않은 AMI(stock Canonical·이전 커스텀)로 부팅될 수 있다. 랩 콘텐츠가 베이크 전제로
	// init.packages 에서 이들을 빼므로, EC2 에선 누락 시에만 설치해 보강한다. 아래 tailscale 설치와
	// 랩 runcmd(예: curl -sfL get.k3s.io)가 curl 에 의존하므로 가장 먼저 실행한다. 이미 있으면 즉시
	// no-op 이라 베이크/stock AMI 의 부팅 속도엔 영향이 없다(멱등·best-effort).
	b.WriteString("  - sh -c 'miss=; for c in curl unzip; do command -v $c >/dev/null 2>&1 || miss=\"$miss $c\"; done; [ -n \"$miss\" ] && { apt-get update; DEBIAN_FRONTEND=noninteractive apt-get install -y $miss; }; true'\n")
	// SSM Agent — 채점(SendCommand) 경로. lab-base 에 deb 로 베이크돼 있으면 기동만 하면 돼 빠르다.
	// 베이크되지 않은 AMI 대비 snap 폴백을 둔다. 폴백에선 서비스 기동을 snap install 성공에 묶지
	// 않는다(Canonical AMI 처럼 snap 이 이미 깔린 경우 install 이 비정상 종료해도 서비스는 켜지게).
	b.WriteString("  - if systemctl enable --now amazon-ssm-agent 2>/dev/null; then :; else snap install amazon-ssm-agent --classic 2>/dev/null || true; systemctl enable --now snap.amazon-ssm-agent.amazon-ssm-agent.service 2>/dev/null || true; fi\n")
	if authKey != "" {
		hostname := tailnetHostname(cfg, sessionID)
		// tailscale 설치(베이스 AMI 가 packer 로 미리 굽지 않은 경우 대비) 후 가입. 가입은 설치 뒤에 와야 한다.
		b.WriteString("  - curl -fsSL https://tailscale.com/install.sh | sh || true\n")
		// --ssh: 검증엔진/사용자가 tailnet 경유 SSH 로 접속(virtctl 대체). --hostname: 결정적 MagicDNS 이름.
		fmt.Fprintf(&b, "  - tailscale up --ssh --hostname=%s --authkey=%s\n",
			yamlScalar(hostname), yamlScalar(authKey))
	}
	for _, cmd := range init.Runcmd {
		// runcmd 의 각 항목은 셸로 실행되는 단일 문자열로 둔다(content DSL 과 동일 계약).
		fmt.Fprintf(&b, "  - %s\n", yamlScalar(cmd))
	}
	// 브라우저 IDE(code-server) — best-effort 라 맨 마지막에 둔다. 무거운 다운로드라 채점·터미널·
	// 랩 초기화(init.Runcmd, 예: k3s 준비)를 막지 않도록 그 뒤에 설치한다. tailnet 경유 프록시라 authkey 필요.
	if authKey != "" {
		b.WriteString("  - curl -fsSL https://code-server.dev/install.sh | sh || true\n")
	}

	return b.String()
}

// tailnetHostname은 세션 인스턴스의 MagicDNS 호스트네임을 반환한다("<prefix>-<sessionID>").
// VMIAddress 가 돌려주는 주소와 동일해야 한다(프록시 도달 대상).
func tailnetHostname(cfg *config.AWSConfig, sessionID string) string {
	prefix := cfg.TailnetHostnamePrefix
	if prefix == "" {
		prefix = "lab"
	}
	return prefix + "-" + sessionID
}

// yamlScalar는 cloud-init YAML 스칼라를 안전하게 따옴표 처리한다.
// 명령/패키지 문자열에 콜론·따옴표 등이 섞여도 YAML 파싱이 깨지지 않게 큰따옴표로 감싸고 이스케이프한다.
func yamlScalar(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return `"` + r.Replace(s) + `"`
}
