// Package consumer는 Kafka validation-requests 토픽에서 메시지를 읽는다.
package consumer

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"time"

	"github.com/requset700k/cledyu/validation-engine/internal/model"
	"github.com/segmentio/kafka-go"
	"go.uber.org/zap"
)

// kafkaReader는 Kafka 메시지를 읽는 인터페이스. 테스트에서 mock으로 교체 가능.
type kafkaReader interface {
	FetchMessage(ctx context.Context) (kafka.Message, error)
	CommitMessages(ctx context.Context, msgs ...kafka.Message) error
	Close() error
}

// dlqPublisher는 DLQ 토픽에 메시지를 발행하는 인터페이스. 테스트에서 mock으로 교체 가능.
type dlqPublisher interface {
	WriteMessages(ctx context.Context, msgs ...kafka.Message) error
	Close() error
}

// Consumer는 Kafka에서 메시지를 읽는 구조체
type Consumer struct {
	reader    kafkaReader
	dlqWriter dlqPublisher // 처리 실패 메시지를 보관하는 DLQ 토픽 writer
	log       *zap.Logger
}

// New는 Consumer를 만든다
// brokers: Kafka 주소 목록 (예: ["cledyu-kafka-bootstrap.kafka.svc:9093"])
func New(brokers []string, tlsCfg *tls.Config, log *zap.Logger) *Consumer {
	reader := kafka.NewReader(kafka.ReaderConfig{
		Brokers: brokers,
		Topic:   "validation-requests",
		GroupID: "validation-engine",
		Dialer:  &kafka.Dialer{TLS: tlsCfg},
	})

	dlqWriter := &kafka.Writer{
		Addr:      kafka.TCP(brokers...),
		Topic:     "validation-requests-dlq",
		Transport: &kafka.Transport{TLS: tlsCfg},
	}

	return &Consumer{
		reader:    reader,
		dlqWriter: dlqWriter,
		log:       log,
	}
}

// HandleFunc는 메시지를 받았을 때 실행할 함수 타입
type HandleFunc func(ctx context.Context, req model.ValidationRequest) error

// handlerTimeout은 메시지 하나를 처리하는 최대 시간
const handlerTimeout = 5 * time.Minute

// Run은 Kafka에서 메시지를 계속 읽으면서 handler를 실행한다.
// ctx가 취소되면 새 메시지를 꺼내지 않고, 처리 중인 메시지는 완료 후 종료한다.
func (c *Consumer) Run(ctx context.Context, handler HandleFunc) error {
	c.log.Info("kafka consumer 시작", zap.String("topic", "validation-requests"))

	for {
		// FetchMessage: 메시지를 꺼내기만 하고 Kafka에 완료 기록하지 않는다.
		// ctx가 취소되면 (SIGTERM 등) 여기서 멈추고 새 메시지를 받지 않는다.
		msg, err := c.reader.FetchMessage(ctx)
		if err != nil {
			if ctx.Err() != nil {
				c.log.Info("consumer 종료")
				return nil //nolint:nilerr // context 취소는 정상 종료
			}
			return fmt.Errorf("메시지 읽기 실패: %w", err)
		}

		c.log.Info("메시지 수신",
			zap.String("topic", msg.Topic),
			zap.Int("partition", msg.Partition),
			zap.Int64("offset", msg.Offset),
		)

		var req model.ValidationRequest
		if err := json.Unmarshal(msg.Value, &req); err != nil {
			c.log.Error("JSON 파싱 실패 — DLQ 발행",
				zap.Error(err),
				zap.ByteString("raw", msg.Value),
			)
			c.publishToDLQ(msg)
			if commitErr := c.reader.CommitMessages(context.Background(), msg); commitErr != nil {
				c.log.Error("커밋 실패", zap.Error(commitErr))
			}
			continue
		}

		// handler는 shutdown 신호(ctx)와 무관하게 완료까지 실행한다.
		// SIGTERM이 와도 현재 검증을 끝낸 뒤 종료하기 위해 별도 context를 쓴다.
		handlerCtx, cancel := context.WithTimeout(context.Background(), handlerTimeout)
		err = handler(handlerCtx, req)
		cancel()

		if err != nil {
			c.log.Error("handler 실패 — DLQ 발행",
				zap.Error(err),
				zap.String("session_id", req.SessionID),
				zap.Int("step_id", req.StepID),
			)
			c.publishToDLQ(msg)
			if commitErr := c.reader.CommitMessages(context.Background(), msg); commitErr != nil {
				c.log.Error("커밋 실패", zap.Error(commitErr))
			}
			continue
		}

		// 성공 시에만 커밋 — Kafka에 "처리 완료" 기록
		if err := c.reader.CommitMessages(context.Background(), msg); err != nil {
			c.log.Error("커밋 실패", zap.Error(err))
		}
	}
}

// 재시도 횟수 (3번)
const dlqMaxRetries = 3

// 재시도 간격 (0.5초)
const dlqRetryInterval = 500 * time.Millisecond

// publishToDLQ는 처리 실패한 원본 메시지를 DLQ 토픽에 발행
// 최대 3회 재시도하고, 모두 실패하면 원본 payload를 로그에 남겨 Loki에서 확인할 수 있게 한다.
func (c *Consumer) publishToDLQ(msg kafka.Message) {
	for i := range dlqMaxRetries {
		err := c.dlqWriter.WriteMessages(context.Background(), kafka.Message{
			Key:   msg.Key,
			Value: msg.Value,
		})
		if err == nil {
			return
		}
		// 마지막 시도 실패 시에는 sleep 없이 바로 최종 실패 처리
		if i < dlqMaxRetries-1 {
			c.log.Warn("DLQ 발행 재시도",
				zap.Int("attempt", i+1),
				zap.Error(err),
			)
			time.Sleep(dlqRetryInterval)
		}
	}
	c.log.Error("DLQ 발행 최종 실패 — payload를 로그에 기록 (Loki에서 복구 가능)",
		zap.String("topic", msg.Topic),
		zap.Int("partition", msg.Partition),
		zap.Int64("offset", msg.Offset),
		zap.ByteString("payload", msg.Value),
	)
}

// Close는 Kafka 연결을 닫는다. 프로그램 종료 시 호출한다.
func (c *Consumer) Close() error {
	_ = c.dlqWriter.Close()
	return c.reader.Close()
}
