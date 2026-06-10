// Package api wires the Gin router and middleware chain.
// 미들웨어 적용 순서: Recovery → Logger → CORS → JWT (protected 그룹만)
package api

import (
	"context"
	"time"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"github.com/requset700k/cledyu/api/internal/api/handlers"
	"github.com/requset700k/cledyu/api/internal/auth"
	"github.com/requset700k/cledyu/api/internal/config"
	"github.com/requset700k/cledyu/api/internal/events"
	"github.com/requset700k/cledyu/api/internal/kubevirt"
	"github.com/requset700k/cledyu/api/internal/middleware"
	"github.com/requset700k/cledyu/api/internal/store"
	"github.com/requset700k/cledyu/api/internal/validation"
	"go.uber.org/zap"
)

// NewRouter는 Gin 엔진과 함께 *handlers.Handler를 반환한다.
// 핸들러는 검증 결과 consumer가 stepStore를 갱신(ApplyValidationResult)하도록 main에서 참조한다.
// eventsPub은 학습 이벤트(lab-events) 발행기, db 는 PostgreSQL 영속 계층 — 둘 다 nil 허용(로컬/CI).
func NewRouter(cfg *config.Config, log *zap.Logger, sessions *kubevirt.Manager, validator validation.Publisher, eventsPub events.Publisher, db *store.Store) (*gin.Engine, *handlers.Handler) {
	gin.SetMode(cfg.Server.Mode)

	// Keycloak OIDC provider — discovery(.well-known) 수행. Keycloak 미가용(CI/로컬)
	// 환경에서는 nil 로 두고 graceful degradation 한다.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	authProvider, err := auth.NewProvider(ctx, cfg.Keycloak)
	if err != nil {
		log.Warn("oidc provider unavailable; auth flow disabled until Keycloak reachable", zap.Error(err))
		authProvider = nil
	}

	r := gin.New()
	r.Use(gin.Recovery())
	r.Use(middleware.Logger(log))
	r.Use(cors.New(cors.Config{
		// Next.js dev server(3000) 및 클러스터 프론트엔드에서의 요청 허용.
		// 프로덕션에서는 Traefik이 CORS를 처리하므로 이 설정은 로컬 개발 전용.
		AllowOrigins:     []string{"http://localhost:3000", "https://app.cledyu.local"},
		AllowMethods:     []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowHeaders:     []string{"Origin", "Authorization", "Content-Type"},
		AllowCredentials: true,
	}))

	h := handlers.New(cfg, log, sessions, validator, eventsPub, db, authProvider)

	r.GET("/health", h.Health)

	// 인증 불필요 — OIDC authorization code(PKCE) 흐름.
	r.GET("/api/v1/auth/login", h.Login)
	r.GET("/api/v1/auth/callback", h.Callback)
	r.GET("/api/v1/auth/logout", h.Logout)
	// silent refresh — access_token 만료 시 프론트가 호출(쿠키의 refresh_token grant).
	r.POST("/api/v1/auth/refresh", h.Refresh)

	if cfg.Server.Mode == "release" && authProvider == nil {
		log.Warn("running WITHOUT auth provider in release mode — protected routes will 503")
	}

	// release 에서는 provider 필수, debug 에서만 mock 신원 폴백 허용.
	devFallback := cfg.Server.Mode != "release"
	v1 := r.Group("/api/v1")
	v1.Use(middleware.JWT(authProvider, log, devFallback))
	{
		v1.GET("/me", h.GetMe)
		v1.GET("/labs", h.ListLabs)
		v1.GET("/labs/:id", h.GetLab)

		// 랩 세션 — KubeVirt VM 수명주기.
		v1.POST("/sessions", h.CreateSession)
		v1.GET("/sessions/:id", h.GetSession)
		v1.DELETE("/sessions/:id", h.DeleteSession)

		// 스텝 진행/검증 — publisher 미연결 시 mock 동작, 연결 시 Kafka로 발행.
		v1.GET("/sessions/:id/steps", h.GetSessionSteps)
		v1.POST("/sessions/:id/validate", h.ValidateStep)

		// AI 학습 도우미 — ai-tutor BFF 프록시(미가용 시 Lab DSL 정적 hint_levels 폴백).
		v1.POST("/sessions/:id/hint", h.RequestHint)

		// 실시간 터미널 — VM serial console을 WebSocket으로 프록시(JWT가 ?token= 처리).
		v1.GET("/sessions/:id/ws", h.Console)

		// 브라우저 IDE(code-server) — IDE 랩(lab DSL ide: true)만. 세션 소유자 전용 프록시.
		v1.Any("/sessions/:id/ide/*idepath", h.IDE)
	}

	return r, h
}
