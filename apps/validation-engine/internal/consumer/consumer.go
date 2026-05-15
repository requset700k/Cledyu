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

// Consumer는 Kafka에서 메시지를 읽는 구조체
type Consumer struct {
	reader *kafka.Reader
	log    *zap.Logger
}

// New는 Consumer를 만든다
// brokers: Kafka 주소 목록 (예: ["cledyu-kafka-bootstrap.kafka.svc:9093"])
func New(brokers []string, tlsCfg *tls.Config, log *zap.Logger) *Consumer {
	reader := kafka.NewReader(kafka.ReaderConfig{
		Brokers:     brokers,
		Topic:       "validation-requests",
		GroupID:     "validation-engine",
		Dialer:      &kafka.Dialer{TLS: tlsCfg},
	})

	return &Consumer{
		reader: reader,
		log:    log,
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
				return nil
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
			c.log.Error("JSON 파싱 실패, 메시지 건너뜀",
				zap.Error(err),
				zap.ByteString("raw", msg.Value),
			)
			// 깨진 메시지는 재처리해도 소용없으므로 커밋하고 넘어간다
			_ = c.reader.CommitMessages(context.Background(), msg)
			continue
		}

		// handler는 shutdown 신호(ctx)와 무관하게 완료까지 실행한다.
		// SIGTERM이 와도 현재 검증을 끝낸 뒤 종료하기 위해 별도 context를 쓴다.
		handlerCtx, cancel := context.WithTimeout(context.Background(), handlerTimeout)
		err = handler(handlerCtx, req)
		cancel()

		if err != nil {
			// 시스템 오류 — 오프셋 커밋 없이 consumer를 종료한다.
			// 뒤 offset을 커밋하면 이 offset도 처리된 것으로 간주되므로 즉시 멈춘다.
			// Pod 재시작 후 Kafka는 마지막으로 커밋된 offset부터 다시 전달한다.
			c.log.Error("handler 실패 — consumer 종료",
				zap.Error(err),
				zap.String("session_id", req.SessionID),
				zap.Int("step_id", req.StepID),
			)
			return fmt.Errorf("handler 오류: %w", err)
		}

		// 성공 시에만 커밋 — Kafka에 "처리 완료" 기록
		if err := c.reader.CommitMessages(context.Background(), msg); err != nil {
			c.log.Error("커밋 실패", zap.Error(err))
		}
	}
}

// Close는 Kafka 연결을 닫는다. 프로그램 종료 시 호출한다.
func (c *Consumer) Close() error {
	return c.reader.Close()
}
