package executor

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"

	"github.com/requset700k/cledyu/validation-engine/internal/model"
)

// 랩 VM SSH 접속 기본값. 세션 VM cloud-init이 user "lab"에 엔진 공개키를 authorized_keys로 넣고,
// 엔진은 대응하는 private key(LAB_SSH_KEY)로 접속한다. 미설정 시 키 없이 시도(로컬/테스트).
// 한 체크당 실행 상한은 checker가 ctx deadline으로 건다(checker.defaultCheckTimeout 또는
// Check.Timeout). 여기서 별도 상한을 또 걸면 per-check timeout이 20s로 잘리므로 걸지 않는다.
const defaultLabSSHUser = "lab"

// KubeVirtExecutor는 KubeVirt VM에 명령어를 실행하는 도구
// virtctl ssh(native client)를 통해 Kubernetes API 서버를 거쳐 VM에 접속
type KubeVirtExecutor struct {
	vmName    string // VM 이름 (예: "session-vm")
	namespace string // K8s 네임스페이스 (예: "lab-<sessionID>")
	sshUser   string // VM 로그인 사용자 (LAB_SSH_USER, 기본 "lab")
	sshKey    string // SSH private key 파일 경로 (LAB_SSH_KEY, 비면 키 미지정)
}

// newKubeVirtExecutor는 KubeVirtExecutor를 생성
// VM 이름과 네임스페이스가 없으면 어느 VM에 접속해야 할지 모르므로 에러를 반환
func newKubeVirtExecutor(vm model.VMSpec) (*KubeVirtExecutor, error) {
	if vm.Name == "" || vm.Namespace == "" {
		return nil, fmt.Errorf("KubeVirt VM은 Name과 Namespace가 필요합니다")
	}
	sshUser := os.Getenv("LAB_SSH_USER")
	if sshUser == "" {
		sshUser = defaultLabSSHUser
	}
	return &KubeVirtExecutor{
		vmName:    vm.Name,
		namespace: vm.Namespace,
		sshUser:   sshUser,
		sshKey:    os.Getenv("LAB_SSH_KEY"),
	}, nil
}

// Exec는 virtctl ssh로 VM에 접속해서 명령어를 실행하고 결과를 반환
// 실행 예시: virtctl ssh --username lab --namespace lab-abc --local-ssh-opts "..." --identity-file /etc/lab-ssh/id_ed25519 --command "stat -c %a /x" vmi/session-vm
func (e *KubeVirtExecutor) Exec(ctx context.Context, cmd string) (string, error) {
	// v1.8.2: --command/-c 로 명령 전달, vmi/ 접두어로 타깃 지정.
	// --local-ssh-opts: OpenSSH에 StrictHostKeyChecking=no, UserKnownHostsFile=/dev/null 전달해
	// 새 VM 접속 시 host key 프롬프트 없이 진행. kube-apiserver WebSocket TLS 터널이 보호하므로
	// lab 검증 용도에서 수용 가능.
	args := []string{
		"ssh",
		"--username", e.sshUser,
		"--namespace", e.namespace,
		"--local-ssh-opts", "-o StrictHostKeyChecking=no",
		"--local-ssh-opts", "-o UserKnownHostsFile=/dev/null",
	}
	if e.sshKey != "" {
		args = append(args, "--identity-file", e.sshKey)
	}
	args = append(args,
		"--command", cmd,
		"vmi/"+e.vmName,
	)

	// 실행 상한은 호출자(checker)가 ctx에 deadline으로 걸어둔다.
	// ctx 만료 시 CommandContext가 virtctl 프로세스를 종료한다.
	command := exec.CommandContext(ctx, "virtctl", args...)

	// 명령어 출력(stdout)과 에러(stderr)를 각각 수집한다.
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr

	if err := command.Run(); err != nil {
		return "", fmt.Errorf("virtctl 실행 실패: %w\nstderr: %s", err, stderr.String())
	}

	return stdout.String(), nil
}

// Close는 KubeVirt는 매번 새 연결을 쓰므로 닫을 것이 없다.
func (e *KubeVirtExecutor) Close() {}
