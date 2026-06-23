package handlers

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/requset700k/cledyu/api/internal/auth"
	"github.com/requset700k/cledyu/api/internal/session"
	"go.uber.org/zap"
)

// 승격 가능한 역할 — 임의 realm 역할 부여를 막는 화이트리스트.
var promotableRoles = map[string]bool{"instructor": true, "admin": true}

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

// GetUserActivity는 유저의 랩 완료 이력을 반환한다(관리자 활동 조회).
// GET /api/v1/admin/users/:uid/activity
func (h *Handler) GetUserActivity(c *gin.Context) {
	if h.db == nil {
		h.err(c, http.StatusServiceUnavailable, "user store not configured")
		return
	}
	completions, err := h.db.ListCompletionsByUser(c.Request.Context(), c.Param("uid"))
	if err != nil {
		h.log.Error("list user completions", zap.Error(err))
		h.err(c, http.StatusInternalServerError, "load activity failed")
		return
	}
	c.JSON(http.StatusOK, gin.H{"completions": completions, "total": len(completions)})
}

// SetUserRole은 유저를 instructor/admin 으로 승격한다(Keycloak realm 역할 추가).
// Keycloak 이 신원의 원본이므로 먼저 거기에 반영하고, 성공 시 DB 미러도 즉시 갱신한다
// (다음 로그인까지 기다리지 않고 admin 콘솔에 반영되도록).
// admin client 미설정 시 501. POST /api/v1/admin/users/:uid/role  body: { "role": "instructor" }
func (h *Handler) SetUserRole(c *gin.Context) {
	if h.kcAdmin == nil {
		h.err(c, http.StatusNotImplemented, "role management not configured")
		return
	}
	var req struct {
		Role string `json:"role" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		h.err(c, http.StatusBadRequest, "role is required")
		return
	}
	if !promotableRoles[req.Role] {
		h.err(c, http.StatusBadRequest, "role must be instructor or admin")
		return
	}
	uid := c.Param("uid")

	if err := h.kcAdmin.AssignRealmRole(c.Request.Context(), uid, req.Role); err != nil {
		switch {
		case errors.Is(err, auth.ErrUserNotFound):
			h.err(c, http.StatusNotFound, "user not found")
		case errors.Is(err, auth.ErrRoleNotFound):
			// realm 에 해당 역할이 없음 — 운영 설정 문제(Terraform 역할 미생성).
			h.err(c, http.StatusInternalServerError, "realm role not provisioned")
		default:
			h.log.Error("assign realm role", zap.Error(err), zap.String("uid", uid), zap.String("role", req.Role))
			h.err(c, http.StatusBadGateway, "keycloak role assignment failed")
		}
		return
	}

	// DB 미러 즉시 갱신(있을 때만 — 로그인 전 유저면 다음 로그인에서 upsert).
	if h.db != nil {
		if err := h.db.SetUserRole(c.Request.Context(), uid, req.Role); err != nil {
			// Keycloak 은 이미 바뀌었으므로 실패해도 치명적이지 않다(다음 로그인에 동기화).
			h.log.Warn("db role mirror update failed", zap.Error(err), zap.String("uid", uid))
		}
	}
	c.JSON(http.StatusOK, gin.H{"user_id": uid, "role": req.Role, "status": "promoted"})
}

// TerminateUserSession은 해당 유저의 활성 세션을 강제 종료한다(관리자 개입).
// 세션이 없으면 404. DELETE /api/v1/admin/users/:uid/session
func (h *Handler) TerminateUserSession(c *gin.Context) {
	if h.sessions == nil {
		h.err(c, http.StatusServiceUnavailable, "kubevirt not configured")
		return
	}
	uid := c.Param("uid")
	sessionID, err := h.sessions.FindActiveByUser(c.Request.Context(), uid)
	if err != nil {
		h.log.Error("find active session for terminate", zap.Error(err))
		h.err(c, http.StatusInternalServerError, "lookup session failed")
		return
	}
	if sessionID == "" {
		h.err(c, http.StatusNotFound, "no active session for user")
		return
	}

	if err := h.sessions.Delete(c.Request.Context(), sessionID); err != nil && !errors.Is(err, session.ErrNotFound) {
		h.log.Error("terminate session", zap.Error(err), zap.String("session_id", sessionID))
		h.err(c, http.StatusInternalServerError, "terminate session failed")
		return
	}
	// stepStore·DB 진행 상태도 정리(소유자 일반 삭제와 동일 경로).
	h.steps.remove(sessionID)
	c.JSON(http.StatusOK, gin.H{"user_id": uid, "session_id": sessionID, "status": "terminated"})
}
