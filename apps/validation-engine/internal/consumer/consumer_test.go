package consumer

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/requset700k/cledyu/validation-engine/internal/model"
	"github.com/segmentio/kafka-go"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest"
	"go.uber.org/zap/zaptest/observer"
)

// mockDLQ는 dlqPublisher 인터페이스를 구현하는 테스트용 mock.
type mockDLQ struct {
	failUntil int32        // 이 횟수까지 WriteMessages를 실패시킨다 (0 = 항상 성공)
	calls     atomic.Int32 // WriteMessages 호출 횟수
	received  []kafka.Message
}

func (m *mockDLQ) WriteMessages(_ context.Context, msgs ...kafka.Message) error {
	n := m.calls.Add(1)
	if n <= m.failUntil {
		return errors.New("DLQ 브로커 오류")
	}
	m.received = append(m.received, msgs...)
	return nil
}

func (m *mockDLQ) Close() error { return nil }

// mockReader는 테스트용 메시지 배열을 순서대로 반환하고, 소진되면 ctx 취소를 기다린다.
type mockReader struct {
	msgs    []kafka.Message
	idx     int
	commits []kafka.Message
}

func (r *mockReader) FetchMessage(ctx context.Context) (kafka.Message, error) {
	if r.idx < len(r.msgs) {
		msg := r.msgs[r.idx]
		r.idx++
		return msg, nil
	}
	<-ctx.Done()
	return kafka.Message{}, ctx.Err()
}

func (r *mockReader) CommitMessages(_ context.Context, msgs ...kafka.Message) error {
	r.commits = append(r.commits, msgs...)
	return nil
}

func (r *mockReader) Close() error { return nil }

// newTestConsumer는 mock reader/dlqWriter를 사용하는 Consumer를 반환한다.
func newTestConsumer(reader *mockReader, dlq dlqPublisher, log *zap.Logger) *Consumer {
	return &Consumer{
		reader:    reader,
		dlqWriter: dlq,
		log:       log,
	}
}

func validMsg(t *testing.T, sessionID string, stepID int) kafka.Message {
	t.Helper()
	b, _ := json.Marshal(model.ValidationRequest{
		SessionID: sessionID,
		StepID:    stepID,
	})
	return kafka.Message{Topic: "validation-requests", Value: b}
}

// runConsumerOnce는 메시지를 모두 처리한 직후 consumer를 종료한다.
// 메시지가 소진되면 mockReader가 ctx.Done()을 기다리므로 짧은 timeout으로 빠르게 종료한다.
func runConsumerOnce(t *testing.T, c *Consumer, handler HandleFunc) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_ = c.Run(ctx, handler)
}

// Test: handler가 에러를 반환하면 원본 메시지가 DLQ에 들어가야 한다.
func TestDLQ_HandlerFailure_SendsToDLQ(t *testing.T) {
	dlq := &mockDLQ{}
	reader := &mockReader{msgs: []kafka.Message{validMsg(t, "sess-1", 1)}}
	log := zaptest.NewLogger(t)

	c := newTestConsumer(reader, dlq, log)
	handler := func(_ context.Context, _ model.ValidationRequest) error {
		return errors.New("handler 오류")
	}
	runConsumerOnce(t, c, handler)

	if dlq.calls.Load() == 0 {
		t.Fatal("handler 실패 시 DLQ.WriteMessages가 호출되지 않았다")
	}
	if len(dlq.received) != 1 {
		t.Fatalf("DLQ에 메시지 1개가 들어가야 하지만 %d개 들어갔다", len(dlq.received))
	}
	if string(dlq.received[0].Value) != string(reader.msgs[0].Value) {
		t.Error("DLQ에 들어간 메시지가 원본과 다르다")
	}
	if len(reader.commits) != 1 {
		t.Fatalf("DLQ 발행 후 커밋이 1회여야 하지만 %d회였다", len(reader.commits))
	}
}

// Test: JSON 파싱 실패 시에도 원본 메시지가 DLQ에 들어가야 한다.
func TestDLQ_InvalidJSON_SendsToDLQ(t *testing.T) {
	dlq := &mockDLQ{}
	reader := &mockReader{msgs: []kafka.Message{
		{Topic: "validation-requests", Value: []byte("invalid json")},
	}}
	log := zaptest.NewLogger(t)

	c := newTestConsumer(reader, dlq, log)
	handler := func(_ context.Context, _ model.ValidationRequest) error { return nil }
	runConsumerOnce(t, c, handler)

	if len(dlq.received) != 1 {
		t.Fatalf("JSON 파싱 실패 시 DLQ에 메시지 1개가 들어가야 하지만 %d개 들어갔다", len(dlq.received))
	}
	if string(dlq.received[0].Value) != "invalid json" {
		t.Error("DLQ에 들어간 메시지가 원본 raw bytes와 다르다")
	}
}

