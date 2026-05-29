// SIGINT/SIGTERM 수신 시 진행 중인 요청을 최대 10초 기다린 후 graceful shutdown.
package main

import (
	"context"
	"log"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	api "github.com/requset700k/cledyu/api/internal/api"
	"github.com/requset700k/cledyu/api/internal/config"
	"github.com/requset700k/cledyu/api/internal/kubevirt"
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

	router := api.NewRouter(cfg, logger, sessions, nil)

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
