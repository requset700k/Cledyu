package handlers

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// GetMe는 검증된 JWT claims(미들웨어가 컨텍스트에 주입)에서 현재 사용자 정보를 반환한다. 인증 필요.
// GET /api/v1/me
// points/badges는 아직 STUB — 학습 진행/뱃지 스토어 연동 시 실 조회로 교체.
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
