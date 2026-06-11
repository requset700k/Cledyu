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

	"github.com/redis/go-redis/v9"
	api "github.com/requset700k/cledyu/api/internal/api"
	"github.com/requset700k/cledyu/api/internal/config"
	"github.com/requset700k/cledyu/api/internal/events"
	"github.com/requset700k/cledyu/api/internal/kubevirt"
	"github.com/requset700k/cledyu/api/internal/lock"
	"github.com/requset700k/cledyu/api/internal/store"
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

	// validation publisher: Kafka mTLS 인증서가 있으면 연결하고, 없으면(로컬/CI) nil로 두어
	// ValidateStep 핸들러가 mock 검증으로 폴백한다. 학습 이벤트(lab-events) 발행기도
	// 같은 mTLS 자격으로 함께 구성한다 — 미가용 시 nil(발행 생략).
	var validator validation.Publisher
	var consumer *validation.KafkaConsumer
	var eventsPub events.Publisher
	if tlsCfg, tlsErr := validation.LoadTLS(cfg.Kafka.TLSCert, cfg.Kafka.TLSKey, cfg.Kafka.TLSCA); tlsErr != nil {
		logger.Warn("kafka mTLS 인증서 없음 — validation/학습 이벤트 비활성(mock 모드)", zap.Error(tlsErr))
	} else {
		brokers := strings.Split(cfg.Kafka.Brokers, ",")
		pub := validation.NewKafkaPublisher(brokers, cfg.Kafka.Topic, tlsCfg, logger)
		defer pub.Close() //nolint:errcheck
		validator = pub
		consumer = validation.NewKafkaConsumer(brokers, cfg.Kafka.ResultsTopic, cfg.Kafka.ConsumerGroup, tlsCfg, logger)
		defer consumer.Close() //nolint:errcheck
		evPub := events.NewKafkaPublisher(brokers, cfg.Kafka.EventsTopic, tlsCfg, logger)
		defer evPub.Close() //nolint:errcheck
		eventsPub = evPub
		logger.Info("validation 연결",
			zap.Strings("brokers", brokers),
			zap.String("requests_topic", cfg.Kafka.Topic),
			zap.String("results_topic", cfg.Kafka.ResultsTopic),
			zap.String("events_topic", cfg.Kafka.EventsTopic),
		)
	}

	// PostgreSQL 영속 계층 — DSN 미설정(로컬/CI) 또는 연결 실패 시 nil 로 두고
	// in-memory 전용으로 동작한다(시작은 막지 않되 진행 상태가 재시작에 휘발됨을 경고).
	var db *store.Store
	if cfg.DB.DSN != "" {
		if s, dbErr := store.Open(ctx, cfg.DB.DSN, logger); dbErr != nil {
			logger.Error("db 연결 실패 — 진행 상태 영속화 비활성(in-memory 전용)", zap.Error(dbErr))
		} else {
			db = s
			defer db.Close()
			logger.Info("db 연결 — 유저/진행 상태 영속화 활성")
		}
	} else {
		logger.Warn("db 미설정(CLEDYU_DB_DSN) — 진행 상태가 API 재시작 시 휘발됨")
	}

	// 세션 생성 직렬화 락 — Redis 연결되면 분산 락(다중 레플리카 안전), 아니면 in-memory.
	// 연결 확인(ping)에 실패하면 MemLocker 로 폴백한다(단일 인스턴스 best-effort).
	var locker lock.Locker = lock.NewMemLocker()
	if cfg.Redis.Addr != "" {
		rdb := redis.NewClient(&redis.Options{
			Addr:     cfg.Redis.Addr,
			Password: cfg.Redis.Password,
			DB:       cfg.Redis.DB,
		})
		pingCtx, cancelPing := context.WithTimeout(ctx, 3*time.Second)
		if err := rdb.Ping(pingCtx).Err(); err != nil {
			logger.Warn("redis 미연결 — 세션 락 in-memory 폴백(다중 레플리카 비권장)", zap.Error(err))
			_ = rdb.Close()
		} else {
			locker = lock.NewRedisLocker(rdb)
			defer rdb.Close() //nolint:errcheck
			logger.Info("redis 연결 — 분산 세션 락 활성", zap.String("addr", cfg.Redis.Addr))
		}
		cancelPing()
	}

	router, h := api.NewRouter(cfg, logger, sessions, validator, eventsPub, db, locker)

	// 검증 결과 소비 루프: 결과를 stepStore에 반영한다. ctx 취소(종료 신호) 시 graceful 종료.
	if consumer != nil {
		go func() {
			if err := consumer.Run(ctx, h.ApplyValidationResult); err != nil {
				logger.Error("validation consumer stopped", zap.Error(err))
			}
		}()
	}

	// 프로비저닝 타임아웃 reaper: ready 못 된 stuck 세션을 주기적으로 회수해 CDI 클론 thrash를 차단한다.
	go func() {
		ticker := time.NewTicker(2 * time.Minute)
		defer ticker.Stop()
		h.ReapStuckSessions(ctx) // 시작 직후 1회
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				h.ReapStuckSessions(ctx)
			}
		}
	}()

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
