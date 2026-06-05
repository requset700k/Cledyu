// Package validation은 검증 요청을 Kafka(validation-requests 토픽)로 발행한다.
//
// 검증엔진(apps/validation-engine)이 이 토픽을 소비해 VM에서 체크를 실행하고
// 결과를 validation-results 로 돌려준다. 결과 소비(consumer)는 후속 작업이며,
// 이 패키지는 발행(producer) 쪽만 담당한다.
//
// 메시지 스키마(Request/VMSpec)와 mTLS 구성은 검증엔진과 동일하게 맞춰,
// 엔진이 그대로 역직렬화·소비할 수 있게 한다. Checks 는 콘텐츠의 content.Check 를
// 그대로 재사용해 직렬화 형식(특히 command→`cmd`)이 어긋나지 않도록 한다.
package validation

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"os"

	"github.com/requset700k/cledyu/api/internal/content"
	kafka "github.com/segmentio/kafka-go"
	"go.uber.org/zap"
)

// VMType 는 검증을 수행할 VM 종류다. Phase-1 세션은 모두 KubeVirt VM 이다.
const VMTypeKubeVirt = "kubevirt"

// VMSpec 은 검증엔진이 접속할 VM 정보다(검증엔진 model.VMSpec 의 KubeVirt 필드와 정렬).
type VMSpec struct {
	Type      string `json:"type"`
	Name      string `json:"name,omitempty"`
	Namespace string `json:"namespace,omitempty"`
}

// Request 는 validation-requests 토픽에 실리는 메시지다(검증엔진 model.ValidationRequest 와 정렬).
type Request struct {
	TraceID   string          `json:"trace_id,omitempty"`
	SessionID string          `json:"session_id"`
	StepID    int             `json:"step_id"`
	VM        VMSpec          `json:"vm"`
	Checks    []content.Check `json:"checks"`
}

// Producer 는 Kafka Writer 를 감싸 검증 요청을 발행한다.
type Producer struct {
	writer *kafka.Writer
	log    *zap.Logger
}

// New 는 validation-requests 발행용 Producer 를 만든다.
// brokers: mTLS 리스너 주소 목록(예: ["cledyu-kafka-kafka-bootstrap.kafka.svc:9093"]).
func New(brokers []string, topic string, tlsCfg *tls.Config, log *zap.Logger) *Producer {
	return &Producer{
		writer: &kafka.Writer{
			Addr:      kafka.TCP(brokers...),
			Topic:     topic,
			Transport: &kafka.Transport{TLS: tlsCfg},
			Balancer:  &kafka.Hash{}, // session_id 키 기반 파티셔닝 — 세션별 순서 보장
		},
		log: log,
	}
}

// Publish 는 검증 요청을 발행한다. 파티션 키는 session_id 로, 세션 단위 순서를 보장한다.
func (p *Producer) Publish(ctx context.Context, req Request) error {
	data, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal validation request: %w", err)
	}
	if err := p.writer.WriteMessages(ctx, kafka.Message{
		Key:   []byte(req.SessionID),
		Value: data,
	}); err != nil {
		return fmt.Errorf("publish validation request: %w", err)
	}
	p.log.Info("검증 요청 발행",
		zap.String("session_id", req.SessionID),
		zap.Int("step_id", req.StepID),
		zap.Int("checks", len(req.Checks)),
	)
	return nil
}

// Close 는 Kafka 연결을 닫는다.
func (p *Producer) Close() error {
	return p.writer.Close()
}

// LoadTLS 는 cert-manager 가 발급해 마운트한 클라이언트 인증서·키·CA 로 mTLS 설정을 구성한다.
// 검증엔진(cmd/engine/main.go)의 loadTLS 와 동일한 방식으로, 인증서 갱신을 매 연결마다 반영한다.
func LoadTLS(certFile, keyFile, caFile string) (*tls.Config, error) {
	caPEM, err := os.ReadFile(caFile)
	if err != nil {
		return nil, err
	}
	caPool := x509.NewCertPool()
	caPool.AppendCertsFromPEM(caPEM)

	// 시작 시 인증서 유효성 확인.
	if _, err := tls.LoadX509KeyPair(certFile, keyFile); err != nil {
		return nil, err
	}

	return &tls.Config{
		// cert-manager 가 Secret 볼륨을 갱신하면 다음 연결부터 새 인증서를 사용한다(Pod 재시작 불필요).
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
