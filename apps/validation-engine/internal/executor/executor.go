// Package executor는 VM에 접속해서 명령어를 실행하는 추상화 레이어다.
// KubeVirt VM과 EC2 VM 두 종류를 지원하며, 외부에서는 VMExecutor 인터페이스만 쓴다.
package executor

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/requset700k/cledyu/validation-engine/internal/model"
)

// ErrCommandFailed는 **명령이 VM에서 실제로 실행됐고 0이 아닌 상태로 끝났다**는 뜻이다.
//
// 이 구분이 왜 필요한가(2026-07-16 실측): checker는 `test -d <path>`의 실패를 "디렉터리 없음"으로
// 렌더한다. 그런데 이전엔 Exec의 **모든** 에러가 그 문구로 뭉개졌다 — SSM 전파 지연도, AccessDenied도,
// 네트워크 오류도 전부 "디렉터리 없음". 그래서 DR 랩에서 **사용자가 맞게 풀었는데 계속 틀렸다고
// 나오는데 로그·결과·UI 어디에도 진짜 원인이 없었고**, CloudTrail을 뒤져서야 원인을 찾았다.
//
//   - ErrCommandFailed      → 조건 불충족(진짜 "없음"). 사용자에게 그렇게 말해도 된다.
//   - 그 외 에러            → 인프라 문제. **"없음"이라고 말하면 거짓말**이고 사용자를 오도한다.
var ErrCommandFailed = errors.New("VM 명령이 실패 상태로 끝남")

// VMExecutor는 VM에서 명령어를 실행하는 인터페이스다.
// KubeVirt든 EC2든 이 인터페이스만 알면 쓸 수 있다.
type VMExecutor interface {
	// Exec는 VM 안에서 명령어를 실행하고 출력 결과를 돌려준다.
	Exec(ctx context.Context, cmd string) (output string, err error)

	// DefaultTimeout은 체크가 timeout을 지정하지 않았을 때 쓸 한 체크당 기본 상한이다.
	// 백엔드 특성에 따라 다르다(KubeVirt virtctl ssh는 짧게, EC2 SSM은 전송+폴링이 길어 넉넉히).
	DefaultTimeout() time.Duration

	// Close는 VM 연결을 닫는다.
	Close()
}

// New는 VMSpec을 보고 알맞은 VMExecutor를 만들어서 돌려준다.
// vm.Type이 "kubevirt"면 KubeVirtExecutor, "ec2"면 EC2Executor를 반환한다.
func New(vm model.VMSpec) (VMExecutor, error) {
	switch vm.Type {
	case model.VMTypeKubeVirt:
		return newKubeVirtExecutor(vm)
	case model.VMTypeEC2:
		return newEC2Executor(vm)
	default:
		return nil, fmt.Errorf("알 수 없는 VM 타입: %s", vm.Type)
	}
}
