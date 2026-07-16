package executor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/requset700k/cledyu/validation-engine/internal/model"
)

// 랩 VM SSH 접속 기본값. 세션 VM cloud-init이 user "lab"에 엔진 공개키를 authorized_keys로 넣고,
// 엔진은 대응하는 private key(LAB_SSH_KEY)로 접속한다. 미설정 시 키 없이 시도(로컬/테스트).
// 한 체크당 실행 상한은 checker가 ctx deadline으로 건다(Check.Timeout 또는 DefaultTimeout).
// 여기서 별도 상한을 또 걸면 per-check timeout이 잘리므로 Exec에서는 걸지 않는다.
const (
	defaultLabSSHUser = "lab"
	// kubevirtDefaultTimeout: virtctl ssh가 인증/연결 단계에서 멈출 때 오래 매달리지 않도록 한
	// 한 체크당 기본 상한. timeout을 더 길게 줘야 하는 명령(ansible-playbook 등)은 Check.Timeout으로 늘린다.
	kubevirtDefaultTimeout = 20 * time.Second
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
		// ⚠️ ctx 만료를 **먼저** 본다. CommandContext 는 ctx 가 끝나면 프로세스를 SIGKILL 하고
		// Run 은 "signal: killed" **ExitError** 를 준다 — ctx.Err() 를 체인에 넣어주지 않는다.
		// 그래서 이 줄이 없으면 checker 의 DeadlineExceeded 분기가 영영 안 걸리고, 타임아웃이
		// 아래 ErrCommandFailed 로 흘러 "파일 없음" 으로 둔갑한다(EC2 에서 겪은 것과 같은 오분류).
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", fmt.Errorf("virtctl 실행 중단: %w\nstderr: %s", ctxErr, stderr.String())
		}

		// 명령이 VM 에서 **실행됐고** 0 이 아닌 상태로 끝난 경우(예: test -d → exit 1 = 없음).
		// 이건 "조건 불충족" 이지 인프라 오류가 아니다 → checker 가 "없음" 으로 렌더해도 참이다.
		//
		// ⚠️ ssh 관례상 **exit 255 는 ssh 자체 실패**(연결·인증)라 여기서 갈라내는 게 더 정확하다.
		// 그러나 virtctl 이 그 관례를 그대로 전달하는지 **확인하지 못했다**(이 환경엔 KubeVirt 가 없다).
		// 지어내지 않고 **현행 동작을 그대로 보존**한다 — 255 를 인프라로 가르는 건 온프렘에서
		// 실측한 뒤에 한다. (지금도 그 경우는 "없음" 으로 나오지만, 그건 이 커밋 이전과 동일하다.)
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return "", fmt.Errorf("%w: virtctl exit %d\nstderr: %s", ErrCommandFailed, exitErr.ExitCode(), stderr.String())
		}

		// virtctl 바이너리 부재·spawn 실패 등 — 명령을 실행조차 못 했다.
		return "", fmt.Errorf("virtctl 실행 실패: %w\nstderr: %s", err, stderr.String())
	}

	return stdout.String(), nil
}

// DefaultTimeout은 timeout 미지정 체크의 기본 상한이다(virtctl ssh는 짧게 끊는다).
func (e *KubeVirtExecutor) DefaultTimeout() time.Duration { return kubevirtDefaultTimeout }

// Close는 KubeVirt는 매번 새 연결을 쓰므로 닫을 것이 없다.
func (e *KubeVirtExecutor) Close() {}
