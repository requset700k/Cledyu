package ec2

import (
	"fmt"
	"strings"

	"github.com/requset700k/cledyu/api/internal/config"
	"github.com/requset700k/cledyu/api/internal/session"
)

// renderCloudInit은 세션 EC2 인스턴스의 cloud-init user-data(#cloud-config)를 만든다.
//
// 베이스 AMI(W1 terraform/packer)에 SSM Agent·code-server·tailscale 바이너리가 이미 설치돼 있다고
// 가정하고, 여기서는 세션별로만:
//   - tailnet 가입: authkey 가 있으면 "<prefix>-<sessionID>" MagicDNS 호스트네임으로 tailscale up.
//     이렇게 붙은 호스트네임으로 API/검증엔진이 인스턴스에 도달한다(라이브 터미널/IDE 프록시·SSH).
//   - 랩별 초기화: BootInit 의 packages(apt) 설치와 runcmd 실행.
//
// authkey 가 비면 tailscale 가입을 생략한다 — SSM 채점은 여전히 동작하지만 라이브 터미널/IDE 는 불가.
func renderCloudInit(sessionID string, cfg *config.AWSConfig, init session.BootInit) string {
	var b strings.Builder
	b.WriteString("#cloud-config\n")

	if len(init.Packages) > 0 {
		b.WriteString("package_update: true\n")
		b.WriteString("packages:\n")
		for _, p := range init.Packages {
			fmt.Fprintf(&b, "  - %s\n", yamlScalar(p))
		}
	}

	b.WriteString("runcmd:\n")
	if cfg.TailscaleAuthKey != "" {
		hostname := tailnetHostname(cfg, sessionID)
		// --ssh: 검증엔진/사용자가 tailnet 경유 SSH 로 접속(virtctl 대체). --hostname: 결정적 MagicDNS 이름.
		fmt.Fprintf(&b, "  - tailscale up --ssh --hostname=%s --authkey=%s\n",
			yamlScalar(hostname), yamlScalar(cfg.TailscaleAuthKey))
	}
	for _, cmd := range init.Runcmd {
		// runcmd 의 각 항목은 셸로 실행되는 단일 문자열로 둔다(content DSL 과 동일 계약).
		fmt.Fprintf(&b, "  - %s\n", yamlScalar(cmd))
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
