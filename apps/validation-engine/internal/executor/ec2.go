package executor

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"
	"github.com/requset700k/cledyu/validation-engine/internal/model"
)

// ec2DefaultTimeout: timeout 미지정 체크의 기본 상한. SSM은 SendCommand 후 결과를 비동기로
// 폴링하므로 전송+실행 왕복이 수십 초~분 단위일 수 있다. KubeVirt(20s)보다 넉넉히 잡아,
// 명시 timeout이 없어도 consumer handlerTimeout(5분) 안에서 완료될 수 있게 한다.
const ec2DefaultTimeout = 5 * time.Minute

// EC2Executor는 AWS EC2 VM에 SSH 키 없이 SSM(AWS Systems Manager)으로 명령을 실행한다.
// SSM은 AWS가 제공하는 원격 명령 실행 서비스로, EC2 인스턴스에 SSM Agent가 설치돼 있으면
// SSH 없이도 AWS API를 통해 명령을 보낼 수 있다.
type EC2Executor struct {
	instanceID string      // 명령을 실행할 EC2 인스턴스 ID (예: "i-0abc1234567890")
	client     *ssm.Client // AWS SSM API 클라이언트
}

// DefaultTimeout은 timeout 미지정 체크의 기본 상한이다(SSM 왕복이 길어 KubeVirt보다 넉넉히).
func (e *EC2Executor) DefaultTimeout() time.Duration { return ec2DefaultTimeout }

// newEC2Executor는 EC2Executor를 생성한다.
// VMSpec에서 인스턴스 ID와 리전을 읽어 AWS SSM 클라이언트를 초기화한다.
// AWS 인증은 Pod에 연결된 IAM Role(IRSA)을 자동으로 사용한다 — 별도 키 설정 불필요.
func newEC2Executor(vm model.VMSpec) (*EC2Executor, error) {
	if vm.InstanceID == "" || vm.Region == "" {
		return nil, fmt.Errorf("EC2 VM은 InstanceID와 Region이 필요합니다")
	}

	// AWS SDK 기본 설정 로드 — 리전만 지정하고 인증은 IRSA(IAM Role for Service Account)에서 자동 처리
	cfg, err := config.LoadDefaultConfig(context.Background(),
		config.WithRegion(vm.Region),
	)
	if err != nil {
		return nil, fmt.Errorf("AWS 설정 로드 실패: %w", err)
	}

	return &EC2Executor{
		instanceID: vm.InstanceID,
		client:     ssm.NewFromConfig(cfg),
	}, nil
}

// Exec는 SSM SendCommand로 EC2에 명령을 보내고 완료될 때까지 기다린 뒤 출력을 반환한다.
//
// 흐름:
//  1. SendCommand: AWS에 "이 EC2에서 이 명령 실행해줘" 요청 → 커맨드 ID 발급
//  2. GetCommandInvocation: 커맨드 ID로 실행 결과 폴링 (2초 간격)
//  3. Success면 stdout 반환, 실패면 에러 반환
func (e *EC2Executor) Exec(ctx context.Context, cmd string) (string, error) {
	// AWS-RunShellScript: EC2에서 셸 명령을 실행하는 SSM 기본 문서
	out, err := e.client.SendCommand(ctx, &ssm.SendCommandInput{
		InstanceIds:  []string{e.instanceID},
		DocumentName: aws.String("AWS-RunShellScript"),
		Parameters:   map[string][]string{"commands": {cmd}},
	})
	if err != nil {
		return "", fmt.Errorf("SSM SendCommand 실패: %w", err)
	}

	commandID := aws.ToString(out.Command.CommandId)

	// SSM은 비동기로 명령을 실행하므로 완료될 때까지 폴링한다.
	// 실행 상한은 호출자(checker)가 ctx deadline 으로 건다(기본 20s, Check.Timeout 으로 조정).
	// 그 시간 안에 완료되지 않으면 ctx.Done()으로 빠져나간다.
	for {
		result, err := e.client.GetCommandInvocation(ctx, &ssm.GetCommandInvocationInput{
			CommandId:  aws.String(commandID),
			InstanceId: aws.String(e.instanceID),
		})
		if err != nil {
			// SendCommand 직후엔 invocation 이 아직 전파되지 않아 GetCommandInvocation 이
			// InvocationDoesNotExist 를 반환한다(SSM eventual consistency, 통상 ~1-2s). 이는
			// 실패가 아니라 "아직 준비 안 됨"이므로 Pending/InProgress 와 동일하게 재시도한다.
			// 이걸 치명적 에러로 처리하면(과거 동작) 명령이 인스턴스에서 실제로 성공/실패해도
			// 결과를 못 읽어 모든 EC2 채점이 항상 fail 로 떨어진다 — 2026-07-14 DR 드릴서 발견
			// (SSM 이력은 Success 인데 검증 결과는 passed=false, duration<0.5s 로 폴링 대기 없이 즉시 실패).
			var notReady *ssmtypes.InvocationDoesNotExist
			if errors.As(err, &notReady) {
				select {
				case <-ctx.Done():
					return "", ctx.Err()
				case <-time.After(2 * time.Second):
				}
				continue
			}
			return "", fmt.Errorf("SSM GetCommandInvocation 실패: %w", err)
		}

		status := aws.ToString(result.StatusDetails)
		switch status {
		case "Success":
			// 명령 성공 — stdout 반환
			return aws.ToString(result.StandardOutputContent), nil
		case "Failed", "Cancelled", "TimedOut", "DeliveryTimedOut":
			// 명령 실패 — stderr 포함해서 에러 반환
			return "", fmt.Errorf("SSM 명령 실패 (%s): %s", status, aws.ToString(result.StandardErrorContent))
		default:
			// Pending, InProgress 등 아직 실행 중 — 2초 후 다시 확인
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(2 * time.Second):
			}
		}
	}
}

// Close는 EC2도 매번 새 API 호출을 쓰므로 닫을 것이 없다.
func (e *EC2Executor) Close() {}
