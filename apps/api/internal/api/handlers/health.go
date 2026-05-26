package handlers

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// Health는 서버 상태와 버전을 반환한다. 인증 불필요.
// GET /health
// TODO: Redis 연동 후 PING 결과를 응답에 포함.
func (h *Handler) Health(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"status":  "ok",
		"version": "0.1.0",
	})
}
