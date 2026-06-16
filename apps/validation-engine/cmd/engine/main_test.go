package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/requset700k/cledyu/validation-engine/internal/consumer"
	"github.com/requset700k/cledyu/validation-engine/internal/executor"
	"github.com/requset700k/cledyu/validation-engine/internal/model"
	"go.uber.org/zap"
)

// ── 테스트용 mock ──────────────────────────────────────────────────────────────

// mockPublisher는 Publish 호출 결과를 캡처한다.
// err가 설정되어 있으면 Publish 호출 시 해당 에러를 반환한다.
type mockPublisher struct {
	published []model.ValidationResult
	err       error
}

func (m *mockPublisher) Publish(_ context.Context, r model.ValidationResult) error {
	if m.err != nil {
		return m.err
	}
	m.published = append(m.published, r)
	return nil
}

// mockExecutor는 Exec를 호출하면 정해진 output/err를 반환한다.
// 실제 virtctl이나 SSM 없이 checker.RunAll을 테스트할 수 있다.
type mockExecutor struct {
	output string
	err    error
}

func (m *mockExecutor) Exec(_ context.Context, _ string) (string, error) {
	return m.output, m.err
}
func (m *mockExecutor) Close() {}

// mockExecFactory는 항상 mockExecutor를 반환하는 executor 팩토리다.
// handle()의 newExec 자리에 주입해서 실제 VM 없이 테스트한다.
func mockExecFactory(output string) func(model.VMSpec) (executor.VMExecutor, error) {
	return func(_ model.VMSpec) (executor.VMExecutor, error) {
		return &mockExecutor{output: output}, nil
	}
}

// ── 테스트용 헬퍼 ──────────────────────────────────────────────────────────────

// baseRequest는 모든 필드가 유효한 기본 요청이다. 테스트마다 필요한 필드만 바꿔 쓴다.
func baseRequest() model.ValidationRequest {
	return model.ValidationRequest{
		SessionID: "sess-test",
		StepID:    1,
		VM:        model.VMSpec{Type: model.VMTypeKubeVirt, Name: "vm1", Namespace: "lab"},
		Checks: []model.Check{
			// Exec 결과에 "ok"가 포함되면 통과하는 체크
			{Type: model.CheckCommand, Command: "echo ok", Expect: "ok"},
		},
	}
}

// ── 테스트 ─────────────────────────────────────────────────────────────────────

// session_id가 없으면 passed=false를 발행하고 nil을 반환해야 한다 (커밋 대상).
func TestHandle_MissingSessionID_PublishFailed(t *testing.T) {
	pub := &mockPublisher{}
	h := handle(pub, executor.New, zap.NewNop())

	req := baseRequest()
	req.SessionID = ""

	if err := h(context.Background(), req); err != nil {
		t.Fatalf("영구 오류는 nil을 반환해야 함, got: %v", err)
	}
	assertPublishedFailed(t, pub, "session_id가 비어있음")
}

// step_id가 0 이하면 passed=false를 발행하고 nil을 반환해야 한다.
func TestHandle_InvalidStepID_PublishFailed(t *testing.T) {
	pub := &mockPublisher{}
	h := handle(pub, executor.New, zap.NewNop())

	req := baseRequest()
	req.StepID = 0

	if err := h(context.Background(), req); err != nil {
		t.Fatalf("영구 오류는 nil을 반환해야 함, got: %v", err)
	}
	assertPublishedFailed(t, pub, "step_id가 유효하지 않음")
}

// checks가 비어있으면 passed=false를 발행하고 nil을 반환해야 한다.
func TestHandle_EmptyChecks_PublishFailed(t *testing.T) {
	pub := &mockPublisher{}
	h := handle(pub, executor.New, zap.NewNop())

	req := baseRequest()
	req.Checks = nil

	if err := h(context.Background(), req); err != nil {
		t.Fatalf("영구 오류는 nil을 반환해야 함, got: %v", err)
	}
	assertPublishedFailed(t, pub, "checks가 비어있음")
}

