package executor

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/requset700k/cledyu/validation-engine/internal/model"
)

// 랩 VM SSH 접속 기본값. 세션 VM cloud-init이 user "lab"에 엔진 공개키를 authorized_keys로 넣고,
// 엔진은 대응하는 private key(LAB_SSH_KEY)로 접속한다. 미설정 시 키 없이 시도(로컬/테스트).
const (
	defaultLabSSHUser = "lab"
	// execTimeout: virtctl ssh가 인증/연결 단계에서 멈출 때 30s+ 매달리지 않도록 한 체크당 상한.
	execTimeout = 20 * time.Second
)

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
// 실행 예시: virtctl ssh --username lab --identity-file /etc/lab-ssh/id_ed25519 session-vm -n lab-abc -- stat -c %a /x
func (e *KubeVirtExecutor) Exec(ctx context.Context, cmd string) (string, error) {
	// distroless 이미지에는 ssh 바이너리가 없으므로 virtctl native client(기본)를 쓴다.
	// --username/--identity-file로 인증한다(미설정 시 키 없이 시도).
	args := []string{"ssh", "--username", e.sshUser}
	if e.sshKey != "" {
		args = append(args, "--identity-file", e.sshKey)
	}
	args = append(args,
		e.vmName,          // 어느 VM에 접속할지
		"-n", e.namespace, // VM이 어느 네임스페이스에 있는지
		"--", cmd, // VM 안에서 실행할 명령어
	)

	// 인증/연결 단계에서 멈추는 경우를 대비해 한 체크당 타임아웃을 건다.
	runCtx, cancel := context.WithTimeout(ctx, execTimeout)
	defer cancel()
	command := exec.CommandContext(runCtx, "virtctl", args...)

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
