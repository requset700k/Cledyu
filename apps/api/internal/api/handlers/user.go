package handlers

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// GetMe는 JWT claims에서 추출한 현재 사용자 정보를 반환한다. 인증 필요.
// GET /api/v1/me
// TODO: Keycloak 연동 후 실 DB 조회로 교체.
func (h *Handler) GetMe(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"id":     c.GetString("user_id"),
		"email":  c.GetString("user_email"),
		"name":   c.GetString("user_name"),
		"role":   c.GetString("user_role"),
		"points": 0,
		"badges": []gin.H{},
	})
}
