package validation

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"os"

	"github.com/segmentio/kafka-go"
	"go.uber.org/zap"
)

// KafkaPublisher는 ValidationRequest를 validation-requests 토픽에 mTLS로 발행한다.
// validation-engine 의 producer(internal/producer)와 동일한 kafka-go·TLS 패턴을 사용한다.
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
		Balancer:  &kafka.Hash{}, // session_id 키 기반 파티셔닝 — validation-engine 컨슈머와 동일 규칙
	}
	return &KafkaPublisher{writer: writer, log: log}
}

// PublishRequest는 validation.Publisher 인터페이스를 구현한다.
// 메시지 키로 session_id를 써서 같은 세션의 요청이 같은 파티션으로 가도록 한다.
func (p *KafkaPublisher) PublishRequest(ctx context.Context, req ValidationRequest) error {
	data, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal validation request: %w", err)
	}
	if err := p.writer.WriteMessages(ctx, kafka.Message{
		Key:   []byte(req.SessionID),
		Value: data,
	}); err != nil {
		return fmt.Errorf("write validation request: %w", err)
	}
	p.log.Info("validation request 발행",
		zap.String("trace_id", req.TraceID),
		zap.String("session_id", req.SessionID),
		zap.Int("step_id", req.StepID),
	)
	return nil
}

// Close는 Kafka writer를 닫는다.
func (p *KafkaPublisher) Close() error {
	return p.writer.Close()
}

// LoadTLS는 cert-manager가 발급한 client cert/key + CA 인증서를 읽어 mTLS 설정을 만든다.
// GetClientCertificate가 매 연결마다 파일을 다시 읽으므로 cert-manager 갱신 시 Pod 재시작이 필요 없다.
// 인증서 파일이 없으면(로컬/CI) 에러를 반환해 호출자가 mock 모드로 폴백하게 한다.
func LoadTLS(certFile, keyFile, caFile string) (*tls.Config, error) {
	caPEM, err := os.ReadFile(caFile)
	if err != nil {
		return nil, fmt.Errorf("read kafka CA: %w", err)
	}
	caPool := x509.NewCertPool()
	if !caPool.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("no CA certificate found in %s", caFile)
	}
	if _, err := tls.LoadX509KeyPair(certFile, keyFile); err != nil {
		return nil, fmt.Errorf("load kafka client cert: %w", err)
	}
	return &tls.Config{
		GetClientCertificate: func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
			cert, err := tls.LoadX509KeyPair(certFile, keyFile)
			if err != nil {
				return nil, err
			}
			return &cert, nil
		},
		RootCAs:    caPool,
		MinVersion: tls.VersionTLS13,
	}, nil
}
