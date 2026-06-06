// SIGINT/SIGTERM 수신 시 진행 중인 요청을 최대 10초 기다린 후 graceful shutdown.
package main

import (
	"context"
	"log"
	"net/http"
	"os/signal"
	"strings"
	"syscall"
	"time"

	api "github.com/requset700k/cledyu/api/internal/api"
	"github.com/requset700k/cledyu/api/internal/api/handlers"
	"github.com/requset700k/cledyu/api/internal/config"
	"github.com/requset700k/cledyu/api/internal/kubevirt"
	"github.com/requset700k/cledyu/api/internal/validation"
	"go.uber.org/zap"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	// release 모드: JSON 구조화 로그 / debug 모드: 사람이 읽기 쉬운 콘솔 로그
	var logger *zap.Logger
	if cfg.Server.Mode == "release" {
		logger, _ = zap.NewProduction()
	} else {
		logger, _ = zap.NewDevelopment()
	}
	defer logger.Sync() //nolint:errcheck

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// kubevirt Manager: 클러스터 외부 실행 시 nil로 폴백 — 세션 API가 503을 반환한다.
	sessions, err := kubevirt.NewManager(&cfg.KubeVirt)
	if err != nil {
		logger.Warn("kubevirt manager init failed, sessions disabled", zap.Error(err))
		sessions = nil
	}

	// 검증 연동: kafka.enabled=true 이고 인증서 로드에 성공할 때만 활성화한다.
	// 비활성(기본)이면 dispatch=nil(발행 생략) + consumer=nil(결과 소비 없음)이라
	// ValidateStep이 mock 통과로 동작해 클러스터 없이도 안전하다.
	var dispatch handlers.Dispatcher
	var consumer *validation.Consumer
	if cfg.Kafka.Enabled {
		tlsCfg, terr := validation.LoadTLS(cfg.Kafka.TLSCert, cfg.Kafka.TLSKey, cfg.Kafka.CACert)
		if terr != nil {
			logger.Warn("kafka tls load failed, validation disabled", zap.Error(terr))
		} else {
			brokers := strings.Split(cfg.Kafka.Brokers, ",")
			prod := validation.New(brokers, cfg.Kafka.Topic, tlsCfg, logger)
			defer prod.Close() //nolint:errcheck
			dispatch = prod
			consumer = validation.NewConsumer(brokers, cfg.Kafka.ResultsTopic, cfg.Kafka.ConsumerGroup, tlsCfg, logger)
			defer consumer.Close() //nolint:errcheck
			logger.Info("validation enabled",
				zap.String("requests_topic", cfg.Kafka.Topic),
				zap.String("results_topic", cfg.Kafka.ResultsTopic),
			)
		}
	}

	router, h := api.NewRouter(cfg, logger, sessions, dispatch)

	// 검증 결과 소비 루프: 결과를 stepStore에 반영한다. ctx 취소(종료 신호) 시 graceful 종료.
	if consumer != nil {
		go func() {
			if err := consumer.Run(ctx, h.ApplyValidationResult); err != nil {
				logger.Error("validation consumer stopped", zap.Error(err))
			}
		}()
	}

	// Read/WriteTimeout: 느린 클라이언트로 인한 goroutine 고갈 방지. IdleTimeout: keep-alive 연결 유지 상한.
	srv := &http.Server{
		Addr:         cfg.Server.Addr,
		Handler:      router,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// ctx.Done() 대기를 위해 goroutine으로 분리.
	go func() {
		logger.Info("server started", zap.String("addr", cfg.Server.Addr))
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Fatal("server error", zap.Error(err))
		}
	}()

	<-ctx.Done()
	logger.Info("shutting down...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("shutdown error", zap.Error(err))
	}
}
