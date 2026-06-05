package validation

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/requset700k/cledyu/api/internal/content"
)

// TestRequest_WireSchema는 발행 메시지가 검증엔진 스키마와 정렬되는지 확인한다:
// vm 필드, command 체크의 `cmd` 키, file_content 체크의 path/expect.
func TestRequest_WireSchema(t *testing.T) {
	req := Request{
		SessionID: "sess-1",
		StepID:    3,
		VM:        VMSpec{Type: VMTypeKubeVirt, Name: "session-vm", Namespace: "lab-sess-1"},
		Checks: []content.Check{
			{Type: "command", Command: "stat -c %a /home/lab/work/notes.txt", Expect: "600"},
			{Type: "file_content", Path: "/home/lab/work/notes.txt", Expect: "cledyu"},
		},
	}

	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(raw)

	for _, want := range []string{
		`"session_id":"sess-1"`,
		`"step_id":3`,
		`"type":"kubevirt"`,
		`"name":"session-vm"`,
		`"namespace":"lab-sess-1"`,
		`"cmd":"stat -c %a /home/lab/work/notes.txt"`, // command, not "command"
		`"path":"/home/lab/work/notes.txt"`,
		`"expect":"600"`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("발행 바이트에 %s 없음\n전체: %s", want, s)
		}
	}
	// command 체크가 옛 `command` 키로 새지 않는지(드리프트 회귀 방지).
	if strings.Contains(s, `"command":`) {
		t.Errorf("command 체크가 `command` 키로 직렬화됨(엔진은 `cmd` 기대): %s", s)
	}
}
