// Package api wires the Gin router and middleware chain.
// 미들웨어 적용 순서: Recovery → Logger → CORS → JWT (protected 그룹만)
package api

import (
	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"github.com/requset700k/cledyu/api/internal/api/handlers"
	"github.com/requset700k/cledyu/api/internal/config"
	"github.com/requset700k/cledyu/api/internal/kubevirt"
	"github.com/requset700k/cledyu/api/internal/middleware"
	"go.uber.org/zap"
)

func NewRouter(cfg *config.Config, log *zap.Logger, sessions *kubevirt.Manager) *gin.Engine {
	gin.SetMode(cfg.Server.Mode)

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

	h := handlers.New(cfg, log, sessions)

	r.GET("/health", h.Health)

	// 인증 불필요 — 로그인 (mock: 쿠키 설정 후 /callback 리다이렉트)
	r.GET("/api/v1/auth/login", h.Login)

	if cfg.Server.Mode == "release" {
		log.Warn("JWT verification is running in STUB mode — replace with JWKS before handling real users")
	}

	// TODO: Keycloak JWKS 검증으로 교체 (현재 stub).
	v1 := r.Group("/api/v1")
	v1.Use(middleware.JWT())
	{
		v1.GET("/me", h.GetMe)
		v1.GET("/labs", h.ListLabs)
		v1.GET("/labs/:id", h.GetLab)

		// 랩 세션 — KubeVirt VM 수명주기.
		v1.POST("/sessions", h.CreateSession)
		v1.GET("/sessions/:id", h.GetSession)
		v1.DELETE("/sessions/:id", h.DeleteSession)

		// 스텝 진행/검증 — STUB(검증엔진 연동 전까지 in-memory mock).
		v1.GET("/sessions/:id/steps", h.GetSessionSteps)
		v1.POST("/sessions/:id/validate", h.ValidateStep)
	}

	return r
}
