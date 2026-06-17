package middleware

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/requset700k/cledyu/api/internal/auth"
	"go.uber.org/zap"
)

// JWT는 access_token(쿠키/Bearer/쿼리)을 Keycloak JWKS로 검증하는 미들웨어다.
//
// provider 가 nil 이면(Keycloak discovery 실패 — CI/로컬) 인증을 강제할 수 없으므로
// 모든 보호 요청을 503 으로 막는다. devFallback=true 인 경우에만(=debug 모드)
// mock 신원을 주입해 로컬 개발을 허용한다 — 운영(release)에서는 절대 우회 없음.
func JWT(provider *auth.Provider, log *zap.Logger, devFallback bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		if provider == nil {
			if devFallback {
				c.Set("user_id", "dev-user")
				c.Set("user_email", "dev@cledyu.local")
				c.Set("user_name", "Dev User")
				c.Set("user_role", "admin")
				c.Set("user_org", "public")
				c.Next()
				return
			}
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "auth provider unavailable"})
			c.Abort()
			return
		}

		token := extractToken(c)
		if token == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "missing token"})
			c.Abort()
			return
		}

		id, err := provider.VerifyAccessToken(c.Request.Context(), token)
		if err != nil {
			if log != nil {
				log.Debug("access token verification failed", zap.Error(err))
			}
			c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid token"})
			c.Abort()
			return
		}

		c.Set("user_id", id.Subject)
		c.Set("user_email", id.Email)
		c.Set("user_name", id.Name)
		c.Set("user_role", id.Role())
		c.Set("user_org", id.Org()) // RAG 멀티테넌트 — 소속 조직 collection(없으면 public)
		c.Next()
	}
}

func extractToken(c *gin.Context) string {
	// 쿠키가 실제 로그인 세션이다. 로컬 dev 의 Authorization 헤더(dev-token stub, apps/web
	// lib/api.ts DEV_HEADERS)보다 우선해야, 실 Keycloak 로그인 후에도 그 stub 헤더에 가로채여
	// 매 요청이 401 로 실패 → /login 으로 되돌아가는 문제가 생기지 않는다.
	if cookie, err := c.Cookie("access_token"); err == nil {
		return cookie
	}
	if h := c.GetHeader("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return strings.TrimPrefix(h, "Bearer ")
	}
	// Query param: WebSocket 업그레이드 시 브라우저가 헤더를 못 보냄.
	if t := c.Query("token"); t != "" {
		return t
	}
	return ""
}
