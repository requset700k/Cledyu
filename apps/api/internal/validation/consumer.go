package validation

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"time"

	"github.com/segmentio/kafka-go"
	"go.uber.org/zap"
)

// KafkaConsumer는 validation-results 토픽을 mTLS로 구독해 검증 결과를 수신한다.
// KafkaPublisher와 동일한 kafka-go·TLS 패턴을 사용한다.
type KafkaConsumer struct {
	reader *kafka.Reader
	log    *zap.Logger
}

// NewKafkaConsumer는 브로커/토픽/컨슈머그룹/TLS 설정으로 Kafka reader를 구성한다.
// group으로 컨슈머 그룹을 지정해 여러 API 레플리카가 결과를 분담하되 각 메시지를 한 번만 처리한다.
func NewKafkaConsumer(brokers []string, topic, group string, tlsCfg *tls.Config, log *zap.Logger) *KafkaConsumer {
	reader := kafka.NewReader(kafka.ReaderConfig{
		Brokers: brokers,
		Topic:   topic,
		GroupID: group,
		Dialer: &kafka.Dialer{
			Timeout:   10 * time.Second,
			DualStack: true,
			TLS:       tlsCfg,
		},
	})
	return &KafkaConsumer{reader: reader, log: log}
}

// Run은 ctx가 취소될 때까지 결과 메시지를 읽어 handle로 넘긴다.
// 역직렬화 실패나 일시적 읽기 오류는 로깅 후 다음 메시지로 진행한다(소비 루프 유지).
// ctx 취소로 종료될 때는 nil을 반환한다.
func (c *KafkaConsumer) Run(ctx context.Context, handle func(ValidationResult)) error {
	for {
		m, err := c.reader.ReadMessage(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil //nolint:nilerr // ctx 취소는 에러가 아니라 정상 종료다
			}
			c.log.Error("validation result 읽기 실패", zap.Error(err))
			continue
		}
		var r ValidationResult
		if err := json.Unmarshal(m.Value, &r); err != nil {
			c.log.Error("validation result 역직렬화 실패", zap.Error(err), zap.ByteString("value", m.Value))
			continue
		}
		c.log.Info("validation result 수신",
			zap.String("trace_id", r.TraceID),
			zap.String("session_id", r.SessionID),
			zap.Int("step_id", r.StepID),
			zap.Bool("passed", r.Passed),
		)
		handle(r)
	}
}

// Close는 Kafka reader를 닫는다.
func (c *KafkaConsumer) Close() error {
	return c.reader.Close()
}
