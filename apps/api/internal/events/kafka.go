package events

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"

	"github.com/segmentio/kafka-go"
	"go.uber.org/zap"
)

// Publisher는 학습 이벤트 발행 인터페이스다. nil 이면 핸들러가 발행을 건너뛴다(로컬/CI).
type Publisher interface {
	Publish(ctx context.Context, e Event) error
}

// KafkaPublisher는 Event를 lab-events 토픽에 mTLS로 발행한다.
// validation.KafkaPublisher 와 동일한 kafka-go/TLS 패턴을 사용한다.
type KafkaPublisher struct {
	writer *kafka.Writer
	log    *zap.Logger
}

// NewKafkaPublisher는 브로커/토픽/TLS 설정으로 Kafka writer를 구성한다.
func NewKafkaPublisher(brokers []string, topic string, tlsCfg *tls.Config, log *zap.Logger) *KafkaPublisher {
	writer := &kafka.Writer{
		Addr:      kafka.TCP(brokers...),
		Topic:     topic,
		Transport: &kafka.Transport{TLS: tlsCfg},
		Balancer:  &kafka.Hash{}, // user_id 키 기반 파티셔닝 — 학습자별 이벤트 순서 보존
		// 학습 이벤트는 사용자 요청 경로의 부수 효과라 지연 최소화를 우선한다.
		// 유실 허용(분석용 데이터, at-most-once) — RequiredAcks 기본(One) 유지.
		Async: false,
	}
	return &KafkaPublisher{writer: writer, log: log}
}

// Publish는 이벤트를 발행한다. 메시지 키는 user_id.
func (p *KafkaPublisher) Publish(ctx context.Context, e Event) error {
	data, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("marshal learning event: %w", err)
	}
	if err := p.writer.WriteMessages(ctx, kafka.Message{
		Key:   []byte(e.UserID),
		Value: data,
	}); err != nil {
		return fmt.Errorf("write learning event: %w", err)
	}
	return nil
}

// Close는 Kafka writer를 닫는다.
func (p *KafkaPublisher) Close() error {
	return p.writer.Close()
}
