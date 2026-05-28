// Package producer는 Kafka validation-results 토픽에 결과 메시지를 발행한다.
package producer

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"

	"github.com/requset700k/cledyu/validation-engine/internal/model"
	"github.com/segmentio/kafka-go"
	"go.uber.org/zap"
)

// Producer는 Kafka에 메시지를 넣는 구조체
type Producer struct {
	writer *kafka.Writer
	log    *zap.Logger
}

// New는 Producer를 만든다
// brokers: Kafka 주소 목록 (예: ["cledyu-kafka-bootstrap.kafka.svc:9093"])
func New(brokers []string, tlsCfg *tls.Config, log *zap.Logger) *Producer {
	transport := &kafka.Transport{TLS: tlsCfg}

	writer := &kafka.Writer{
		Addr:      kafka.TCP(brokers...), // 어느 Kafka 서버에 보낼지
		Topic:     "validation-results",  // 어느 토픽에 넣을지
		Transport: transport,
		Balancer:  &kafka.Hash{}, // session_id key 기반 파티셔닝
	}

	return &Producer{writer: writer, log: log}
}

// Publish는 검증 결과를 Kafka에 넣는다.
func (p *Producer) Publish(ctx context.Context, result model.ValidationResult) error {
	data, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("JSON 변환 실패: %w", err)
	}

	err = p.writer.WriteMessages(ctx, kafka.Message{
		Key:   []byte(result.SessionID),
		Value: data,
	})
	if err != nil {
		return fmt.Errorf("메시지 발행 실패: %w", err)
	}

	p.log.Info("결과 발행 완료",
		zap.String("session_id", result.SessionID),
		zap.Int("step_id", result.StepID),
		zap.Bool("passed", result.Passed),
	)

	return nil
}

// Close는 Kafka 연결을 닫는다.
func (p *Producer) Close() error {
	return p.writer.Close()
}