// checks가 maxChecks를 초과하면 passed=false를 발행하고 nil을 반환해야 한다.
func TestHandle_TooManyChecks_PublishFailed(t *testing.T) {
	pub := &mockPublisher{}
	h := handle(pub, executor.New, zap.NewNop())

	req := baseRequest()
	req.Checks = make([]model.Check, maxChecks+1)

	if err := h(context.Background(), req); err != nil {
		t.Fatalf("영구 오류는 nil을 반환해야 함, got: %v", err)
	}
	assertPublishedFailed(t, pub, "체크 수 초과")
}

// VM 스펙이 잘못되면 (KubeVirt인데 name/namespace 없음) passed=false를 발행해야 한다.
// executor.New를 실제로 호출해도 되는 이유: name이 없으면 에러 반환이 보장됨.
func TestHandle_InvalidVMSpec_PublishFailed(t *testing.T) {
	pub := &mockPublisher{}
	h := handle(pub, executor.New, zap.NewNop())

	req := baseRequest()
	req.VM = model.VMSpec{Type: model.VMTypeKubeVirt} // name, namespace 없음

	if err := h(context.Background(), req); err != nil {
		t.Fatalf("영구 오류는 nil을 반환해야 함, got: %v", err)
	}
	assertPublishedFailed(t, pub, "KubeVirt VM은 Name과 Namespace가 필요합니다")
}

// Kafka 발행 자체가 실패하면 FatalError를 반환해야 한다 — consumer가 DLQ 대신 종료되어 파드 재시작 후 재시도한다.
// publishFailed 경로(빈 checks)에서 pub.Publish가 실패하는 상황으로 검증한다.
func TestHandle_PublishError_ReturnsFatalError(t *testing.T) {
	pub := &mockPublisher{err: errors.New("kafka 장애")}
	h := handle(pub, executor.New, zap.NewNop())

	req := baseRequest()
	req.Checks = nil // publishFailed 경로로 유도

	err := h(context.Background(), req)
	if err == nil {
		t.Fatal("publish 실패 시 error를 반환해야 함")
	}
	var fatal *consumer.FatalError
	if !errors.As(err, &fatal) {
		t.Fatalf("publish 실패는 FatalError여야 하지만 %T였다", err)
	}
}

// 검증 결과 발행(pub.Publish) 자체가 실패해도 FatalError를 반환해야 한다.
// publishFailed 경로가 아닌 checker.RunAll 이후 결과 발행 실패 경로를 검증한다.
func TestHandle_ResultPublishError_ReturnsFatalError(t *testing.T) {
	pub := &mockPublisher{err: errors.New("kafka 장애")}
	h := handle(pub, mockExecFactory("ok"), zap.NewNop())

	err := h(context.Background(), baseRequest())
	if err == nil {
		t.Fatal("결과 발행 실패 시 error를 반환해야 함")
	}
	var fatal *consumer.FatalError
	if !errors.As(err, &fatal) {
		t.Fatalf("결과 발행 실패는 FatalError여야 하지만 %T였다", err)
	}
}

// 정상 요청은 검증 결과를 발행하고 nil을 반환해야 한다.
// mockExecFactory로 실제 VM 없이 Exec 성공을 흉내낸다.
func TestHandle_ValidRequest_PublishResult(t *testing.T) {
	pub := &mockPublisher{}
	// mockExecFactory: Exec 호출마다 "ok" 반환 → CheckCommand{Expect:"ok"} 통과
	h := handle(pub, mockExecFactory("ok"), zap.NewNop())

	req := baseRequest()

	if err := h(context.Background(), req); err != nil {
		t.Fatalf("정상 요청은 nil을 반환해야 함, got: %v", err)
	}
	if len(pub.published) != 1 {
		t.Fatalf("Publish가 1번 호출돼야 함, 실제: %d", len(pub.published))
	}
	result := pub.published[0]
	if result.SessionID != req.SessionID {
		t.Errorf("session_id 불일치: want %s, got %s", req.SessionID, result.SessionID)
	}
	if result.StepID != req.StepID {
		t.Errorf("step_id 불일치: want %d, got %d", req.StepID, result.StepID)
	}
	if !result.Passed {
		t.Error("모든 체크가 통과했으므로 passed=true여야 함")
	}
}

