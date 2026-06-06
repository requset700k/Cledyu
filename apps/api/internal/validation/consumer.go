package validation

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"time"

	kafka "github.com/segmentio/kafka-go"
	"go.uber.org/zap"
)

// CheckOutcome 은 검증 항목 하나의 결과다(검증엔진 model.CheckResult 와 정렬).
type CheckOutcome struct {
	Type   string `json:"type"`
	Passed bool   `json:"passed"`
	Detail string `json:"detail,omitempty"` // 실패 이유 또는 실행 결과 요약
}

// Result 는 validation-results 토픽 메시지다(검증엔진 model.ValidationResult 와 정렬).
type Result struct {
	TraceID    string         `json:"trace_id,omitempty"`
	SessionID  string         `json:"session_id"`
	StepID     int            `json:"step_id"`
	Passed     bool           `json:"passed"` // Checks 가 모두 통과하면 true
	Checks     []CheckOutcome `json:"checks"`
	DurationMS int64          `json:"duration_ms"`
}

// Consumer 는 validation-results 토픽을 구독해 검증 결과를 수신한다.
type Consumer struct {
	reader *kafka.Reader
	log    *zap.Logger
}

// NewConsumer 는 검증 결과 소비용 Consumer 를 만든다.
// group 으로 컨슈머 그룹을 지정해 여러 API 레플리카가 결과를 분담하면서도 각 메시지를 한 번만 처리한다.
func NewConsumer(brokers []string, topic, group string, tlsCfg *tls.Config, log *zap.Logger) *Consumer {
	return &Consumer{
		reader: kafka.NewReader(kafka.ReaderConfig{
			Brokers: brokers,
			Topic:   topic,
			GroupID: group,
			Dialer: &kafka.Dialer{
				Timeout:   10 * time.Second,
				DualStack: true,
				TLS:       tlsCfg, // producer 와 동일한 mTLS 구성
			},
		}),
		log: log,
	}
}

// Run 은 ctx 가 취소될 때까지 결과 메시지를 읽어 handle 로 넘긴다.
// 역직렬화 실패나 일시적 읽기 오류는 로깅 후 다음 메시지로 진행한다(소비 루프 유지).
// ctx 취소로 종료될 때는 nil 을 반환한다.
func (c *Consumer) Run(ctx context.Context, handle func(Result)) error {
	for {
		m, err := c.reader.ReadMessage(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil // graceful shutdown
			}
			c.log.Error("검증 결과 읽기 실패", zap.Error(err))
			continue
		}
		var r Result
		if err := json.Unmarshal(m.Value, &r); err != nil {
			c.log.Error("검증 결과 역직렬화 실패", zap.Error(err), zap.ByteString("value", m.Value))
			continue
		}
		c.log.Info("검증 결과 수신",
			zap.String("session_id", r.SessionID),
			zap.Int("step_id", r.StepID),
			zap.Bool("passed", r.Passed),
		)
		handle(r)
	}
}

// Close 는 Kafka 연결을 닫는다.
func (c *Consumer) Close() error {
	return c.reader.Close()
}
