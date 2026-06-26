package handlers

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// GetMe는 검증된 JWT claims(미들웨어가 컨텍스트에 주입)에서 현재 사용자 정보를 반환한다. 인증 필요.
// GET /api/v1/me
// points = 난이도 가중 누적 점수. badges 는 v2 까지 STUB.
func (h *Handler) GetMe(c *gin.Context) {
	points := 0
	if h.db != nil {
		points, _ = h.userScore(c.Request.Context(), c.GetString("user_id"))
	}
	c.JSON(http.StatusOK, gin.H{
		"id":     c.GetString("user_id"),
		"email":  c.GetString("user_email"),
		"name":   c.GetString("user_name"),
		"role":   c.GetString("user_role"),
		"org":    c.GetString("user_org"),
		"points": points,
		"badges": []gin.H{},
	})
}
