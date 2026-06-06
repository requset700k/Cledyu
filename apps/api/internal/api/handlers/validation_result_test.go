package handlers

import (
	"testing"

	"github.com/requset700k/cledyu/api/internal/content"
	"github.com/requset700k/cledyu/api/internal/validation"
	"go.uber.org/zap"
)

func resultTestHandler() *Handler {
	return &Handler{
		log:   zap.NewNop(),
		labs:  map[string]content.LabContent{},
		steps: newStepStore(),
	}
}

func twoStepSession(status1 string) *sessionSteps {
	return &sessionSteps{
		LabID:       "lab-x",
		Steps:       []stepState{{StepID: 1, Status: status1}, {StepID: 2, Status: "pending"}},
		CurrentStep: 1,
	}
}

// TestApplyValidationResult_Pass: passed 결과는 스텝을 passed로 확정하고 다음 스텝을 활성화하며
// 체크별 상세를 저장한다.
func TestApplyValidationResult_Pass(t *testing.T) {
	h := resultTestHandler()
	h.steps.m["s1"] = twoStepSession("validating")

	h.ApplyValidationResult(validation.Result{
		SessionID: "s1", StepID: 1, Passed: true,
		Checks: []validation.CheckOutcome{{Type: "command", Passed: true, Detail: "ok"}},
	})

	ss := h.steps.m["s1"]
	if ss.Steps[0].Status != "passed" {
		t.Errorf("step1 status = %q, want passed", ss.Steps[0].Status)
	}
	if ss.Steps[1].Status != "active" {
		t.Errorf("step2 status = %q, want active", ss.Steps[1].Status)
	}
	if ss.CurrentStep != 2 {
		t.Errorf("current step = %d, want 2", ss.CurrentStep)
	}
	if len(ss.Steps[0].Checks) != 1 || !ss.Steps[0].Checks[0].Passed {
		t.Errorf("per-check outcome not stored: %+v", ss.Steps[0].Checks)
	}
}

// TestApplyValidationResult_Fail: failed 결과는 스텝을 failed로 두되 진행시키지 않고(current 유지)
// 실패 사유(detail)를 저장한다 — 사용자가 재시도할 수 있어야 한다.
func TestApplyValidationResult_Fail(t *testing.T) {
	h := resultTestHandler()
	h.steps.m["s1"] = twoStepSession("validating")

	h.ApplyValidationResult(validation.Result{
		SessionID: "s1", StepID: 1, Passed: false,
		Checks: []validation.CheckOutcome{{Type: "command", Passed: false, Detail: "expected 600 got 644"}},
	})

	ss := h.steps.m["s1"]
	if ss.Steps[0].Status != "failed" {
		t.Errorf("step1 status = %q, want failed", ss.Steps[0].Status)
	}
	if ss.Steps[1].Status != "pending" {
		t.Errorf("step2 status = %q, want pending (실패 시 진행 금지)", ss.Steps[1].Status)
	}
	if ss.CurrentStep != 1 {
		t.Errorf("current step = %d, want 1 (실패 시 유지)", ss.CurrentStep)
	}
	if len(ss.Steps[0].Checks) != 1 || ss.Steps[0].Checks[0].Detail == "" {
		t.Errorf("failure detail not stored: %+v", ss.Steps[0].Checks)
	}
}

// TestApplyValidationResult_UnknownSessionOrStep: 모르는 세션/스텝의 결과(지연 도착 등)는 무시한다.
func TestApplyValidationResult_UnknownSessionOrStep(t *testing.T) {
	h := resultTestHandler()
	h.ApplyValidationResult(validation.Result{SessionID: "nope", StepID: 1, Passed: true}) // panic 없어야 함

	h.steps.m["s1"] = &sessionSteps{Steps: []stepState{{StepID: 1, Status: "active"}}, CurrentStep: 1}
	h.ApplyValidationResult(validation.Result{SessionID: "s1", StepID: 99, Passed: true})
	if h.steps.m["s1"].Steps[0].Status != "active" {
		t.Errorf("unrelated step mutated: %q", h.steps.m["s1"].Steps[0].Status)
	}
}
