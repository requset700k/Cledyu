// Package consumer는 Kafka validation-requests 토픽에서 메시지를 읽는다.
package consumer

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/requset700k/cledyu/validation-engine/internal/model"
	"github.com/segmentio/kafka-go"
	"go.uber.org/zap"
)

// Consumer는 Kafka에서 메시지를 읽는 구조체
type Consumer struct {
	reader *kafka.Reader // Kafka에서 메시지를 읽는 도구
	log    *zap.Logger
}

// New는 Consumer를 만든다
// brokers: Kafka 주소 목록 (예: ["cledyu-kafka-bootstrap.kafka.svc:9092"])
func New(brokers []string, log *zap.Logger) *Consumer {
	reader := kafka.NewReader(kafka.ReaderConfig{
		Brokers: brokers,
		Topic:   "validation-requests", // 읽을 토픽 이름
		GroupID: "validation-engine",   // 컨슈머 그룹 이름
	})

	return &Consumer{
		reader: reader,
		log:    log,
	}
}

// HandleFunc는 메시지를 받았을 때 실행할 함수 타입
// consumer.go는 메시지를 꺼내는 역할만 하고,
// 실제로 뭘 할지는 main.go에서 이 함수로 넘겨준다
type HandleFunc func(ctx context.Context, req model.ValidationRequest) error

// Run은 Kafka에서 메시지를 계속 읽으면서 handler를 실행한다.
// ctx가 취소되면 (프로그램 종료 시) 멈춘다.
func (c *Consumer) Run(ctx context.Context, handler HandleFunc) error {
	c.log.Info("kafka consumer 시작", zap.String("topic", "validation-requests"))

	for {
		// ctx가 취소됐는지 먼저 확인 — 프로그램 종료 신호가 오면 루프를 빠져나감
		select {
		case <-ctx.Done():
			c.log.Info("consumer 종료")
			return nil
		default:
		}

		// Kafka에서 메시지 하나를 꺼낸다
		// 메시지가 없으면 올 때까지 여기서 기다린다
		msg, err := c.reader.ReadMessage(ctx)
		if err != nil {
			// ctx 취소로 인한 종료는 정상이므로 오류 없이 리턴
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("메시지 읽기 실패: %w", err)
		}

		c.log.Info("메시지 수신",
			zap.String("topic", msg.Topic),
			zap.Int("partition", msg.Partition),
			zap.Int64("offset", msg.Offset),
		)

		// 꺼낸 메시지(JSON)를 ValidationRequest 구조체로 변환
		var req model.ValidationRequest
		if err := json.Unmarshal(msg.Value, &req); err != nil {
			c.log.Error("JSON 파싱 실패, 메시지 건너뜀",
				zap.Error(err),
				zap.ByteString("raw", msg.Value),
			)
			continue
		}

		// handler 실행 — 실제 검증 로직은 여기서 처리
		if err := handler(ctx, req); err != nil {
			c.log.Error("handler 실패",
				zap.Error(err),
				zap.String("session_id", req.SessionID),
				zap.Int("step_id", req.StepID),
			)
			// handler 실패해도 다음 메시지는 계속 처리
			continue
		}
	}
}

// Close는 Kafka 연결을 닫는다. 프로그램 종료 시 호출한다.
func (c *Consumer) Close() error {
	return c.reader.Close()
}
