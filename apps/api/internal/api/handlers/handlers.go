// Package handlers는 도메인별 HTTP 핸들러를 구현한다 (health, lab, user, auth).
package handlers

import (
	"github.com/gin-gonic/gin"
	"github.com/requset700k/cledyu/api/internal/config"
	"github.com/requset700k/cledyu/api/internal/kubevirt"
	"go.uber.org/zap"
)

// Handler는 모든 HTTP 핸들러의 공유 의존성을 보관한다.
type Handler struct {
	cfg      *config.Config
	log      *zap.Logger
	sessions *kubevirt.Manager
}

// New는 sessions에 nil을 허용한다 — kubevirt 없이 실행 시 세션 API가 503을 반환한다.
func New(cfg *config.Config, log *zap.Logger, sessions *kubevirt.Manager) *Handler {
	return &Handler{cfg: cfg, log: log, sessions: sessions}
}

// 프론트엔드 lib/api.ts의 ApiError 타입과 대응.
type errResp struct {
	Error string `json:"error"`
	Code  string `json:"code,omitempty"`
}

func (h *Handler) err(c *gin.Context, status int, msg string) {
	c.JSON(status, errResp{Error: msg})
}
