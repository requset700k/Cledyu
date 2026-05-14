// Validation Engine 진입점.
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/requset700k/cledyu/validation-engine/internal/checker"
	"github.com/requset700k/cledyu/validation-engine/internal/consumer"
	"github.com/requset700k/cledyu/validation-engine/internal/executor"
	"github.com/requset700k/cledyu/validation-engine/internal/model"
	"github.com/requset700k/cledyu/validation-engine/internal/producer"
	"go.uber.org/zap"
)

const maxChecks = 20

func main() {
	// Uber의 zap 라이브러리를 사용하여 로그를 출력할 수 있게 고성능 로거를 초기화
	log, _ := zap.NewProduction()
	defer log.Sync() //nolint:errcheck

	// Kafka 서버 주소
	brokers := []string{getEnv("KAFKA_BROKERS", "cledyu-kafka-kafka-bootstrap.kafka.svc:9093")}

	// 헬스체크 엔드포인트 — Kubernetes liveness probe가 여기로 요청을 보낸다
	go func() {
		http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		})
		if err := http.ListenAndServe(":8080", nil); err != nil {
			log.Error("헬스 서버 종료", zap.Error(err))
		}
	}()

	// Kafka와 안전하게 통신하기 위해 TLS 인증서를 로드
	// cert-manager가 생성하여 컨테이너의 /etc/kafka-certs 경로에 마운트한 파일을 사용
	tlsCfg, err := loadTLS(
		getEnv("KAFKA_TLS_CERT", "/etc/kafka-certs/tls.crt"),
		getEnv("KAFKA_TLS_KEY", "/etc/kafka-certs/tls.key"),
		getEnv("KAFKA_CA_CERT", "/etc/kafka-certs/ca.crt"),
	)
	if err != nil {
		log.Fatal("TLS 인증서 로드 실패", zap.Error(err))
	}

	// consumer: Kafka 토픽에서 검증 요청 메시지를 읽어오는 인스턴스
	// producer: 검증 결과를 Kafka 토픽으로 내보내는 인스턴스
	cons := consumer.New(brokers, tlsCfg, log)
	prod := producer.New(brokers, tlsCfg, log)
	defer cons.Close()
	defer prod.Close()

	// Ctrl+C 또는 서버 종료 명령이 오면 감지할 수 있게 준비
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	log.Info("validation engine 시작", zap.Strings("brokers", brokers))

	// Kafka를 계속 바라보면서 요청이 오면 handle()을 실행(컨슈머)
	// 종료 신호가 오거나 오류가 발생하면 여기서 멈춘다
	if err := cons.Run(ctx, handle(prod, log)); err != nil {
		log.Error("consumer 오류", zap.Error(err))
		os.Exit(1)
	}
}

// handle은 Kafka로부터 받은 각 메시지를 어떻게 처리할지 정의
// 요청을 받아 → VM에서 검증하고 → 결과를 Kafka로 돌려보낸다
func handle(prod *producer.Producer, log *zap.Logger) consumer.HandleFunc {
	return func(ctx context.Context, req model.ValidationRequest) error {
		start := time.Now()

		// 어떤 요청이 들어왔는지 로그로 남김
		log.Info("검증 시작",
			zap.String("trace_id", req.TraceID),
			zap.String("session_id", req.SessionID),
			zap.Int("step_id", req.StepID),
			zap.String("vm_type", string(req.VM.Type)),
		)

		// 체크 수가 너무 많으면 거부 — 무한 루프나 리소스 고갈 방지
		if len(req.Checks) > maxChecks {
			return fmt.Errorf("체크 수 초과: %d > %d", len(req.Checks), maxChecks)
		}

		// 요청에 적힌 VM에 접속, executor는 VM에 명령을 내리는 도구
		exe, err := executor.New(req.VM)
		if err != nil {
			return err
		}
		defer exe.Close()

		// 실제로 모든 검증 항목(Checks)을 실행 (예: 특정 파일이 있는지, 프로세스가 떠 있는지 등)
		// results: 항목별 결과 / allPassed: 전부 통과했는지 여부
		results, allPassed := checker.RunAll(ctx, exe, req.Checks)

		result := model.ValidationResult{
			TraceID:    req.TraceID,
			SessionID:  req.SessionID,
			StepID:     req.StepID,
			Passed:     allPassed,
			Checks:     results,
			DurationMS: time.Since(start).Milliseconds(),
		}

		// 검증 결과를 다시 Kafka의 결과 토픽으로 발행(Publish)
		if err := prod.Publish(ctx, result); err != nil {
			return err
		}

		log.Info("검증 완료",
			zap.String("trace_id", req.TraceID),
			zap.String("session_id", req.SessionID),
			zap.Int("step_id", req.StepID),
			zap.Bool("passed", allPassed),
			zap.Int64("duration_ms", result.DurationMS),
		)

		return nil
	}
}

// loadTLS는 cert-manager가 발급한 클라이언트 인증서, 개인키, CA 인증서를 읽어 mTLS 설정을 구성
func loadTLS(certFile, keyFile, caFile string) (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, err
	}

	caPEM, err := os.ReadFile(caFile)
	if err != nil {
		return nil, err
	}

	caPool := x509.NewCertPool()
	caPool.AppendCertsFromPEM(caPEM)

	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		RootCAs:      caPool,
		MinVersion:   tls.VersionTLS13,
	}, nil
}

func getEnv(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}
