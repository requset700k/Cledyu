package executor

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/aws/aws-sdk-go-v2/service/ssm/types"
	"github.com/requset700k/cledyu/validation-engine/internal/model"
)

// ec2DefaultTimeout: timeout 미지정 체크의 기본 상한. SSM은 SendCommand 후 결과를 비동기로
// 폴링하므로 전송+실행 왕복이 수십 초~분 단위일 수 있다. KubeVirt(20s)보다 넉넉히 잡아,
// 명시 timeout이 없어도 consumer handlerTimeout(5분) 안에서 완료될 수 있게 한다.
const ec2DefaultTimeout = 5 * time.Minute

// ec2PollInterval: 명령 완료를 기다리는 폴링 간격.
//
// ⚠️ **SendCommand 직후 첫 폴링에도 쓰인다** — 그게 이 값이 2초에서 내려온 이유다(2026-07-16).
// SSM은 SendCommand가 200을 준 **직후엔 invocation을 조회할 수 없다**(전파 지연) → 첫 폴링이
// InvocationDoesNotExist로 떨어진다. 그때마다 2초를 통째로 기다리면 체크 6개짜리 랩이
// 12초를 그냥 버린다. 실측상 전파는 보통 수백 ms라 500ms면 대개 1~2회로 붙는다.
const ec2PollInterval = 500 * time.Millisecond

// ssmAPI는 EC2Executor가 쓰는 SSM 호출만 추린 인터페이스다.
// *ssm.Client를 직접 들고 있으면 전파 지연 재시도 같은 폴링 로직을 단위 테스트할 수 없다
// — 이 인터페이스가 있어야 mock으로 InvocationDoesNotExist를 주입해 회귀를 막을 수 있다.
type ssmAPI interface {
	SendCommand(context.Context, *ssm.SendCommandInput, ...func(*ssm.Options)) (*ssm.SendCommandOutput, error)
	GetCommandInvocation(context.Context, *ssm.GetCommandInvocationInput, ...func(*ssm.Options)) (*ssm.GetCommandInvocationOutput, error)
}

// EC2Executor는 AWS EC2 VM에 SSH 키 없이 SSM(AWS Systems Manager)으로 명령을 실행한다.
// SSM은 AWS가 제공하는 원격 명령 실행 서비스로, EC2 인스턴스에 SSM Agent가 설치돼 있으면
// SSH 없이도 AWS API를 통해 명령을 보낼 수 있다.
type EC2Executor struct {
	instanceID string // 명령을 실행할 EC2 인스턴스 ID (예: "i-0abc1234567890")
	client     ssmAPI // AWS SSM API 클라이언트 (테스트는 mock 주입)

	// pollInterval은 명령 완료를 기다리는 간격이다. 테스트가 짧게 줄인다.
	pollInterval time.Duration
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
		instanceID:   vm.InstanceID,
		client:       ssm.NewFromConfig(cfg),
		pollInterval: ec2PollInterval,
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
			// 🔴 **InvocationDoesNotExist는 실패가 아니라 "아직 전파 전"이다** (2026-07-16 실측).
			//
			// SSM은 SendCommand가 200을 준 직후에도 invocation을 곧바로 조회하게 해주지 않는다.
			// Go에서 두 호출은 ~5ms 간격이라 **첫 폴링이 사실상 100% 이 에러**로 떨어진다.
			// 이걸 에러로 반환하면 명령은 EC2에서 정상 실행되는데(SSM 이력상 Success) 우리만
			// 포기하고, checker가 그걸 "파일 없음"으로 렌더해 **맞게 푼 사용자가 계속 틀렸다고
			// 나온다.** DR 랩(EC2 채점)에서 CloudTrail로 확인: GetCommandInvocation 12건이
			// 전부 errorCode=InvocationDoesNotExist인데 대응 명령 12건은 전부 Success였다.
			//
			// 같은 함정을 DR 자식 상태 머신(dr-orchestration.tf)에서도 밟아 Retry로 막아뒀다 —
			// "명령 직후엔 InvocationDoesNotExist 가 날 수 있다(전파)". 여기만 방어가 없었다.
			//
			// ⚠️ 이 에러**만** 삼킨다. AccessDenied·InvalidInstanceId 등은 기다려도 안 낫는
			//    진짜 실패라 즉시 올려야 한다(삼키면 ctx deadline까지 매달린다).
			var notYet *types.InvocationDoesNotExist
			if !errors.As(err, &notYet) {
				return "", fmt.Errorf("SSM GetCommandInvocation 실패: %w", err)
			}
			if werr := e.wait(ctx); werr != nil {
				return "", werr
			}
			continue
		}

		status := aws.ToString(result.StatusDetails)
		switch status {
		case "Success":
			// 명령 성공 — stdout 반환
			return aws.ToString(result.StandardOutputContent), nil
		case "Failed", "Cancelled", "TimedOut", "DeliveryTimedOut":
			// 명령이 **실행됐고** 실패한 것 — 체크 관점에선 "조건 불충족"이다(예: test -d → exit 1).
			// 인프라 오류와 구분되도록 ErrCommandFailed로 감싼다(checker가 errors.Is로 가른다).
			return "", fmt.Errorf("%w (%s): %s", ErrCommandFailed, status, aws.ToString(result.StandardErrorContent))
		default:
			// Pending, InProgress 등 아직 실행 중
			if werr := e.wait(ctx); werr != nil {
				return "", werr
			}
		}
	}
}

// wait은 폴링 간격만큼 쉬되 ctx 취소를 존중한다.
func (e *EC2Executor) wait(ctx context.Context) error {
	interval := e.pollInterval
	if interval <= 0 {
		interval = ec2PollInterval
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(interval):
		return nil
	}
}

// Close는 EC2도 매번 새 API 호출을 쓰므로 닫을 것이 없다.
func (e *EC2Executor) Close() {}