// ── 체크 필드 검증 테스트 ─────────────────────────────────────────────────────────

// file_content에 Expect=""이면 strings.Contains(output, "")=true라 항상 통과하는 버그.
// validateCheck에서 request_error로 잡아야 한다.
func TestHandle_FileContentEmptyExpect_PublishFailed(t *testing.T) {
	pub := &mockPublisher{}
	h := handle(pub, mockExecFactory("anything"), zap.NewNop())

	req := baseRequest()
	req.Checks = []model.Check{
		{Type: model.CheckFileContent, Path: "/etc/nginx.conf", Expect: ""},
	}

	if err := h(context.Background(), req); err != nil {
		t.Fatalf("영구 오류는 nil을 반환해야 함, got: %v", err)
	}
	assertPublishedFailed(t, pub, "expect가 비어있음")
}

// http_response에 ExpectCode=0이면 JSON에서 필드를 안 적은 것.
// HTTP 상태코드 0은 없으므로 request_error로 잡아야 한다.
func TestHandle_HTTPResponseZeroExpectCode_PublishFailed(t *testing.T) {
	pub := &mockPublisher{}
	h := handle(pub, mockExecFactory("200"), zap.NewNop())

	req := baseRequest()
	req.Checks = []model.Check{
		{Type: model.CheckHTTPResponse, URL: "http://localhost:80", ExpectCode: 0},
	}

	if err := h(context.Background(), req); err != nil {
		t.Fatalf("영구 오류는 nil을 반환해야 함, got: %v", err)
	}
	assertPublishedFailed(t, pub, "expect_code가 0")
}

// command에 cmd=""이면 request_error로 잡아야 한다.
func TestHandle_CommandEmptyCmd_PublishFailed(t *testing.T) {
	pub := &mockPublisher{}
	h := handle(pub, mockExecFactory(""), zap.NewNop())

	req := baseRequest()
	req.Checks = []model.Check{
		{Type: model.CheckCommand, Command: ""},
	}

	if err := h(context.Background(), req); err != nil {
		t.Fatalf("영구 오류는 nil을 반환해야 함, got: %v", err)
	}
	assertPublishedFailed(t, pub, "cmd가 비어있음")
}

// ── 공통 단언 함수 ──────────────────────────────────────────────────────────────

// assertPublishedFailed는 published 결과가 하나이고, passed=false이며,
// Checks에 CheckRequestError 타입이 있고 Detail에 wantDetail이 포함되는지 확인한다.
func assertPublishedFailed(t *testing.T, pub *mockPublisher, wantDetail string) {
	t.Helper()
	if len(pub.published) != 1 {
		t.Fatalf("Publish가 1번 호출돼야 함, 실제: %d", len(pub.published))
	}
	result := pub.published[0]
	if result.Passed {
		t.Error("passed=false여야 함")
	}
	if len(result.Checks) == 0 {
		t.Fatal("Checks가 비어있음 — 실패 이유가 없음")
	}
	check := result.Checks[0]
	if check.Type != model.CheckRequestError {
		t.Errorf("Checks[0].Type = %q, want %q", check.Type, model.CheckRequestError)
	}
	if check.Passed {
		t.Error("Checks[0].Passed = true여야 false임")
	}
	if wantDetail != "" && !containsString(check.Detail, wantDetail) {
		t.Errorf("Checks[0].Detail = %q, want to contain %q", check.Detail, wantDetail)
	}
}

func containsString(s, substr string) bool {
	return strings.Contains(s, substr)
}
