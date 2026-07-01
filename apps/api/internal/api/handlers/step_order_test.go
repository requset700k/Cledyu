package handlers

import (
	"net/http"
	"testing"

	"go.uber.org/zap"
)

func stepOrderHandler() *Handler {
	h := &Handler{log: zap.NewNop(), steps: newStepStore(nil, nil)}
	h.steps.m["s1"] = &sessionSteps{
		LabID:  "lab-linux-basics",
		UserID: "alice",
		Steps: []stepState{
			{StepID: 1, Status: "active"},
			{StepID: 2, Status: "pending"},
			{StepID: 3, Status: "pending"},
		},
		CurrentStep: 1,
	}
	return h
}

// Web에서 미래 단계를 disabled 처리해도 API를 직접 호출하면 우회할 수 있다.
// 서버는 이전 단계가 passed가 아닌 검증 요청을 409로 막아 진행 순서를 최종 보장해야 한다.
func TestValidateStep_BlocksOutOfOrderStep(t *testing.T) {
	h := stepOrderHandler()
	r := ownershipRouter(h, "alice")

	w := doJSON(r, http.MethodPost, "/sessions/s1/validate", map[string]any{"step_id": 2})
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409 for out-of-order validation, got %d: %s", w.Code, w.Body.String())
	}

	ss := h.steps.m["s1"]
	if ss.Steps[1].Status != "pending" || ss.Steps[1].Attempts != 0 {
		t.Fatalf("out-of-order validation mutated step2: %+v", ss.Steps[1])
	}
}

func TestValidateStep_AllowsNextStepAfterPreviousPassed(t *testing.T) {
	h := stepOrderHandler()
	h.steps.m["s1"].Steps[0].Status = "passed"
	h.steps.m["s1"].Steps[1].Status = "active"
	r := ownershipRouter(h, "alice")

	w := doJSON(r, http.MethodPost, "/sessions/s1/validate", map[string]any{"step_id": 2})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for next allowed step, got %d: %s", w.Code, w.Body.String())
	}

	if got := h.steps.m["s1"].Steps[1].Status; got != "passed" {
		t.Fatalf("expected step2 to pass through mock validator, got %q", got)
	}
}
