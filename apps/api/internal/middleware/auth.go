package middleware

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// TODO: Keycloak 연동 완료 후 stub → 실 JWKS 검증으로 교체.
func JWT() gin.HandlerFunc {
	return func(c *gin.Context) {
		token := extractToken(c)
		if token == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "missing token"})
			c.Abort()
			return
		}

		// Stub: 비어있지 않은 토큰은 모두 허용, mock claims 주입.
		// admin role → 전체 엔드포인트 접근 가능 (개발 전용).
		c.Set("user_id", "mock-user-id")
		c.Set("user_email", "admin@cledyu.local")
		c.Set("user_name", "Dev Admin")
		c.Set("user_role", "admin")
		c.Next()
	}
}

func extractToken(c *gin.Context) string {
	if h := c.GetHeader("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return strings.TrimPrefix(h, "Bearer ")
	}
	if cookie, err := c.Cookie("access_token"); err == nil {
		return cookie
	}
	// Query param: WebSocket 업그레이드 시 브라우저가 헤더를 못 보냄.
	if t := c.Query("token"); t != "" {
		return t
	}
	return ""
}
