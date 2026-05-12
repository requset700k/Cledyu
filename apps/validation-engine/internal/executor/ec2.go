package executor

import (
	"context"
	"fmt"

	"github.com/requset700k/cledyu/validation-engine/internal/model"
)

// EC2Executor는 AWS EC2 VM에 SSM으로 접속해서 명령어를 실행한다.
// SSH 키 없이 AWS SSM Session Manager를 통해 접속한다.
type EC2Executor struct {
	instanceID string // EC2 인스턴스 ID (예: "i-0abc1234567890")
	region     string // AWS 리전 (예: "ap-northeast-2")
}

func newEC2Executor(vm model.VMSpec) (*EC2Executor, error) {
	if vm.InstanceID == "" || vm.Region == "" {
		return nil, fmt.Errorf("EC2 VM은 InstanceID와 Region이 필요합니다")
	}
	return &EC2Executor{
		instanceID: vm.InstanceID,
		region:     vm.Region,
	}, nil
}

// Exec는 AWS SSM SendCommand로 EC2에서 명령어를 실행한다.
// TODO: AWS SDK v2 연동 예정
func (e *EC2Executor) Exec(ctx context.Context, cmd string) (string, error) {
	return "", fmt.Errorf("EC2 executor 미구현 (instanceID: %s, cmd: %s)", e.instanceID, cmd)
}

// Close는 EC2도 매번 새 연결을 쓰므로 닫을 것이 없다.
func (e *EC2Executor) Close() {}
