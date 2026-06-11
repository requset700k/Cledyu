package handlers

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/requset700k/cledyu/api/internal/events"
	"github.com/requset700k/cledyu/api/internal/validation"
	"go.uber.org/zap"
)

// capturePublisher는 발행된 학습 이벤트를 기록하는 테스트 더블이다.
// emitEvent 가 비동기(goroutine)라 대기 헬퍼(waitFor)로 수집을 기다린다.
type capturePublisher struct {
	mu     sync.Mutex
	events []events.Event
}

func (p *capturePublisher) Publish(_ context.Context, e events.Event) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.events = append(p.events, e)
	return nil
}

func (p *capturePublisher) snapshot() []events.Event {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]events.Event, len(p.events))
	copy(out, p.events)
	return out
}

// waitFor는 발행 이벤트 수가 want 개가 될 때까지 최대 2초 대기한다.
func (p *capturePublisher) waitFor(t *testing.T, want int) []events.Event {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		got := p.snapshot()
		if len(got) >= want {
			return got
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("expected %d events, got %d: %+v", want, len(p.snapshot()), p.snapshot())
	return nil
}

func eventsTestHandler(pub events.Publisher) *Handler {
	return &Handler{log: zap.NewNop(), steps: newStepStore(nil, nil), events: pub}
}

func seededSession(h *Handler, status1 string) {
	h.steps.m["s1"] = &sessionSteps{
		LabID:  "lab-linux-basics",
		UserID: "u1",
		Steps: []stepState{
			{StepID: 1, Status: status1},
			{StepID: 2, Status: "pending"},
		},
		CurrentStep: 1,
	}
}

// 스텝 통과 결과는 step_completed 를 발행한다(마지막 스텝이 아니면 lab_completed 없음).
func TestApplyValidationResult_EmitsStepCompleted(t *testing.T) {
	pub := &capturePublisher{}
	h := eventsTestHandler(pub)
	seededSession(h, "validating")

	h.ApplyValidationResult(validation.ValidationResult{SessionID: "s1", StepID: 1, Passed: true})

	got := pub.waitFor(t, 1)
	if got[0].Type != events.StepCompleted || got[0].UserID != "u1" || got[0].StepID != 1 {
		t.Fatalf("unexpected event: %+v", got[0])
	}
	if got[0].LabID != "lab-linux-basics" {
		t.Errorf("expected lab id, got %q", got[0].LabID)
	}
}

// 마지막 스텝까지 통과하면 step_completed + lab_completed 가 함께 발행된다.
func TestApplyValidationResult_EmitsLabCompleted(t *testing.T) {
	pub := &capturePublisher{}
	h := eventsTestHandler(pub)
	seededSession(h, "passed") // 1번은 이미 통과
	h.steps.m["s1"].Steps[1].Status = "validating"

	h.ApplyValidationResult(validation.ValidationResult{SessionID: "s1", StepID: 2, Passed: true})

	got := pub.waitFor(t, 2)
	types := map[events.Type]bool{}
	for _, e := range got {
		types[e.Type] = true
	}
	if !types[events.StepCompleted] || !types[events.LabCompleted] {
		t.Fatalf("expected step_completed + lab_completed, got %+v", got)
	}
}

// 검증 실패는 validation_failed 를 발행한다.
func TestApplyValidationResult_EmitsValidationFailed(t *testing.T) {
	pub := &capturePublisher{}
	h := eventsTestHandler(pub)
	seededSession(h, "validating")

	h.ApplyValidationResult(validation.ValidationResult{SessionID: "s1", StepID: 1, Passed: false})

	got := pub.waitFor(t, 1)
	if got[0].Type != events.ValidationFailed {
		t.Fatalf("expected validation_failed, got %+v", got[0])
	}
}

// publisher 미설정(nil)이면 발행 없이 정상 동작한다(로컬/CI 경로).
func TestApplyValidationResult_NilPublisher(t *testing.T) {
	h := eventsTestHandler(nil)
	seededSession(h, "validating")
	h.ApplyValidationResult(validation.ValidationResult{SessionID: "s1", StepID: 1, Passed: true})
	// panic 없이 통과하면 충분 — emitEvent 의 nil 가드 검증.
}
