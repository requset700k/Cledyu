package handlers

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// ListUsers는 등록 유저 목록을 반환한다(관리자 콘솔). RBAC 미들웨어(RequireMinRole("admin"))
// 가 라우터에서 접근을 막으므로 핸들러는 인가를 다시 검사하지 않는다.
// DB 미설정 시 503 — 유저 미러는 PostgreSQL 에만 존재한다.
// GET /api/v1/admin/users?limit=50
func (h *Handler) ListUsers(c *gin.Context) {
	if h.db == nil {
		h.err(c, http.StatusServiceUnavailable, "user store not configured")
		return
	}
	limit := 50
	if v := c.Query("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 500 {
			limit = n
		}
	}

	users, err := h.db.ListUsers(c.Request.Context(), limit)
	if err != nil {
		h.log.Error("list users", zap.Error(err))
		h.err(c, http.StatusInternalServerError, "list users failed")
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": users, "total": len(users)})
}
