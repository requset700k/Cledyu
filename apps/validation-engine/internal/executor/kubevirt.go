package executor

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"

	"github.com/requset700k/cledyu/validation-engine/internal/model"
)

// KubeVirtExecutor는 KubeVirt VM에 명령어를 실행하는 도구
// virtctl ssh를 통해 Kubernetes API 서버를 거쳐 VM에 접속
type KubeVirtExecutor struct {
	vmName    string // VM 이름 (예: "lab-vm-abc123")
	namespace string // K8s 네임스페이스 (예: "lab-sessions")
}

// newKubeVirtExecutor는 KubeVirtExecutor를 생성
// VM 이름과 네임스페이스가 없으면 어느 VM에 접속해야 할지 모르므로 에러를 반환
func newKubeVirtExecutor(vm model.VMSpec) (*KubeVirtExecutor, error) {
	if vm.Name == "" || vm.Namespace == "" {
		return nil, fmt.Errorf("KubeVirt VM은 Name과 Namespace가 필요합니다")
	}
	return &KubeVirtExecutor{
		vmName:    vm.Name,
		namespace: vm.Namespace,
	}, nil
}

// Exec는 virtctl ssh로 VM에 접속해서 명령어를 실행하고 결과를 반환
// 실행 예시: virtctl ssh lab-vm-abc123 -n lab-sessions -- nginx -t
func (e *KubeVirtExecutor) Exec(ctx context.Context, cmd string) (string, error) {
	// virtctl ssh <vm이름> -n <네임스페이스> -- <실행할명령어>
	args := []string{
		"ssh", e.vmName, // 어느 VM에 접속할지
		"-n", e.namespace, // VM이 어느 네임스페이스에 있는지
		"--", cmd, // VM 안에서 실행할 명령어
	}

	// os/exec로 virtctl 명령어를 실행한다.
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
