package handlers

import (
	"net/http"
	"testing"

	"github.com/requset700k/cledyu/api/internal/config"
	"github.com/requset700k/cledyu/api/internal/content"
	"go.uber.org/zap"
)

// validatorNilHandler는 validator 가 nil 인(=DR: validation-engine 미배포) Handler 를 주어진 server mode 로 만든다.
func validatorNilHandler(t *testing.T, mode string) *Handler {
	t.Helper()
	labs, err := content.Load()
	if err != nil {
		t.Fatalf("load lab content: %v", err)
	}
	h := &Handler{log: zap.NewNop(), labs: labs, steps: newStepStore(nil, nil)}
	h.cfg = &config.Config{Server: config.ServerConfig{Mode: mode}}
	h.steps.m["s1"] = &sessionSteps{
		LabID:  "lab-linux-basics",
		UserID: "alice",
		Steps:  []stepState{{StepID: 1, Status: "active"}},
	}
	return h
}

// release 모드 + validator nil → mock-pass 금지, fail-closed 503 이어야 한다(무검증 통과 차단, Codex P1).
func TestValidateStep_ReleaseNilValidator_503(t *testing.T) {
	h := validatorNilHandler(t, "release")
	r := ownershipRouter(h, "alice")
	w := doJSON(r, http.MethodPost, "/sessions/s1/validate", map[string]any{"step_id": 1})
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("release+validator nil: expected 503, got %d: %s", w.Code, w.Body.String())
	}
	// 스텝이 passed 로 변조되지 않았는지 확인(mock-pass 가 실행되면 안 됨).
	if st := h.steps.m["s1"].Steps[0].Status; st == "passed" {
		t.Fatalf("스텝이 무검증 passed 로 변조됨 — fail-closed 실패")
	}
}

// debug/로컬 모드는 기존대로 mock-pass(200) — 개발 편의 보존.
func TestValidateStep_DebugNilValidator_MockPass(t *testing.T) {
	h := validatorNilHandler(t, "debug")
	r := ownershipRouter(h, "alice")
	w := doJSON(r, http.MethodPost, "/sessions/s1/validate", map[string]any{"step_id": 1})
	if w.Code != http.StatusOK {
		t.Fatalf("debug+validator nil: expected 200 mock, got %d: %s", w.Code, w.Body.String())
	}
}
