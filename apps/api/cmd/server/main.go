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
	"github.com/requset700k/cledyu/api/internal/bq"
	"github.com/requset700k/cledyu/api/internal/config"
	"github.com/requset700k/cledyu/api/internal/ec2"
	"github.com/requset700k/cledyu/api/internal/events"
	"github.com/requset700k/cledyu/api/internal/kube"
	"github.com/requset700k/cledyu/api/internal/kubevirt"
	"github.com/requset700k/cledyu/api/internal/lock"
	"github.com/requset700k/cledyu/api/internal/session"
	"github.com/requset700k/cledyu/api/internal/store"
	"github.com/requset700k/cledyu/api/internal/tailnet"
	"github.com/requset700k/cledyu/api/internal/validation"
	"github.com/requset700k/cledyu/api/internal/vmfiles"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.21.0"
	"go.uber.org/zap"
)

const (
	vmFileAccessTimeout       = 5 * time.Second
	vmFileAccessMaxConcurrent = 4
	otelSampleRatio           = 0.1 // 10% — 트래픽 늘면 조정
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

	otlpExp, otelErr := otlptracegrpc.New(ctx,
		otlptracegrpc.WithEndpoint("alloy.loki.svc.cluster.local:4317"),
		otlptracegrpc.WithInsecure(),
	)
	if otelErr != nil {
		logger.Warn("OTel exporter 초기화 실패 — trace 비활성", zap.Error(otelErr))
	} else {
		tp := sdktrace.NewTracerProvider(
			sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(otelSampleRatio))),
			sdktrace.WithBatcher(otlpExp),
			sdktrace.WithResource(resource.NewWithAttributes(
				semconv.SchemaURL,
				semconv.ServiceNameKey.String("cledyu-api"),
			)),
		)
		otel.SetTracerProvider(tp)

		otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
			propagation.TraceContext{},
			propagation.Baggage{},
		))

		defer func() {
			if err := tp.Shutdown(context.Background()); err != nil {
				logger.Error("OTel shutdown error", zap.Error(err))
			}
		}()
		logger.Info("OTel TracerProvider 초기화 완료")
	}

	// 세션 프로바이더 배선:
	//   - 온프렘 KubeVirt 매니저(primary)
	//   - AWS EC2 오버플로우 프로비저너(선택) — AWS.LaunchTemplateID 와 MaxActiveSessions>0 이면 활성
	//   - 둘 다 있으면 디스패처(온프렘 우선, 만석 시 EC2 버스트)로 묶는다
	// 주의: 타입 있는 nil 포인터를 인터페이스에 담으면 non-nil 인터페이스가 되는 Go 함정을
	// 피하려고, 각 프로바이더는 생성 성공 시에만 인터페이스에 대입한다.
	var onprem session.Provider
	if mgr, err := kubevirt.NewManager(&cfg.KubeVirt); err != nil {
		logger.Warn("kubevirt manager init failed, on-prem sessions disabled", zap.Error(err))
	} else {
		onprem = mgr
	}

	var overflow session.Provider
	if cfg.AWS.LaunchTemplateID != "" && cfg.AWS.MaxActiveSessions > 0 {
		if prov, err := ec2.NewProvisioner(ctx, &cfg.AWS); err != nil {
			logger.Warn("ec2 provisioner init failed, overflow disabled", zap.Error(err))
		} else {
			overflow = prov
		}
	}

	// 세션 API 가 503 을 반환하도록 둘 다 없으면 nil 인터페이스로 둔다.
	var sessions session.Provider
	switch {
	case onprem != nil && overflow != nil:
		sessions = session.NewDispatcher(onprem, overflow, cfg.KubeVirt.MaxActiveSessions, logger)
		logger.Info("EC2 오버플로우 활성 — 온프렘 만석 시 AWS 버스트",
			zap.Int("onprem_cap", cfg.KubeVirt.MaxActiveSessions),
			zap.Int("ec2_cap", cfg.AWS.MaxActiveSessions),
			zap.String("region", cfg.AWS.Region))
	case onprem != nil:
		sessions = onprem
	case overflow != nil:
		sessions = overflow
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
	var redisClient *redis.Client

	if cfg.Redis.Addr != "" {
		redisClient = redis.NewClient(&redis.Options{
			Addr:     cfg.Redis.Addr,
			Password: cfg.Redis.Password,
			DB:       cfg.Redis.DB,
		})
		pingCtx, cancelPing := context.WithTimeout(ctx, 3*time.Second)
		if err := redisClient.Ping(pingCtx).Err(); err != nil {
			logger.Warn("redis 미연결 — 세션 락 in-memory 폴백(다중 레플리카 비권장)", zap.Error(err))
			_ = redisClient.Close()
			redisClient = nil
		} else {
			locker = lock.NewRedisLocker(redisClient)
			defer redisClient.Close() //nolint:errcheck
			logger.Info("redis 연결 — 분산 세션 락 활성", zap.String("addr", cfg.Redis.Addr))
		}
		cancelPing()
	}

	router, h := api.NewRouter(cfg, logger, sessions, validator, eventsPub, db, locker, redisClient)

	// 세션 VM 파일 탐색 — 수동 새로고침 기반의 read-only 경로다. Secret/RBAC 미설정이면
	// 기능만 비활성화하고 API 서버는 계속 기동한다(파일 endpoint는 503).
	if cfg.KubeVirt.FileListSSHPrivateKeyPath == "" {
		logger.Warn("VM 파일 탐색 비활성 — CLEDYU_KUBEVIRT_FILE_LIST_SSH_PRIVATE_KEY_PATH 미설정")
	} else if restCfg, restErr := kube.NewRESTConfig(); restErr != nil {
		logger.Warn("VM 파일 탐색 비활성 — kube config 로드 실패", zap.Error(restErr))
	} else if runner, runnerErr := vmfiles.NewKubeVirtFileListRunner(restCfg, cfg.KubeVirt.FileListSSHPrivateKeyPath); runnerErr != nil {
		logger.Warn("VM 파일 탐색 비활성 — file-list SSH runner 생성 실패", zap.Error(runnerErr))
	} else {
		h.SetVMFiles(vmfiles.NewService(runner, vmFileAccessTimeout, vmFileAccessMaxConcurrent))
		logger.Info("VM 파일 탐색 활성", zap.Duration("timeout", vmFileAccessTimeout), zap.Int("max_concurrent", vmFileAccessMaxConcurrent))
	}

	// EC2 오버플로우 라이브 터미널 — api 가 tsnet 으로 tailnet 에 가입해 세션 인스턴스(MagicDNS)에
	// SSH PTY 를 붙인다. 클러스터 파드는 tailnet 에 직접 못 닿기 때문이다. authkey 미설정이면
	// 미기동(라이브 터미널 비활성, SSM 채점은 무관). 기동 실패도 graceful — 세션 API 는 계속 동작.
	if cfg.AWS.APITailscaleAuthKey != "" {
		tnCtx, tnCancel := context.WithTimeout(ctx, 60*time.Second)
		node, terr := tailnet.New(tnCtx, tailnet.Config{
			Hostname: "cledyu-api",
			AuthKey:  cfg.AWS.APITailscaleAuthKey,
			StateDir: cfg.AWS.APITailnetStateDir,
		}, logger)
		tnCancel()
		if terr != nil {
			logger.Warn("tailnet 노드 기동 실패 — EC2 라이브 터미널 비활성", zap.Error(terr))
		} else {
			defer node.Close() //nolint:errcheck
			h.SetEC2Dial(node.Dial)
			logger.Info("tailnet 노드 가입 — EC2 라이브 터미널 활성")
		}
	}

	// D3 강사 분석 — ProjectID 설정 시에만 BigQuery 조회기 주입(미설정 시 핸들러 503).
	if cfg.Analytics.ProjectID != "" {
		bqClient, bqErr := bq.NewClient(ctx, cfg.Analytics.ProjectID, cfg.Analytics.Dataset)
		if bqErr != nil {
			logger.Warn("BigQuery 분석 클라이언트 생성 실패 — 강사 분석 비활성", zap.Error(bqErr))
		} else {
			h.SetBQAnalytics(bqClient)
			defer bqClient.Close()
			logger.Info("BigQuery 분석 활성", zap.String("project", cfg.Analytics.ProjectID), zap.String("dataset", cfg.Analytics.Dataset))
		}
	}

	// 검증 결과 소비 루프: 결과를 stepStore에 반영한다. ctx 취소(종료 신호) 시 graceful 종료.
	if consumer != nil {
		go func() {
			if err := consumer.Run(ctx, h.ApplyValidationResult); err != nil {
				logger.Error("validation consumer stopped", zap.Error(err))
			}
		}()
	}

	// 세션 reaper 루프(2분): ① stuck(프로비저닝 실패) 회수로 CDI 클론 thrash 차단,
	// ② TTL(expires_at) 만료 세션 회수로 광고한 세션 수명 강제 + VM 풀 누수 방지.
	go func() {
		ticker := time.NewTicker(2 * time.Minute)
		defer ticker.Stop()
		reap := func() {
			h.ReapStuckSessions(ctx)
			h.ReapExpiredSessions(ctx)
		}
		reap() // 시작 직후 1회
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				reap()
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
