package ec2

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"

	"github.com/requset700k/cledyu/api/internal/session"
)

type fakeMinter struct {
	key   string
	err   error
	calls int
}

func (m *fakeMinter) Mint(_ context.Context) (string, error) {
	m.calls++
	return m.key, m.err
}

// minter 가 있으면 세션마다 발급한 one-off 키가 user-data 에 들어가고, 정적 키는 안 쓰인다.
func TestCreate_UsesMintedSessionKey(t *testing.T) {
	f := &fakeEC2{}
	p := newWithAPI(f, testCfg(), nil, nil) // testCfg 는 정적 TailscaleAuthKey=tskey-auth-test
	m := &fakeMinter{key: "tskey-auth-minted999"}
	p.minter = m

	if _, err := p.Create(context.Background(), "abc123", "lab-x", "user-1", session.BootInit{}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if m.calls != 1 {
		t.Errorf("minter 호출 %d회, want 1", m.calls)
	}
	ud := decodeUserData(t, aws.ToString(f.runInputs[0].UserData))
	if !strings.Contains(ud, "tskey-auth-minted999") {
		t.Errorf("user-data 에 발급된 세션 키가 없음:\n%s", ud)
	}
	if strings.Contains(ud, "tskey-auth-test") {
		t.Errorf("발급 성공인데 정적 authkey 가 user-data 에 들어감:\n%s", ud)
	}
}

// 발급 실패 시 fail-secure: 정적 reusable 키로 폴백하지 않고 tailscale 자체를 생략한다
// (세션 프로비저닝·SSM 채점은 계속). 유출 위험 있는 정적 키를 안 쓰는 게 핵심(issue #307).
func TestCreate_MintFailure_FailSecure(t *testing.T) {
	f := &fakeEC2{}
	p := newWithAPI(f, testCfg(), nil, nil)
	p.minter = &fakeMinter{err: errors.New("boom")}

	if _, err := p.Create(context.Background(), "abc123", "lab-x", "user-1", session.BootInit{}); err != nil {
		t.Fatalf("Create 는 발급 실패해도 세션 프로비저닝을 계속해야 함: %v", err)
	}
	ud := decodeUserData(t, aws.ToString(f.runInputs[0].UserData))
	if strings.Contains(ud, "tailscale up") {
		t.Errorf("발급 실패인데 tailscale 가입이 포함됨(정적 키 폴백?):\n%s", ud)
	}
}
