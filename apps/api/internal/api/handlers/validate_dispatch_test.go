package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/requset700k/cledyu/api/internal/config"
	"github.com/requset700k/cledyu/api/internal/validation"
	"go.uber.org/zap"
)

// fakeDispatcher는 발행된 검증 요청을 캡처하는 테스트용 Dispatcher다.
type fakeDispatcher struct {
	got []validation.Request
}

func (f *fakeDispatcher) Publish(_ context.Context, req validation.Request) error {
	f.got = append(f.got, req)
	return nil
}

// TestValidateStep_PublishesRequest는 ValidateStep이 실제 lab 콘텐츠의 checks로
// 올바른 ValidationRequest(VM 타겟·step·cmd 보존)를 발행하는지 검증한다.
func TestValidateStep_PublishesRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)

	fd := &fakeDispatcher{}
	h := New(&config.Config{}, zap.NewNop(), nil, fd)
	if _, ok := h.labs["lab-linux-basics"]; !ok {
		t.Fatal("lab-linux-basics 콘텐츠가 로드되지 않음")
	}

	// 세션 스텝 상태를 직접 시드(클러스터 없이 ValidateStep 단독 테스트).
	h.steps.m["sess-1"] = &sessionSteps{
		LabID:       "lab-linux-basics",
		Steps:       []stepState{{StepID: 1, Status: "active"}, {StepID: 2, Status: "pending"}, {StepID: 3, Status: "pending"}},
		CurrentStep: 1,
	}

	// step 3 = command 체크(stat … expect 600) — cmd 보존이 핵심.
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/sessions/sess-1/validate", strings.NewReader(`{"step_id":3}`))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = gin.Params{{Key: "id", Value: "sess-1"}}

	h.ValidateStep(c)

	// 검증엔진 연동 시 즉시 통과가 아니라 validating(202)으로 응답하고, 결과는 비동기로 확정된다.
	if w.Code != http.StatusAccepted {
		t.Fatalf("기대 202, 실제 %d", w.Code)
	}
	if got := h.steps.m["sess-1"].Steps[2].Status; got != "validating" {
		t.Errorf("step3 상태 = %q, 기대 validating", got)
	}
	if len(fd.got) != 1 {
		t.Fatalf("검증 요청 1건 발행 기대, 실제 %d건", len(fd.got))
	}
	req := fd.got[0]
	if req.SessionID != "sess-1" || req.StepID != 3 {
		t.Errorf("session/step 불일치: %+v", req)
	}
	if req.VM.Type != validation.VMTypeKubeVirt || req.VM.Name != "session-vm" || req.VM.Namespace != "lab-sess-1" {
		t.Errorf("VM 타겟 불일치: %+v", req.VM)
	}
	if len(req.Checks) != 1 {
		t.Fatalf("체크 1개 기대, 실제 %d개", len(req.Checks))
	}
	if req.Checks[0].Type != "command" || req.Checks[0].Command == "" {
		t.Errorf("command 체크의 cmd가 유실됨: %+v", req.Checks[0])
	}

	// 와이어 직렬화 시 `cmd` 키로 나가는지(검증엔진 model.Check와 정렬) 확인.
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(raw), `"cmd"`) {
		t.Errorf("발행 바이트에 \"cmd\" 키 없음: %s", raw)
	}
}

// TestValidateStep_NoDispatcher는 dispatch가 nil이어도(Kafka 미설정) ValidateStep이
// 정상 동작(mock 통과)하는지 검증한다.
func TestValidateStep_NoDispatcher(t *testing.T) {
	gin.SetMode(gin.TestMode)

	h := New(&config.Config{}, zap.NewNop(), nil, nil)
	h.steps.m["sess-2"] = &sessionSteps{
		LabID:       "lab-linux-basics",
		Steps:       []stepState{{StepID: 1, Status: "active"}},
		CurrentStep: 1,
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/sessions/sess-2/validate", strings.NewReader(`{"step_id":1}`))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = gin.Params{{Key: "id", Value: "sess-2"}}

	h.ValidateStep(c)

	if w.Code != http.StatusOK {
		t.Fatalf("기대 200, 실제 %d", w.Code)
	}
}
