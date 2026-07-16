package executor

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/aws/aws-sdk-go-v2/service/ssm/types"
)

// fakeSSM은 SendCommand/GetCommandInvocation 응답을 대본대로 돌려준다.
type fakeSSM struct {
	sendErr error
	// getSeq: GetCommandInvocation 호출마다 앞에서부터 하나씩 소비한다.
	getSeq  []getResp
	getCall int
}

type getResp struct {
	status string // StatusDetails ("" 면 err 를 쓴다)
	stdout string
	stderr string
	err    error
}

func (f *fakeSSM) SendCommand(context.Context, *ssm.SendCommandInput, ...func(*ssm.Options)) (*ssm.SendCommandOutput, error) {
	if f.sendErr != nil {
		return nil, f.sendErr
	}
	return &ssm.SendCommandOutput{Command: &types.Command{CommandId: aws.String("cmd-1")}}, nil
}

func (f *fakeSSM) GetCommandInvocation(context.Context, *ssm.GetCommandInvocationInput, ...func(*ssm.Options)) (*ssm.GetCommandInvocationOutput, error) {
	if f.getCall >= len(f.getSeq) {
		return nil, errors.New("대본 초과 — GetCommandInvocation 이 예상보다 많이 불렸다")
	}
	r := f.getSeq[f.getCall]
	f.getCall++
	if r.err != nil {
		return nil, r.err
	}
	return &ssm.GetCommandInvocationOutput{
		StatusDetails:         aws.String(r.status),
		StandardOutputContent: aws.String(r.stdout),
		StandardErrorContent:  aws.String(r.stderr),
	}, nil
}

func newTestExec(f *fakeSSM) *EC2Executor {
	return &EC2Executor{instanceID: "i-test", client: f, pollInterval: time.Millisecond}
}

// 🔴 회귀 테스트 — 2026-07-16 DR 랩에서 실제로 터진 버그.
//
// SSM 은 SendCommand 직후 invocation 을 곧바로 조회하게 해주지 않는다(전파 지연). Go 에선 두 호출이
// ~5ms 간격이라 **첫 폴링이 사실상 항상** InvocationDoesNotExist 다. 이걸 에러로 반환하면 명령은
// EC2 에서 정상 실행되는데 우리만 포기하고, checker 가 "파일 없음" 으로 렌더해 **맞게 푼 사용자가
// 계속 틀렸다고 나온다.** CloudTrail 실측: GetCommandInvocation 12건 전부 InvocationDoesNotExist,
// 대응 SSM 명령 12건은 전부 Success.
func TestExec_InvocationDoesNotExist_는_실패가_아니라_전파대기(t *testing.T) {
	f := &fakeSSM{getSeq: []getResp{
		{err: &types.InvocationDoesNotExist{}}, // 첫 폴링 — 아직 전파 전
		{err: &types.InvocationDoesNotExist{}}, // 두 번째도 아직
		{status: "Success", stdout: "hello"},   // 이제 붙었다
	}}
	out, err := newTestExec(f).Exec(context.Background(), "test -d /home/lab/work")
	if err != nil {
		t.Fatalf("전파 지연은 삼키고 폴링해야 한다. err=%v", err)
	}
	if out != "hello" {
		t.Errorf("stdout=%q, want %q", out, "hello")
	}
	if f.getCall != 3 {
		t.Errorf("GetCommandInvocation 호출 %d회, want 3", f.getCall)
	}
}

// 명령이 실제로 실행돼 실패한 경우(예: test -d → exit 1)는 ErrCommandFailed 로 와야 한다.
// checker 가 이걸로 "진짜 없음" 과 인프라 오류를 가른다.
func TestExec_명령실패는_ErrCommandFailed(t *testing.T) {
	f := &fakeSSM{getSeq: []getResp{{status: "Failed", stderr: "no such file"}}}
	_, err := newTestExec(f).Exec(context.Background(), "test -d /nope")
	if !errors.Is(err, ErrCommandFailed) {
		t.Fatalf("ErrCommandFailed 로 감싸야 한다. err=%v", err)
	}
}

// ⚠️ 전파 지연이 아닌 에러(AccessDenied 등)는 기다려도 안 낫는다 → 즉시 올려야 한다.
// 이것까지 삼키면 ctx deadline 까지 매달려 랩 하나가 5분씩 멈춘다.
func TestExec_전파지연이_아닌_에러는_즉시_반환(t *testing.T) {
	boom := errors.New("AccessDeniedException")
	f := &fakeSSM{getSeq: []getResp{{err: boom}}}
	_, err := newTestExec(f).Exec(context.Background(), "test -d /x")
	if !errors.Is(err, boom) {
		t.Fatalf("전파 지연이 아닌 에러는 그대로 올려야 한다. err=%v", err)
	}
	if errors.Is(err, ErrCommandFailed) {
		t.Error("인프라 오류를 ErrCommandFailed 로 오분류하면 안 된다")
	}
	if f.getCall != 1 {
		t.Errorf("재시도하면 안 된다. 호출 %d회", f.getCall)
	}
}

// InProgress 는 계속 폴링한다(기존 동작 유지).
func TestExec_InProgress_는_계속_폴링(t *testing.T) {
	f := &fakeSSM{getSeq: []getResp{
		{status: "Pending"},
		{status: "InProgress"},
		{status: "Success", stdout: "done"},
	}}
	out, err := newTestExec(f).Exec(context.Background(), "sleep 1")
	if err != nil || out != "done" {
		t.Fatalf("out=%q err=%v", out, err)
	}
}

// ctx 취소는 존중한다 — 폴링 루프가 영원히 돌면 consumer 가 막힌다.
func TestExec_ctx_취소_존중(t *testing.T) {
	f := &fakeSSM{getSeq: []getResp{
		{err: &types.InvocationDoesNotExist{}},
		{err: &types.InvocationDoesNotExist{}},
		{err: &types.InvocationDoesNotExist{}},
	}}
	e := &EC2Executor{instanceID: "i-test", client: f, pollInterval: 50 * time.Millisecond}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	_, err := e.Exec(ctx, "test -d /x")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ctx deadline 을 올려야 한다. err=%v", err)
	}
}
