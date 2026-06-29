package handlers

import (
	"testing"

	"github.com/requset700k/cledyu/api/internal/content"
)

// toValidationChecks는 DSL의 per-check Timeout(초)을 검증엔진으로 전달하는 wire 구조체에
// 그대로 실어야 한다. 이게 빠지면 ansible step4 같은 긴 명령이 기본 20s에 잘린다.
func TestToValidationChecks_ForwardsTimeout(t *testing.T) {
	out := toValidationChecks([]content.Check{
		{Type: "command", Command: "ansible-playbook", Expect: "changed=0", Timeout: 60},
	})
	if len(out) != 1 {
		t.Fatalf("결과 개수 = %d, 1 기대", len(out))
	}
	if out[0].Timeout != 60 {
		t.Errorf("Timeout = %d, 60 기대", out[0].Timeout)
	}
}