// Test: DLQ 발행이 실패하면 dlqMaxRetries번 재시도해야 한다.
func TestDLQ_Retry_OnWriteFailure(t *testing.T) {
	// failUntil = dlqMaxRetries → 모든 재시도 실패, 마지막 시도도 실패
	dlq := &mockDLQ{failUntil: int32(dlqMaxRetries)}
	reader := &mockReader{msgs: []kafka.Message{validMsg(t, "sess-2", 2)}}

	core, logs := observer.New(zap.ErrorLevel)
	log := zap.New(core)

	c := newTestConsumer(reader, dlq, log)
	handler := func(_ context.Context, _ model.ValidationRequest) error {
		return errors.New("handler 오류")
	}

	// 재시도 대기 시간(2 * 500ms = 1s, 마지막 시도엔 sleep 없음)을 감안한 timeout
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = c.Run(ctx, handler)

	if got := dlq.calls.Load(); got != int32(dlqMaxRetries) {
		t.Fatalf("DLQ WriteMessages가 %d번 호출되어야 하지만 %d번 호출됐다", dlqMaxRetries, got)
	}

	// 최종 실패 로그에 "payload"가 기록되어 있는지 확인 (Loki 복구용)
	found := false
	for _, entry := range logs.All() {
		for _, f := range entry.Context {
			if f.Key == "payload" {
				found = true
			}
		}
	}
	if !found {
		t.Error("DLQ 최종 실패 시 로그에 payload 필드가 없다 — Loki 복구 불가")
	}

	// DLQ 완전 실패 후에도 커밋되어야 한다 — 커밋 안 하면 같은 메시지를 무한 재처리한다
	if len(reader.commits) != 1 {
		t.Fatalf("DLQ 완전 실패 후 커밋이 1회여야 하지만 %d회였다", len(reader.commits))
	}
}

// Test: 첫 번째 메시지 실패 후 consumer가 죽지 않고 두 번째 메시지도 처리해야 한다.
func TestDLQ_ContinuesAfterFailure(t *testing.T) {
	dlq := &mockDLQ{}
	reader := &mockReader{msgs: []kafka.Message{
		validMsg(t, "sess-fail", 1),
		validMsg(t, "sess-ok", 2),
	}}
	log := zaptest.NewLogger(t)

	c := newTestConsumer(reader, dlq, log)
	handledSessions := []string{}
	handler := func(_ context.Context, req model.ValidationRequest) error {
		if req.SessionID == "sess-fail" {
			return errors.New("handler 오류")
		}
		handledSessions = append(handledSessions, req.SessionID)
		return nil
	}
	runConsumerOnce(t, c, handler)

	if len(dlq.received) != 1 {
		t.Fatalf("DLQ에 메시지 1개가 들어가야 하지만 %d개 들어갔다", len(dlq.received))
	}
	if len(reader.commits) != 2 {
		t.Fatalf("커밋이 2회여야 하지만 %d회였다", len(reader.commits))
	}
	if len(handledSessions) != 1 || handledSessions[0] != "sess-ok" {
		t.Errorf("두 번째 메시지(sess-ok)가 처리되지 않았다")
	}
}

// Test: DLQ 발행이 2번 실패하고 3번째에 성공하면 메시지가 DLQ에 들어가야 한다.
func TestDLQ_RetrySucceedsOnThirdAttempt(t *testing.T) {
	dlq := &mockDLQ{failUntil: 2}
	reader := &mockReader{msgs: []kafka.Message{validMsg(t, "sess-1", 1)}}

	core, logs := observer.New(zap.ErrorLevel)
	log := zap.New(core)

	c := newTestConsumer(reader, dlq, log)
	handler := func(_ context.Context, _ model.ValidationRequest) error {
		return errors.New("handler 오류")
	}

	// 재시도 대기 시간(2 * 500ms = 1s)을 감안한 timeout
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = c.Run(ctx, handler)

	if got := dlq.calls.Load(); got != 3 {
		t.Fatalf("WriteMessages가 3번 호출되어야 하지만 %d번 호출됐다", got)
	}
	if len(dlq.received) != 1 {
		t.Fatalf("3번째 시도에서 성공했으므로 DLQ에 메시지 1개가 있어야 하지만 %d개였다", len(dlq.received))
	}
	for _, entry := range logs.All() {
		for _, f := range entry.Context {
			if f.Key == "payload" {
				t.Error("재시도 성공 시 payload 로그가 찍히면 안 된다")
			}
		}
	}
}

// Test: FatalError 반환 시 DLQ 없이 consumer가 종료되어야 한다.
func TestFatalError_TerminatesConsumer(t *testing.T) {
	dlq := &mockDLQ{}
	reader := &mockReader{msgs: []kafka.Message{validMsg(t, "sess-1", 1)}}
	log := zaptest.NewLogger(t)

	c := newTestConsumer(reader, dlq, log)
	handler := func(_ context.Context, _ model.ValidationRequest) error {
		return &FatalError{Err: errors.New("Kafka 브로커 장애")}
	}

	err := c.Run(context.Background(), handler)

	if err == nil {
		t.Fatal("FatalError 시 Run()이 에러를 반환해야 한다")
	}
	if dlq.calls.Load() != 0 {
		t.Fatalf("FatalError 시 DLQ가 호출되면 안 되지만 %d번 호출됐다", dlq.calls.Load())
	}
	if len(reader.commits) != 0 {
		t.Fatalf("FatalError 시 커밋이 없어야 하지만 %d회였다", len(reader.commits))
	}
}

// Test: handler 성공 시 DLQ에 메시지가 가지 않아야 한다.
func TestDLQ_HandlerSuccess_NoDLQ(t *testing.T) {
	dlq := &mockDLQ{}
	reader := &mockReader{msgs: []kafka.Message{validMsg(t, "sess-3", 3)}}
	log := zaptest.NewLogger(t)

	c := newTestConsumer(reader, dlq, log)
	handler := func(_ context.Context, _ model.ValidationRequest) error { return nil }
	runConsumerOnce(t, c, handler)

	if dlq.calls.Load() != 0 {
		t.Fatalf("handler 성공 시 DLQ.WriteMessages가 호출되면 안 되지만 %d번 호출됐다", dlq.calls.Load())
	}
	if len(reader.commits) != 1 {
		t.Fatalf("성공 후 커밋이 1회여야 하지만 %d회였다", len(reader.commits))
	}
}
